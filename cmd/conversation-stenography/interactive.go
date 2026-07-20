package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	conversationstenography "conversationstenography"
)

// interactiveChain drives the "chain-chat" and "chat" interactive REPLs:
// reading commands, dispatching sends/receives, and persisting state after
// every accepted message.
func interactiveChain(ctx context.Context, in io.Reader, out, errOut io.Writer, chain *conversationstenography.ConversationChain, state *persistedChainState, statePath, initialSender string, platformMode bool, stateKey []byte) error {
	activeSender := initialSender
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	if platformMode {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  ┌─────────────────────────────────────┐")
		fmt.Fprintln(out, "  │        🔒  Secure Chat Active       │")
		fmt.Fprintln(out, "  └─────────────────────────────────────┘")
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  Conversation:  %s\n", state.Conversation)
		fmt.Fprintf(out, "  You are:       %s\n", activeSender)
		fmt.Fprintf(out, "  State file:    %s\n", statePath)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  HOW TO USE:")
		fmt.Fprintln(out, "  • Type a message and press Enter → generates cover text to copy")
		fmt.Fprintln(out, "  • Long messages may produce multiple covers; paste them in order")
		fmt.Fprintln(out, "  • /paste SENDER → paste one received cover from someone")
		fmt.Fprintln(out, "  • /help → see all commands")
		fmt.Fprintln(out, "  • /quit → save and exit")
		fmt.Fprintln(out)
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
			fmt.Fprintln(out, "\n  Chat saved. Goodbye! 👋")
			return nil
		case line == "/help":
			if platformMode {
				fmt.Fprintln(out, `
  ┌─ Commands ────────────────────────────────────────┐
  │                                                    │
  │  Messaging:                                        │
  │    Just type     Send a message (generates cover)  │
  │    /paste NAME   Paste one received cover from NAME│
  │    /send         Multi-line message (end with /end)│
  │                                                    │
  │  Info:                                             │
  │    /show         Show conversation history         │
  │    /status       Show sync + pending covers        │
  │    /record N     Re-print transport record N       │
  │                                                    │
  │  Other:                                            │
  │    /quit         Save and exit                     │
  │                                                    │
  └────────────────────────────────────────────────────┘`)
			} else {
				fmt.Fprintln(out, `Commands:
  /as NAME          switch the local sender
  /paste NAME       paste one messaging-app cover; finish with /end (repeat in order)
  /send             type a multiline plaintext; finish with /end
  /receive JSON     accept a one-line record from another participant
  /show             print from|decrypted|encrypted history
  /status           show identity, state path, sync, and pending assemblies
  /record INDEX     print one transport record as JSON
  /quit             save and exit
Any other line is encrypted and sent as the active participant.`)
			}
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
			for _, pending := range chain.ExportPending() {
				mask := pendingReceivedMask(pending)
				fmt.Fprintf(out, "  pending from %s: %s (next part %d/%d)\n", pending.From, mask, pending.NextPart+1, pending.Total)
			}
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
				fmt.Fprintln(errOut, "  ⚠ Sender name cannot be empty. Usage: /paste TheirName")
				continue
			}
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  Paste the exact message received from %s below.\n", sender)
			fmt.Fprintln(out, "  Then type /end on a new line when done:")
			fmt.Fprintln(out)
			carrier, ok := readInteractiveBlock(scanner)
			if !ok {
				return scanner.Err()
			}
			plaintext, done, status, err := chain.ReceiveMessage(ctx, sender, carrier)
			if err != nil {
				fmt.Fprintln(errOut, "  ⚠ Could not decode:", err)
				continue
			}
			state.Records = chain.Records()
			state.Pending = chain.ExportPending()
			if done {
				if n := len(state.Records); n > 0 {
					state.Decrypted[fmt.Sprint(state.Records[n-1].Index)] = base64.StdEncoding.EncodeToString(plaintext)
				}
			}
			if err := saveChainState(statePath, *state, stateKey); err != nil {
				return err
			}
			if !done {
				fmt.Fprintf(out, "\n  Waiting for part %d/%d (sync %s).\n\n", status.Part+1, status.Total, status.SyncCode)
				continue
			}
			fmt.Fprintf(out, "\n  📩 Message from %s:\n  %s\n\n", sender, plaintext)
		case line == "/send":
			fmt.Fprintln(out, "  Type your message, then /end on a new line:")
			plaintext, ok := readInteractiveBlock(scanner)
			if !ok {
				return scanner.Err()
			}
			if err := interactiveSend(ctx, out, errOut, chain, state, statePath, activeSender, plaintext, platformMode, stateKey); err != nil {
				return err
			}
		case strings.HasPrefix(line, "/receive "):
			encoded := strings.TrimSpace(strings.TrimPrefix(line, "/receive "))
			var incoming conversationstenography.ChainRecord
			if err := json.Unmarshal([]byte(encoded), &incoming); err != nil {
				fmt.Fprintln(errOut, "invalid record:", err)
				continue
			}
			if incoming.Index != uint64(len(state.Records)) || incoming.SenderSequence != nextSenderSequence(state.Records, incoming.From) {
				fmt.Fprintln(errOut, "receive failed: record metadata does not match expected order")
				continue
			}
			plaintext, done, status, err := chain.ReceiveMessage(ctx, incoming.From, incoming.Encrypted)
			if err != nil {
				fmt.Fprintln(errOut, "receive failed:", err)
				continue
			}
			state.Records = chain.Records()
			state.Pending = chain.ExportPending()
			if done {
				if n := len(state.Records); n > 0 {
					state.Decrypted[fmt.Sprint(state.Records[n-1].Index)] = base64.StdEncoding.EncodeToString(plaintext)
				}
			}
			if err := saveChainState(statePath, *state, stateKey); err != nil {
				return err
			}
			if !done {
				fmt.Fprintf(out, "waiting for part %d/%d (sync %s)\n", status.Part+1, status.Total, status.SyncCode)
				continue
			}
			fmt.Fprintf(out, "decrypted %s> %s\n", incoming.From, plaintext)
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(errOut, "  Unknown command. Type /help to see available commands.")
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

func interactiveSend(ctx context.Context, out, errOut io.Writer, chain *conversationstenography.ConversationChain, state *persistedChainState, statePath, sender, plaintext string, platformMode bool, stateKey []byte) error {
	if plaintext == "" {
		fmt.Fprintln(errOut, "  Message cannot be empty.")
		return nil
	}
	fmt.Fprint(out, "  ⏳ Generating cover text...")
	records, err := chain.SendMessage(ctx, sender, []byte(plaintext))
	fmt.Fprint(out, "\r\033[K") // clear the thinking indicator
	if err != nil {
		fmt.Fprintln(errOut, "  ⚠ Send failed:", err)
		return nil
	}
	state.Records = chain.Records()
	state.Pending = chain.ExportPending()
	for _, record := range records {
		state.Decrypted[fmt.Sprint(record.Index)] = base64.StdEncoding.EncodeToString([]byte(plaintext))
	}
	if err := saveChainState(statePath, *state, stateKey); err != nil {
		return err
	}
	if platformMode {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  ┌─── COPY into your messaging app ───┐")
		if len(records) > 1 {
			fmt.Fprintln(out, "  (each paragraph below is one chat bubble — send in order)")
		}
		fmt.Fprintln(out)
		for i, record := range records {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "  %s\n", record.Encrypted)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  └─── END — send as "+sender+" ───────────────┘")
		fmt.Fprintln(out)
	} else {
		for _, record := range records {
			fmt.Fprintf(out, "%s\nsent[%d] %s\nrecord> ", record.Encrypted, record.Index, record.From)
			if err := json.NewEncoder(out).Encode(record); err != nil {
				return err
			}
		}
	}
	return nil
}

func pendingReceivedMask(pending conversationstenography.PendingAssembly) string {
	mask := make([]byte, pending.Total)
	for i, piece := range pending.Pieces {
		if piece != nil {
			mask[i] = '1'
		} else {
			mask[i] = '0'
		}
	}
	return string(mask)
}

func nextSenderSequence(records []conversationstenography.ChainRecord, from string) uint64 {
	var sequence uint64
	for _, record := range records {
		if record.From == from {
			sequence++
		}
	}
	return sequence
}
