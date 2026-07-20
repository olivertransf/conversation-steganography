package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	conversationstenography "conversationstenography"
)

// runSimulation creates two independent protocol participants backed by the
// same local model. Every carrier sent by one side is decoded by the other, so
// this exercises the real transport path without requiring another device.
func runSimulation(args []string, in io.Reader, out, errOut io.Writer) error {
	local, err := loadLocalGenerativeConfig(resolveSupportFile("conversation-stenography.local.json"))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	userA := fs.String("user-a", "Alice", "first simulated participant")
	userB := fs.String("user-b", "Bob", "second simulated participant")
	conversation := fs.String("conversation", "local-simulation", "simulation conversation identifier")
	secret := fs.String("secret", "", "shared phrase (skips prompt; overrides env)")
	devSecret := fs.Bool("dev-secret", false, "use built-in local-dev phrase (skips prompt; not for real chats)")
	manual := fs.Bool("manual", false, "do not auto-decode; /paste covers as the other person")
	modelName := fs.String("model", envOr("CONVERSATION_STENOGRAPHY_MODEL", local.Model), "Hugging Face model name or local directory")
	revision := fs.String("revision", envOr("CONVERSATION_STENOGRAPHY_REVISION", defaultString(local.Revision, "main")), "pinned model revision")
	python := fs.String("python", envOr("CONVERSATION_STENOGRAPHY_PYTHON", defaultString(local.Python, "python3")), "Python interpreter")
	runtimeName := fs.String("runtime", envOr("CONVERSATION_STENOGRAPHY_RUNTIME", defaultString(local.Runtime, "transformers")), "transformers or mlx")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("simulate does not accept positional arguments")
	}
	if *devSecret && strings.TrimSpace(*secret) != "" {
		return errors.New("use either -dev-secret or -secret, not both")
	}
	*userA = strings.TrimSpace(*userA)
	*userB = strings.TrimSpace(*userB)
	if *userA == "" || *userB == "" {
		return errors.New("simulated user names cannot be empty")
	}
	if *userA == *userB {
		return errors.New("-user-a and -user-b must be different")
	}
	if strings.TrimSpace(*conversation) == "" {
		return errors.New("-conversation cannot be empty")
	}
	if *modelName == "" {
		return errors.New("-model is required (run 'conversation-stenography setup' first)")
	}

	phrase := strings.TrimSpace(*secret)
	if *devSecret {
		phrase = simulateDevSecret
		fmt.Fprintln(errOut, "Using -dev-secret local phrase (not for real chats).")
	}
	key, err := conversationKeyPhrase(*conversation, phrase, phrase == "", errOut)
	if err != nil {
		return err
	}
	backendPath := resolveSupportFile("python/hf_model.py")
	modelArgs := []string{"--model", *modelName, "--revision", *revision}
	switch *runtimeName {
	case "mlx":
		backendPath = resolveSupportFile("python/mlx_model.py")
	case "transformers":
		modelArgs = append(modelArgs, "--device", "cpu", "--dtype", "float32")
	default:
		return fmt.Errorf("unknown runtime %q", *runtimeName)
	}

	fmt.Fprintln(out, "  ⏳ Loading model... (this may take a moment)")
	ctx := context.Background()
	model, err := conversationstenography.NewProcessModel(ctx, *python, append([]string{backendPath}, modelArgs...)...)
	if err != nil {
		return err
	}
	defer model.Close()

	cfg := conversationstenography.GenerativeConfig{
		Prompt: local.Prompt, TopN: local.TopN, Coding: local.Coding,
		Temperature: local.Temperature, FinishTokens: local.FinishTokens, ChainSystem: local.ChainSystem,
		StrictStyle: local.StrictStyle, CandidatePool: local.CandidatePool, RefreshSentences: local.RefreshSentences,
		CarrierTrials: local.CarrierTrials, NaturalnessSlack: local.NaturalnessSlack, SemanticJudge: local.SemanticJudge,
		SemanticThreshold: local.SemanticThreshold, LengthBias: local.LengthBias,
	}
	first, err := conversationstenography.NewConversationChain(model, key, *conversation, cfg)
	if err != nil {
		return err
	}
	first.SetCapacityOptions(local.MaxCoverChars, resolveCapacityTopN(local.CapacityTopN, local.TopN), local.CapacityLengthBias)
	second, err := conversationstenography.NewConversationChain(model, key, *conversation, cfg)
	if err != nil {
		return err
	}
	second.SetCapacityOptions(local.MaxCoverChars, resolveCapacityTopN(local.CapacityTopN, local.TopN), local.CapacityLengthBias)
	return simulateConversation(ctx, in, out, errOut, first, second, *userA, *userB, *manual)
}

func simulateConversation(ctx context.Context, in io.Reader, out, errOut io.Writer, first, second *conversationstenography.ConversationChain, userA, userB string, manual bool) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	activeName, otherName := userA, userB
	activeChain, otherChain := first, second

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ┌──────────────────────────────────────────┐")
	fmt.Fprintln(out, "  │       🧪  Two-user local simulation      │")
	fmt.Fprintln(out, "  └──────────────────────────────────────────┘")
	fmt.Fprintf(out, "  %s and %s are independent simulated participants.\n", userA, userB)
	if manual {
		fmt.Fprintln(out, "  Manual mode: copy the cover text, then paste it back to decode.")
		fmt.Fprintln(out, "  After each send you are prompted to paste (blank line or /end).")
		fmt.Fprintln(out, "  Use /switch, /show, or /quit.")
	} else {
		fmt.Fprintln(out, "  Type a secret message; the other user will decode it.")
		fmt.Fprintln(out, "  Turns alternate automatically. Use /switch, /show, or /quit.")
	}
	fmt.Fprintln(out)

	var history []string
	lastSender := ""
	for {
		fmt.Fprintf(out, "%s> ", activeName)
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := scanner.Text()
		switch {
		case line == "/quit" || line == "/exit":
			fmt.Fprintln(out, "  Simulation ended; no conversation state was saved.")
			return nil
		case line == "/switch":
			activeName, otherName = otherName, activeName
			activeChain, otherChain = otherChain, activeChain
			continue
		case line == "/show":
			if len(history) == 0 {
				fmt.Fprintln(out, "  No simulated messages yet.")
			} else {
				for _, entry := range history {
					fmt.Fprintln(out, "  "+entry)
				}
			}
			continue
		case line == "/help":
			if manual {
				fmt.Fprintln(out, "  Type a secret to send; copy the cover block and paste when prompted (blank line or /end). /paste [SENDER] also works. /switch, /show, /quit.")
			} else {
				fmt.Fprintln(out, "  Type a message to send it; /switch changes speaker; /show displays plaintext history; /quit exits.")
			}
			continue
		case line == "/paste" || strings.HasPrefix(line, "/paste "):
			if !manual {
				fmt.Fprintln(errOut, "  /paste is only available with -manual.")
				continue
			}
			sender := strings.TrimSpace(strings.TrimPrefix(line, "/paste"))
			if sender == "" {
				sender = lastSender
			}
			if sender == "" {
				sender = otherName
			}
			if sender == activeName {
				fmt.Fprintln(errOut, "  Paste as the recipient; /switch first if needed.")
				continue
			}
			if err := simulateManualPaste(ctx, scanner, out, errOut, activeChain, sender, activeName, &history); err != nil {
				return err
			}
			continue
		case line == "":
			fmt.Fprintln(errOut, "  Message cannot be empty.")
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(errOut, "  Unknown command. Type /help for commands.")
			continue
		}

		fmt.Fprint(out, "  Generating cover text...")
		doneProgress := withChainProgress(activeChain, out, "Encoding")
		records, err := activeChain.SendMessage(ctx, activeName, []byte(line))
		doneProgress()
		fmt.Fprint(out, "\r\033[K")
		if err != nil {
			return fmt.Errorf("%s send: %w", activeName, err)
		}
		lastSender = activeName
		printCopyableCovers(out, records)

		if manual {
			sender := activeName
			activeName, otherName = otherName, activeName
			activeChain, otherChain = otherChain, activeChain
			fmt.Fprintf(out, "  Now %s — paste the cover text below (blank line or /end when done).\n", activeName)
			if err := simulateManualPaste(ctx, scanner, out, errOut, activeChain, sender, activeName, &history); err != nil {
				return err
			}
			continue
		}

		fmt.Fprint(out, "  Decoding...")
		doneDecode := withChainProgress(otherChain, out, "Decoding")
		var decoded []byte
		var done bool
		for i, record := range records {
			var status conversationstenography.ReceiveStatus
			decoded, done, status, err = otherChain.ReceiveMessage(ctx, activeName, record.Encrypted)
			if err != nil {
				doneDecode()
				fmt.Fprint(out, "\r\033[K")
				return fmt.Errorf("%s receive cover %d/%d: %w", otherName, i+1, len(records), err)
			}
			if i < len(records)-1 && (done || !status.Waiting) {
				doneDecode()
				fmt.Fprint(out, "\r\033[K")
				return fmt.Errorf("simulation expected waiting after cover %d/%d", i+1, len(records))
			}
		}
		doneDecode()
		fmt.Fprint(out, "\r\033[K")
		if !done {
			return errors.New("simulation logical message incomplete after all covers")
		}
		fmt.Fprintf(out, "  %s decoded: %s\n\n", otherName, decoded)
		history = append(history, fmt.Sprintf("%s → %s: %s", activeName, otherName, line))
		activeName, otherName = otherName, activeName
		activeChain, otherChain = otherChain, activeChain
	}
}

func printCopyableCovers(out io.Writer, records []conversationstenography.ChainRecord) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "----- copy -----")
	for i, record := range records {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, record.Encrypted)
	}
	fmt.Fprintln(out, "----- end copy -----")
	if len(records) > 1 {
		fmt.Fprintln(out, "(multiple paragraphs: paste one at a time, in order)")
	}
	fmt.Fprintln(out)
}

// readCoverPaste reads pasted cover text until /end or a blank line after content.
func readCoverPaste(scanner *bufio.Scanner) (string, bool) {
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "/end" {
			return strings.Join(lines, "\n"), true
		}
		if line == "" {
			if len(lines) == 0 {
				continue
			}
			return strings.Join(lines, "\n"), true
		}
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n"), true
	}
	return "", false
}

func simulateManualPaste(ctx context.Context, scanner *bufio.Scanner, out, errOut io.Writer, chain *conversationstenography.ConversationChain, sender, recipient string, history *[]string) error {
	for {
		fmt.Fprintf(out, "paste> ")
		carrier, ok := readCoverPaste(scanner)
		if !ok {
			return scanner.Err()
		}
		if strings.TrimSpace(carrier) == "" {
			fmt.Fprintln(errOut, "  Empty paste; try again.")
			continue
		}
		fmt.Fprint(out, "  Decoding...")
		doneProgress := withChainProgress(chain, out, "Decoding")
		decoded, done, status, err := chain.ReceiveMessage(ctx, sender, carrier)
		doneProgress()
		fmt.Fprint(out, "\r\033[K")
		if err != nil {
			fmt.Fprintln(errOut, "  ⚠ Could not decode:", err)
			fmt.Fprintln(out, "  Paste again (one paragraph), then blank line or /end.")
			continue
		}
		if !done {
			fmt.Fprintf(out, "  Got part — waiting for %d/%d. Paste the next paragraph.\n", status.Part+1, status.Total)
			continue
		}
		fmt.Fprintf(out, "\n  📩 Message from %s:\n  %s\n\n", sender, decoded)
		*history = append(*history, fmt.Sprintf("%s → %s: %s", sender, recipient, decoded))
		return nil
	}
}
