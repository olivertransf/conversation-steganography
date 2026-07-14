package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"decalgo"
	"golang.org/x/term"
)

const usage = `decalgo - secure generative conversation codec

Usage:
  decalgo demo   [-conversation ID]
  decalgo encode [-conversation ID]
  decalgo decode [-conversation ID]
  decalgo generate -model MODEL [-prompt TEXT] [-top-n N] < payload > text
  decalgo extract  -model MODEL [-prompt TEXT] [-top-n N] < text > payload
  decalgo chain-send    -from NAME [-state FILE] < plaintext > record.json
  decalgo chain-receive [-state FILE] < record.json > plaintext
  decalgo chain-show    [-state FILE]
  decalgo chain-chat    -as NAME [-state FILE]
  decalgo chat          -conversation NAME -me NAME
  decalgo conversations

Set DECALGO_KEY to a base64-encoded key of at least 16 bytes. Generate one with:
  openssl rand -base64 32
For chat mode, two people can instead enter the same long shared phrase or set
DECALGO_SECRET. The phrase is not stored.

Modes:
  demo    Type plaintext; see the marked wire message and decoded plaintext.
  encode  Type plaintext; emit one marked wire message per line.
  decode  Paste one marked wire message per line; emit authenticated plaintext.
  generate  Encode stdin bytes into deterministic model token choices.
  extract   Recover stdin bytes from deterministic model token choices.
  chain-send     Add one sender's encrypted carrier to a persistent group chain.
  chain-receive  Authenticate, decrypt, and append the next group-chain record.
  chain-show     Print the locally known from/decrypted/encrypted conversation.
  chain-chat     Interactive multi-person tester with a warm model and state.
  chat           General messaging-app copy/paste client using a shared phrase.
  conversations  List locally stored named conversation states.

Enter /quit or press Ctrl-D to leave interactive modes. Chat state is encrypted
and persisted; messages must still be processed in their exact platform order.`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, usage)
		return nil
	}

	mode := args[0]
	if mode == "conversations" {
		if len(args) != 1 {
			return errors.New("conversations takes no arguments")
		}
		return listConversations(out)
	}
	if mode == "generate" || mode == "extract" {
		return runGenerative(mode, args[1:], in, out, errOut)
	}
	if mode == "chain-send" || mode == "chain-receive" || mode == "chain-show" || mode == "chain-chat" || mode == "chat" {
		return runChain(mode, args[1:], in, out, errOut)
	}
	if mode != "demo" && mode != "encode" && mode != "decode" {
		return fmt.Errorf("unknown mode %q\n\n%s", mode, usage)
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(errOut)
	conversation := fs.String("conversation", "test-chat", "conversation identifier")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	keyText := strings.TrimSpace(os.Getenv("DECALGO_KEY"))
	if keyText == "" {
		return errors.New("DECALGO_KEY is not set; use a base64-encoded random key")
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		return errors.New("DECALGO_KEY must be standard base64")
	}
	if len(key) < 16 {
		return errors.New("DECALGO_KEY must decode to at least 16 bytes")
	}

	scanner := bufio.NewScanner(in)
	// Chat messages can be substantially larger than Scanner's default limit.
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	switch mode {
	case "demo":
		return demo(scanner, out, key, *conversation)
	case "encode":
		return encode(scanner, out, key, *conversation)
	default:
		return decode(scanner, out, errOut, key, *conversation)
	}
}

func runGenerative(mode string, args []string, in io.Reader, out, errOut io.Writer) error {
	local, err := loadLocalGenerativeConfig(resolveSupportFile("decalgo.local.json"))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(errOut)
	modelName := fs.String("model", envOr("DECALGO_MODEL", local.Model), "Hugging Face model name or local directory")
	revision := fs.String("revision", envOr("DECALGO_REVISION", defaultString(local.Revision, "main")), "pinned Hugging Face model revision")
	prompt := fs.String("prompt", defaultString(local.Prompt, "The weather today is"), "shared initial prompt")
	promptFile := fs.String("prompt-file", "", "file containing the exact shared rolling model context")
	topNDefault := local.TopN
	if topNDefault == 0 {
		topNDefault = 8
	}
	topN := fs.Int("top-n", topNDefault, "power-of-two candidate count")
	coding := fs.String("coding", defaultString(local.Coding, "huffman"), "coding mode: huffman or uniform")
	temperatureDefault := local.Temperature
	if temperatureDefault == 0 {
		temperatureDefault = 0.75
	}
	temperature := fs.Float64("temperature", temperatureDefault, "distribution temperature used by the coder")
	finishTokens := fs.Int("finish-tokens", local.FinishTokens, "maximum greedy tokens used to finish naturally")
	secure := fs.Bool("secure", local.Secure, "encrypt and authenticate the payload using DECALGO_KEY")
	conversation := fs.String("conversation", defaultString(local.Conversation, "default-chat"), "authenticated conversation identifier")
	direction := fs.String("direction", defaultString(local.Direction, "sender-to-receiver"), "authenticated sender direction")
	sequence := fs.Uint64("sequence", 0, "authenticated message sequence number")
	pythonDefault := envOr("DECALGO_PYTHON", defaultString(local.Python, "python3"))
	runtimeDefault := envOr("DECALGO_RUNTIME", defaultString(local.Runtime, "transformers"))
	python := fs.String("python", pythonDefault, "Python interpreter (or DECALGO_PYTHON)")
	runtimeName := fs.String("runtime", runtimeDefault, "model runtime: transformers or mlx")
	backend := fs.String("backend", "", "custom model backend script")
	device := fs.String("device", "cpu", "PyTorch device")
	dtype := fs.String("dtype", "float32", "model dtype")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *modelName == "" {
		return errors.New("-model is required (or set DECALGO_MODEL)")
	}
	if *promptFile != "" {
		promptBytes, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("read prompt file: %w", err)
		}
		*prompt = string(promptBytes)
	}
	backendPath := *backend
	modelArgs := []string{"--model", *modelName, "--revision", *revision}
	if backendPath == "" {
		switch *runtimeName {
		case "transformers":
			backendPath = resolveSupportFile("hf_model.py")
			modelArgs = append(modelArgs, "--device", *device, "--dtype", *dtype)
		case "mlx":
			backendPath = resolveSupportFile("mlx_model.py")
		default:
			return fmt.Errorf("unknown runtime %q; want transformers or mlx", *runtimeName)
		}
	}

	ctx := context.Background()
	model, err := decalgo.NewProcessModel(ctx, *python, append([]string{backendPath}, modelArgs...)...)
	if err != nil {
		return err
	}
	defer model.Close()
	codec, err := decalgo.NewGenerativeCodec(model, decalgo.GenerativeConfig{Prompt: *prompt, TopN: *topN, Coding: *coding, Temperature: *temperature, FinishTokens: *finishTokens,
		StrictStyle: local.StrictStyle, CandidatePool: local.CandidatePool, RefreshSentences: local.RefreshSentences})
	if err != nil {
		return err
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	if mode == "generate" {
		if *secure {
			key, err := generativeKey()
			if err != nil {
				return err
			}
			data, err = decalgo.SealCarrierPayload(key, *conversation, *direction, *sequence, data)
			if err != nil {
				return err
			}
		}
		text, err := codec.Encode(ctx, data)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, text)
		return err
	}
	payload, err := codec.Decode(ctx, string(data))
	if err != nil {
		return err
	}
	if *secure {
		key, err := generativeKey()
		if err != nil {
			return err
		}
		payload, err = decalgo.OpenCarrierPayload(key, *conversation, *direction, *sequence, payload)
		if err != nil {
			return err
		}
	}
	_, err = out.Write(payload)
	return err
}

type persistedChainState struct {
	Version      int                   `json:"version"`
	Conversation string                `json:"conversation"`
	Records      []decalgo.ChainRecord `json:"records"`
	Decrypted    map[string]string     `json:"decrypted,omitempty"`
}

const chainStateVersion = 2

func runChain(mode string, args []string, in io.Reader, out, errOut io.Writer) error {
	local, err := loadLocalGenerativeConfig(resolveSupportFile("decalgo.local.json"))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(errOut)
	stateDefault := ".decalgo-chain.json"
	if mode == "chat" {
		stateDefault = ""
	}
	statePath := fs.String("state", stateDefault, "durable local chain state")
	conversation := fs.String("conversation", defaultString(local.Conversation, "default-chat"), "group conversation identifier")
	from := fs.String("from", "", "sender name (chain-send only)")
	me := fs.String("me", "", "your participant name (chat mode)")
	showFormat := fs.String("format", "jsonl", "chain-show format: jsonl or table")
	modelName := fs.String("model", envOr("DECALGO_MODEL", local.Model), "Hugging Face model name or local directory")
	revision := fs.String("revision", envOr("DECALGO_REVISION", defaultString(local.Revision, "main")), "pinned model revision")
	python := fs.String("python", envOr("DECALGO_PYTHON", defaultString(local.Python, "python3")), "Python interpreter")
	runtimeName := fs.String("runtime", envOr("DECALGO_RUNTIME", defaultString(local.Runtime, "transformers")), "transformers or mlx")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if mode == "chat" {
		if strings.TrimSpace(*me) == "" {
			return errors.New("-me is required for chat")
		}
		*from = *me
		if *statePath == "" {
			*statePath, err = defaultConversationState(*conversation, *me)
			if err != nil {
				return err
			}
		}
	}
	if directory := filepath.Dir(*statePath); directory != "." {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create state directory: %w", err)
		}
	}
	key, err := conversationKey(*conversation, mode == "chat", errOut)
	if err != nil {
		return err
	}
	state, err := loadChainState(*statePath, *conversation, key)
	if err != nil {
		return err
	}
	if mode == "chain-show" {
		return showChainState(out, state, *showFormat)
	}
	if (mode == "chain-send" || mode == "chain-chat" || mode == "chat") && strings.TrimSpace(*from) == "" {
		return errors.New("-from is required for chain-send and chain-chat")
	}
	if *modelName == "" {
		return errors.New("-model is required (or set DECALGO_MODEL)")
	}
	backendPath := resolveSupportFile("hf_model.py")
	modelArgs := []string{"--model", *modelName, "--revision", *revision}
	switch *runtimeName {
	case "mlx":
		backendPath = resolveSupportFile("mlx_model.py")
	case "transformers":
		modelArgs = append(modelArgs, "--device", "cpu", "--dtype", "float32")
	default:
		return fmt.Errorf("unknown runtime %q", *runtimeName)
	}
	ctx := context.Background()
	model, err := decalgo.NewProcessModel(ctx, *python, append([]string{backendPath}, modelArgs...)...)
	if err != nil {
		return err
	}
	defer model.Close()
	cfg := decalgo.GenerativeConfig{Prompt: local.Prompt, TopN: local.TopN, Coding: local.Coding,
		Temperature: local.Temperature, FinishTokens: local.FinishTokens, ChainSystem: local.ChainSystem,
		StrictStyle: local.StrictStyle, CandidatePool: local.CandidatePool, RefreshSentences: local.RefreshSentences}
	chain, err := decalgo.NewConversationChain(model, key, state.Conversation, cfg)
	if err != nil {
		return err
	}
	if err := chain.RestorePublic(state.Records); err != nil {
		return fmt.Errorf("restore chain state: %w", err)
	}
	if mode == "chain-chat" || mode == "chat" {
		return interactiveChain(ctx, in, out, errOut, chain, &state, *statePath, *from, mode == "chat", key)
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	if mode == "chain-send" {
		record, err := chain.Send(ctx, *from, data)
		if err != nil {
			return err
		}
		state.Records = chain.Records()
		state.Decrypted[fmt.Sprint(record.Index)] = base64.StdEncoding.EncodeToString(data)
		if err := saveChainState(*statePath, state, key); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(record)
	}
	var incoming decalgo.ChainRecord
	if err := json.Unmarshal(data, &incoming); err != nil {
		return fmt.Errorf("parse chain record: %w", err)
	}
	plaintext, accepted, err := chain.Receive(ctx, incoming.From, incoming.Encrypted)
	if err != nil {
		return err
	}
	if incoming.Index != accepted.Index || incoming.SenderSequence != accepted.SenderSequence {
		return errors.New("chain record metadata does not match expected order")
	}
	state.Records = chain.Records()
	state.Decrypted[fmt.Sprint(accepted.Index)] = base64.StdEncoding.EncodeToString(plaintext)
	if err := saveChainState(*statePath, state, key); err != nil {
		return err
	}
	_, err = out.Write(plaintext)
	return err
}

func interactiveChain(ctx context.Context, in io.Reader, out, errOut io.Writer, chain *decalgo.ConversationChain, state *persistedChainState, statePath, initialSender string, platformMode bool, stateKey []byte) error {
	activeSender := initialSender
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	if platformMode {
		fmt.Fprintf(out, "Conversation %q ready as %s.\nState: %s\nPaste received app messages with /paste SENDER.\n", state.Conversation, activeSender, statePath)
	} else {
		fmt.Fprintf(out, "Group chain ready as %s. Type /help for commands.\n", activeSender)
	}
	for {
		fmt.Fprintf(out, "%s> ", activeSender)
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := scanner.Text()
		switch {
		case line == "/quit" || line == "/exit":
			return nil
		case line == "/help":
			fmt.Fprintln(out, `Commands:
  /as NAME          switch the local sender
  /paste NAME       paste a raw messaging-app carrier; finish with /end
  /send             type a multiline plaintext; finish with /end
  /receive JSON     accept a one-line record from another participant
  /show             print from|decrypted|encrypted history
  /status           show identity, state path, and next global index
  /record INDEX     print one transport record as JSON
  /quit             save and exit
Any other line is encrypted and sent as the active participant.`)
		case strings.HasPrefix(line, "/as "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "/as "))
			if name == "" {
				fmt.Fprintln(errOut, "sender name cannot be empty")
				continue
			}
			activeSender = name
			fmt.Fprintf(out, "Now sending as %s.\n", activeSender)
		case line == "/show":
			if err := showChainState(out, *state, "table"); err != nil {
				return err
			}
		case line == "/status":
			fmt.Fprintf(out, "conversation=%q me=%q next_index=%d sync=%s state=%s\n", state.Conversation, activeSender, len(state.Records), chain.SyncCode(), statePath)
		case strings.HasPrefix(line, "/record "):
			var index int
			if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "/record ")), "%d", &index); err != nil || index < 0 || index >= len(state.Records) {
				fmt.Fprintln(errOut, "invalid record index")
				continue
			}
			fmt.Fprint(out, "record> ")
			if err := json.NewEncoder(out).Encode(state.Records[index]); err != nil {
				return err
			}
		case strings.HasPrefix(line, "/paste "):
			sender := strings.TrimSpace(strings.TrimPrefix(line, "/paste "))
			if sender == "" {
				fmt.Fprintln(errOut, "sender name cannot be empty")
				continue
			}
			fmt.Fprintln(out, "Paste the exact received carrier, then type /end on a new line:")
			carrier, ok := readInteractiveBlock(scanner)
			if !ok {
				return scanner.Err()
			}
			plaintext, accepted, err := chain.Receive(ctx, sender, carrier)
			if err != nil {
				fmt.Fprintln(errOut, "receive failed:", err)
				continue
			}
			state.Records = chain.Records()
			state.Decrypted[fmt.Sprint(accepted.Index)] = base64.StdEncoding.EncodeToString(plaintext)
			if err := saveChainState(statePath, *state, stateKey); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nRECEIVED from %s:\n%s\n\n", sender, plaintext)
		case line == "/send":
			fmt.Fprintln(out, "Type plaintext, then /end on a new line:")
			plaintext, ok := readInteractiveBlock(scanner)
			if !ok {
				return scanner.Err()
			}
			if err := interactiveSend(ctx, out, errOut, chain, state, statePath, activeSender, plaintext, platformMode, stateKey); err != nil {
				return err
			}
		case strings.HasPrefix(line, "/receive "):
			encoded := strings.TrimSpace(strings.TrimPrefix(line, "/receive "))
			var incoming decalgo.ChainRecord
			if err := json.Unmarshal([]byte(encoded), &incoming); err != nil {
				fmt.Fprintln(errOut, "invalid record:", err)
				continue
			}
			if incoming.Index != uint64(len(state.Records)) || incoming.SenderSequence != nextSenderSequence(state.Records, incoming.From) {
				fmt.Fprintln(errOut, "receive failed: record metadata does not match expected order")
				continue
			}
			plaintext, accepted, err := chain.Receive(ctx, incoming.From, incoming.Encrypted)
			if err != nil {
				fmt.Fprintln(errOut, "receive failed:", err)
				continue
			}
			if incoming.Index != accepted.Index || incoming.SenderSequence != accepted.SenderSequence {
				fmt.Fprintln(errOut, "receive failed: record metadata does not match expected order")
				continue
			}
			state.Records = chain.Records()
			state.Decrypted[fmt.Sprint(accepted.Index)] = base64.StdEncoding.EncodeToString(plaintext)
			if err := saveChainState(statePath, *state, stateKey); err != nil {
				return err
			}
			fmt.Fprintf(out, "decrypted[%d] %s> %s\n", accepted.Index, accepted.From, plaintext)
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(errOut, "unknown command; type /help")
		default:
			if err := interactiveSend(ctx, out, errOut, chain, state, statePath, activeSender, line, platformMode, stateKey); err != nil {
				return err
			}
		}
	}
}

func readInteractiveBlock(scanner *bufio.Scanner) (string, bool) {
	var lines []string
	for scanner.Scan() {
		if scanner.Text() == "/end" {
			return strings.Join(lines, "\n"), true
		}
		lines = append(lines, scanner.Text())
	}
	return "", false
}

func interactiveSend(ctx context.Context, out, errOut io.Writer, chain *decalgo.ConversationChain, state *persistedChainState, statePath, sender, plaintext string, platformMode bool, stateKey []byte) error {
	if plaintext == "" {
		fmt.Fprintln(errOut, "message cannot be empty")
		return nil
	}
	budget, err := chain.EncodingBudget([]byte(plaintext))
	if err != nil {
		fmt.Fprintln(errOut, "budget failed:", err)
		return nil
	}
	fmt.Fprintf(out, "Encoding budget (local only): plaintext=%d packed=%d authentication=%d framing=%d termination=%d total=%d bytes.\n",
		budget.PlaintextBytes, budget.PackedBytes, budget.AuthenticationBytes, budget.FrameLengthBytes, budget.TerminationBytes, budget.TotalHiddenBytes)
	fmt.Fprintln(out, "Generating carrier…")
	record, err := chain.Send(ctx, sender, []byte(plaintext))
	if err != nil {
		fmt.Fprintln(errOut, "send failed:", err)
		return nil
	}
	state.Records = chain.Records()
	state.Decrypted[fmt.Sprint(record.Index)] = base64.StdEncoding.EncodeToString([]byte(plaintext))
	if err := saveChainState(statePath, *state, stateKey); err != nil {
		return err
	}
	if platformMode {
		fmt.Fprintf(out, "\nSEND THIS TEXT in the messaging app as %s:\n----- BEGIN DECALGO MESSAGE -----\n%s\n----- END DECALGO MESSAGE -----\n\n", sender, record.Encrypted)
	} else {
		fmt.Fprintf(out, "sent[%d] %s\nrecord> ", record.Index, record.From)
		if err := json.NewEncoder(out).Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func nextSenderSequence(records []decalgo.ChainRecord, from string) uint64 {
	var sequence uint64
	for _, record := range records {
		if record.From == from {
			sequence++
		}
	}
	return sequence
}

type encryptedChainState struct {
	Format string `json:"format"`
	Nonce  string `json:"nonce"`
	Body   string `json:"body"`
}

func loadChainState(path, conversation string, key []byte) (persistedChainState, error) {
	state := persistedChainState{Version: chainStateVersion, Conversation: conversation, Decrypted: make(map[string]string)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read chain state: %w", err)
	}
	var envelope encryptedChainState
	if err := json.Unmarshal(b, &envelope); err == nil && envelope.Format == "DCS1" {
		aead, err := chainStateAEAD(key)
		if err != nil {
			return state, err
		}
		nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
		if err != nil || len(nonce) != aead.NonceSize() {
			return state, errors.New("invalid encrypted state nonce")
		}
		body, err := base64.RawStdEncoding.DecodeString(envelope.Body)
		if err != nil {
			return state, errors.New("invalid encrypted state body")
		}
		b, err = aead.Open(nil, nonce, body, []byte("decalgo-state-v1\x00"+conversation))
		if err != nil {
			return state, errors.New("cannot decrypt conversation state: wrong phrase, key, or conversation")
		}
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("parse conversation state: %w", err)
	}
	if state.Version != chainStateVersion {
		return state, fmt.Errorf("conversation state uses incompatible protocol version %d (current %d); both participants must archive/delete their old state for this conversation and start fresh", state.Version, chainStateVersion)
	}
	if state.Conversation != conversation {
		return state, fmt.Errorf("state conversation %q does not match %q", state.Conversation, conversation)
	}
	if state.Decrypted == nil {
		state.Decrypted = make(map[string]string)
	}
	return state, nil
}

func saveChainState(path string, state persistedChainState, key []byte) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	aead, err := chainStateAEAD(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	body := aead.Seal(nil, nonce, b, []byte("decalgo-state-v1\x00"+state.Conversation))
	envelope, err := json.MarshalIndent(encryptedChainState{Format: "DCS1", Nonce: base64.RawStdEncoding.EncodeToString(nonce), Body: base64.RawStdEncoding.EncodeToString(body)}, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(envelope, '\n'), 0600); err != nil {
		return fmt.Errorf("write chain state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit chain state: %w", err)
	}
	return nil
}

func chainStateAEAD(key []byte) (cipher.AEAD, error) {
	material := sha256.New()
	material.Write([]byte("decalgo-state-key-v1\x00"))
	material.Write(key)
	block, err := aes.NewCipher(material.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func showChainState(out io.Writer, state persistedChainState, format string) error {
	if format != "jsonl" && format != "table" {
		return errors.New("format must be jsonl or table")
	}
	if format == "table" {
		fmt.Fprintln(out, "from|decrypted|encrypted")
	}
	encoder := json.NewEncoder(out)
	for _, record := range state.Records {
		row := struct {
			From      string `json:"from"`
			Decrypted string `json:"decrypted,omitempty"`
			Encrypted string `json:"encrypted"`
		}{From: record.From, Encrypted: record.Encrypted}
		if encoded := state.Decrypted[fmt.Sprint(record.Index)]; encoded != "" {
			if plaintext, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				row.Decrypted = string(plaintext)
			}
		}
		if format == "table" {
			fmt.Fprintf(out, "%q|%q|%q\n", row.From, row.Decrypted, row.Encrypted)
		} else if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

type localGenerativeConfig struct {
	Runtime          string  `json:"runtime"`
	Python           string  `json:"python"`
	Model            string  `json:"model"`
	Revision         string  `json:"revision"`
	Prompt           string  `json:"prompt"`
	TopN             int     `json:"top_n"`
	Coding           string  `json:"coding"`
	Temperature      float64 `json:"temperature"`
	Secure           bool    `json:"secure"`
	Conversation     string  `json:"conversation"`
	Direction        string  `json:"direction"`
	FinishTokens     int     `json:"finish_tokens"`
	ChainSystem      string  `json:"chain_system"`
	StrictStyle      bool    `json:"strict_style"`
	CandidatePool    int     `json:"candidate_pool"`
	RefreshSentences bool    `json:"refresh_sentences"`
}

func generativeKey() ([]byte, error) {
	keyText := strings.TrimSpace(os.Getenv("DECALGO_KEY"))
	if keyText == "" {
		return nil, errors.New("DECALGO_KEY is required in secure mode")
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) < 16 {
		return nil, errors.New("DECALGO_KEY must be base64 encoding of at least 16 bytes")
	}
	return key, nil
}

func conversationKey(conversation string, allowPrompt bool, errOut io.Writer) ([]byte, error) {
	phrase := os.Getenv("DECALGO_SECRET")
	keyText := strings.TrimSpace(os.Getenv("DECALGO_KEY"))
	if phrase == "" && allowPrompt && keyText == "" {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return nil, errors.New("cannot prompt for secret phrase; set DECALGO_SECRET")
		}
		defer tty.Close()
		fmt.Fprint(tty, "Shared secret phrase: ")
		password, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		if err != nil {
			return nil, fmt.Errorf("read secret phrase: %w", err)
		}
		phrase = string(password)
	}
	if phrase != "" {
		key, err := decalgo.DeriveKeyFromPhrase(phrase, conversation)
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(errOut, "Using the shared phrase; it is not stored in conversation state.")
		return key, nil
	}
	if keyText != "" {
		key, err := base64.StdEncoding.DecodeString(keyText)
		if err != nil || len(key) < 16 {
			return nil, errors.New("DECALGO_KEY must be base64 encoding of at least 16 bytes")
		}
		return key, nil
	}
	return nil, errors.New("set DECALGO_SECRET to the physically shared phrase (or DECALGO_KEY)")
}

func defaultConversationState(conversation, me string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	digest := sha256.Sum256([]byte(conversation + "\x00" + me))
	name := safeStateName(conversation) + "--" + safeStateName(me) + "--" + fmt.Sprintf("%x", digest[:5]) + ".json"
	return filepath.Join(home, ".decalgo", "conversations", name), nil
}

func listConversations(out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(home, ".decalgo", "conversations")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No local conversations yet.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fmt.Fprintln(out, strings.TrimSuffix(entry.Name(), ".json"))
		count++
	}
	if count == 0 {
		fmt.Fprintln(out, "No local conversations yet.")
	}
	return nil
}

func safeStateName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "conversation"
	}
	return name
}

func loadLocalGenerativeConfig(path string) (localGenerativeConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return localGenerativeConfig{}, nil
	}
	if err != nil {
		return localGenerativeConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg localGenerativeConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func resolveSupportFile(name string) string {
	if name == "decalgo.local.json" {
		if configured := os.Getenv("DECALGO_CONFIG"); configured != "" {
			return configured
		}
	}
	if _, err := os.Stat(name); err == nil {
		return name
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return name
}

func demo(scanner *bufio.Scanner, out io.Writer, key []byte, conversation string) error {
	encoder, err := decalgo.NewEncoder(key, conversation)
	if err != nil {
		return err
	}
	decoder, err := decalgo.NewDecoder(key, conversation)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Demo ready. Type a message and press Enter (/quit to leave).")
	for {
		fmt.Fprint(out, "plain> ")
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "/quit" {
			break
		}
		wire, err := encoder.Encode(line)
		if err != nil {
			return err
		}
		plain, err := decoder.Decode(wire)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "wire > %s\nclear> %s\n", wire, plain)
	}
	return scanner.Err()
}

func encode(scanner *bufio.Scanner, out io.Writer, key []byte, conversation string) error {
	encoder, err := decalgo.NewEncoder(key, conversation)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Encoder ready. Type a message and press Enter (/quit to leave).")
	for {
		fmt.Fprint(out, "plain> ")
		if !scanner.Scan() {
			break
		}
		if scanner.Text() == "/quit" {
			break
		}
		wire, err := encoder.Encode(scanner.Text())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "wire > %s\n", wire)
	}
	return scanner.Err()
}

func decode(scanner *bufio.Scanner, out, errOut io.Writer, key []byte, conversation string) error {
	decoder, err := decalgo.NewDecoder(key, conversation)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Decoder ready. Paste a marked message and press Enter (/quit to leave).")
	for {
		fmt.Fprint(out, "wire > ")
		if !scanner.Scan() {
			break
		}
		if scanner.Text() == "/quit" {
			break
		}
		plain, err := decoder.Decode(strings.TrimSpace(scanner.Text()))
		if err != nil {
			// Authentication errors do not advance decoder state, so retrying is safe.
			fmt.Fprintln(errOut, "decode error:", err)
			continue
		}
		fmt.Fprintf(out, "clear> %s\n", plain)
	}
	return scanner.Err()
}
