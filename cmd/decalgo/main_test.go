package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"decalgo"
)

func TestDemo(t *testing.T) {
	t.Setenv("DECALGO_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	var out, errOut bytes.Buffer
	err := run([]string{"demo", "-conversation", "test"}, strings.NewReader("hello\nsecond message\n/quit\n"), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "wire > enc:DEC1.") != 2 {
		t.Fatalf("expected two marked messages, got:\n%s", text)
	}
	if !strings.Contains(text, "clear> hello") || !strings.Contains(text, "clear> second message") {
		t.Fatalf("missing decoded output:\n%s", text)
	}
}

func TestChainStatePersistenceAndShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	key := []byte("0123456789abcdef0123456789abcdef")
	state := persistedChainState{Version: chainStateVersion, Conversation: "friends", Decrypted: map[string]string{
		"0": base64.StdEncoding.EncodeToString([]byte("hi alex")),
	}, Records: []decalgo.ChainRecord{{Index: 0, From: "samir", SenderSequence: 0, Encrypted: "cover text"}}}
	if err := saveChainState(path, state, key); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(onDisk, []byte("hi alex")) || bytes.Contains(onDisk, []byte("cover text")) {
		t.Fatal("state was stored in plaintext")
	}
	loaded, err := loadChainState(path, "friends", key)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showChainState(&out, loaded, "jsonl"); err != nil {
		t.Fatal(err)
	}
	var row struct{ From, Decrypted, Encrypted string }
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &row); err != nil {
		t.Fatal(err)
	}
	if row.From != "samir" || row.Decrypted != "hi alex" || row.Encrypted != "cover text" {
		t.Fatalf("bad row: %#v", row)
	}
	if _, err := loadChainState(path, "other", key); err == nil {
		t.Fatal("conversation mismatch accepted")
	}
	if _, err := loadChainState(path, "friends", []byte("abcdef0123456789abcdef0123456789")); err == nil {
		t.Fatal("wrong state key accepted")
	}
	out.Reset()
	if err := showChainState(&out, loaded, "table"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "from|decrypted|encrypted\n\"samir\"|\"hi alex\"|\"cover text\"") {
		t.Fatalf("bad table output: %s", out.String())
	}
}

func TestRejectsIncompatibleChainStateVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-state.json")
	key := []byte("0123456789abcdef0123456789abcdef")
	state := persistedChainState{Version: chainStateVersion - 1, Conversation: "friends", Decrypted: map[string]string{}}
	if err := saveChainState(path, state, key); err != nil {
		t.Fatal(err)
	}
	if _, err := loadChainState(path, "friends", key); err == nil || !strings.Contains(err.Error(), "start fresh") {
		t.Fatalf("expected actionable version error, got %v", err)
	}
}

func TestRequiresKey(t *testing.T) {
	t.Setenv("DECALGO_KEY", "")
	err := run([]string{"demo"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DECALGO_KEY") {
		t.Fatalf("expected key error, got %v", err)
	}
}

func TestNextSenderSequence(t *testing.T) {
	records := []decalgo.ChainRecord{{From: "samir"}, {From: "alex"}, {From: "samir"}}
	if got := nextSenderSequence(records, "samir"); got != 2 {
		t.Fatalf("samir sequence %d", got)
	}
	if got := nextSenderSequence(records, "karan"); got != 0 {
		t.Fatalf("karan sequence %d", got)
	}
}

func TestConversationSecretOverridesLegacyKey(t *testing.T) {
	phrase := "correct horse battery staple shared phrase"
	t.Setenv("DECALGO_SECRET", phrase)
	t.Setenv("DECALGO_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	got, err := conversationKey("friends", false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := decalgo.DeriveKeyFromPhrase(phrase, "friends")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("legacy key overrode explicit shared phrase")
	}
}

func TestDefaultConversationStorageAndListing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultConversationState("Family Chat", "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(home, ".decalgo", "conversations")) {
		t.Fatalf("unexpected path %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listConversations(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "family-chat--alex") {
		t.Fatalf("missing conversation: %s", out.String())
	}
}
