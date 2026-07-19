package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	conversationstenography "conversationstenography"

	"golang.org/x/term"
)

// generativeKey reads the plain CONVERSATION_STENOGRAPHY_KEY used by the (non-phrase-based)
// generative codec secure mode.
func generativeKey() ([]byte, error) {
	keyText := strings.TrimSpace(envOr("CONVERSATION_STENOGRAPHY_KEY", ""))
	if keyText == "" {
		return nil, errors.New("CONVERSATION_STENOGRAPHY_KEY is required in secure mode")
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) < 16 {
		return nil, errors.New("CONVERSATION_STENOGRAPHY_KEY must be base64 encoding of at least 16 bytes")
	}
	return key, nil
}

// conversationKey resolves the key for a conversation chain, preferring a
// shared secret phrase (CONVERSATION_STENOGRAPHY_SECRET, or an interactive prompt) over the
// legacy CONVERSATION_STENOGRAPHY_KEY.
func conversationKey(conversation string, allowPrompt bool, errOut io.Writer) ([]byte, error) {
	return conversationKeyPhrase(conversation, "", allowPrompt, errOut)
}

// conversationKeyPhrase is like conversationKey but uses phrase when non-empty
// (skips env and interactive prompt for the phrase).
func conversationKeyPhrase(conversation, phrase string, allowPrompt bool, errOut io.Writer) ([]byte, error) {
	phrase = strings.TrimSpace(phrase)
	keyText := strings.TrimSpace(envOr("CONVERSATION_STENOGRAPHY_KEY", ""))
	if phrase == "" {
		phrase = envOr("CONVERSATION_STENOGRAPHY_SECRET", "")
	}
	if phrase == "" && allowPrompt && keyText == "" {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return nil, errors.New("cannot prompt for secret phrase; set CONVERSATION_STENOGRAPHY_SECRET")
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
		key, err := conversationstenography.DeriveKeyFromPhrase(phrase, conversation)
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(errOut, "Using the shared phrase; it is not stored in conversation state.")
		return key, nil
	}
	if keyText != "" {
		key, err := base64.StdEncoding.DecodeString(keyText)
		if err != nil || len(key) < 16 {
			return nil, errors.New("CONVERSATION_STENOGRAPHY_KEY must be base64 encoding of at least 16 bytes")
		}
		return key, nil
	}
	return nil, errors.New("set CONVERSATION_STENOGRAPHY_SECRET to the physically shared phrase (or CONVERSATION_STENOGRAPHY_KEY)")
}

// simulateDevSecret is a fixed phrase for local simulate only. Never use for real chats.
const simulateDevSecret = "local-dev-only-not-for-real-chat"
