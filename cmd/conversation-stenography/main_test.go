package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	conversationstenography "conversationstenography"
)

type simulationTestModel struct{}

func (simulationTestModel) Fingerprint() string { return "simulation-test-v1" }
func (simulationTestModel) Tokenize(_ context.Context, text string) ([]int, error) {
	ids := make([]int, 0, len(text))
	for _, r := range text {
		if r >= 'a' && r <= 'h' {
			ids = append(ids, int(r-'a'))
		} else {
			ids = append(ids, 1000+int(r))
		}
	}
	return ids, nil
}
func (simulationTestModel) Detokenize(_ context.Context, ids []int) (string, error) {
	runes := make([]rune, len(ids))
	for i, id := range ids {
		runes[i] = rune('a' + id)
	}
	return string(runes), nil
}
func (simulationTestModel) Next(_ context.Context, tokens []int, n int) ([]conversationstenography.TokenCandidate, error) {
	out := make([]conversationstenography.TokenCandidate, n)
	for i := range out {
		out[i] = conversationstenography.TokenCandidate{ID: (len(tokens) + i) % n, LogProb: float64(n - i)}
	}
	return out, nil
}

func TestChainStatePersistenceAndShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	key := []byte("0123456789abcdef0123456789abcdef")
	state := persistedChainState{Version: chainStateVersion, Conversation: "friends", Decrypted: map[string]string{
		"0": base64.StdEncoding.EncodeToString([]byte("hi alex")),
	}, Records: []conversationstenography.ChainRecord{{Index: 0, From: "samir", SenderSequence: 0, Encrypted: "cover text"}}}
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

func TestNextSenderSequence(t *testing.T) {
	records := []conversationstenography.ChainRecord{{From: "samir"}, {From: "alex"}, {From: "samir"}}
	if got := nextSenderSequence(records, "samir"); got != 2 {
		t.Fatalf("samir sequence %d", got)
	}
	if got := nextSenderSequence(records, "karan"); got != 0 {
		t.Fatalf("karan sequence %d", got)
	}
}

func TestConversationSecretOverridesLegacyKey(t *testing.T) {
	phrase := "correct horse battery staple shared phrase"
	t.Setenv("CONVERSATION_STENOGRAPHY_SECRET", phrase)
	t.Setenv("CONVERSATION_STENOGRAPHY_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	got, err := conversationKey("friends", false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := conversationstenography.DeriveKeyFromPhrase(phrase, "friends")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("legacy key overrode explicit shared phrase")
	}
}

func TestLegacyEnvironmentVariableFallback(t *testing.T) {
	t.Setenv("CONVERSATION_STENOGRAPHY_MODEL", "")
	t.Setenv("DECALGO_MODEL", "/legacy/model")
	if got := envOr("CONVERSATION_STENOGRAPHY_MODEL", "fallback"); got != "/legacy/model" {
		t.Fatalf("legacy variable fallback = %q", got)
	}
}

func TestDefaultConversationStorageAndListing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := defaultConversationState("Family Chat", "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(home, ".conversation-stenography", "conversations")) {
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

func TestHelpOutput(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"help"}, strings.NewReader(""), &out, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Conversation Stenography") {
		t.Fatal("help output missing program name")
	}
}

func TestDefaultRunWithoutConfig(t *testing.T) {
	// When there's no config file, runDefault should try setup
	// We can't fully test the wizard (needs interactive input), but we can
	// verify it at least starts without panicking
	t.Setenv("CONVERSATION_STENOGRAPHY_CONFIG", filepath.Join(t.TempDir(), "nonexistent.json"))
	var out, errOut bytes.Buffer
	// Send empty input to trigger EOF during setup, which is fine
	err := run(nil, strings.NewReader(""), &out, &errOut)
	// We expect it to fail with EOF during the wizard, not panic
	_ = err
	if !strings.Contains(out.String(), "Welcome") {
		t.Fatalf("expected welcome message, got: %s", out.String())
	}
}

func TestDownloadModelIgnoresUnmarkedOutput(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "models--example--model", "snapshots", "abc123")
	if err := os.MkdirAll(modelDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(dir, "python")
	script := "#!/bin/sh\nprintf '\\033[32mDownloaded\\033[0m\\n'\nprintf 'CONVERSATION_STENOGRAPHY_MODEL_PATH=%s\\n' '" + modelDir + "'\n"
	if err := os.WriteFile(python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	path, revision, err := downloadModel(python, modelChoice{HuggingFace: "example/model"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if path != modelDir {
		t.Fatalf("path contains downloader output: %q", path)
	}
	if revision != "abc123" {
		t.Fatalf("revision = %q", revision)
	}
}

func TestValidateModelPathRejectsStatusText(t *testing.T) {
	if err := validateModelPath("\x1b[32mDownloaded\x1b[0m\n/path"); err == nil {
		t.Fatal("status text was accepted as a model path")
	}
}

func TestSaveLocalGenerativeConfigCreatesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "conversationstenography.json")
	want := localGenerativeConfig{Runtime: "transformers", Model: "/tmp/model"}
	if err := saveLocalGenerativeConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadLocalGenerativeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime != want.Runtime || got.Model != want.Model {
		t.Fatalf("config = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
}

func TestSimulateConversationRoundTripAndAlternates(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := conversationstenography.GenerativeConfig{Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1}
	alice, err := conversationstenography.NewConversationChain(simulationTestModel{}, key, "simulation", cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice.SetCapacityOptions(600, 8, 0)
	bob, err := conversationstenography.NewConversationChain(simulationTestModel{}, key, "simulation", cfg)
	if err != nil {
		t.Fatal(err)
	}
	bob.SetCapacityOptions(600, 8, 0)
	var out, errOut bytes.Buffer
	input := strings.NewReader("hello bob\nhello alice\n/show\n/quit\n")
	if err := simulateConversation(context.Background(), input, &out, &errOut, alice, bob, "Alice", "Bob", false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Bob decoded: hello bob",
		"Alice decoded: hello alice",
		"Alice → Bob: hello bob",
		"Bob → Alice: hello alice",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", errOut.String())
	}
}

func TestSimulationRejectsSameUser(t *testing.T) {
	err := run([]string{"simulate", "-user-a", "Alex", "-user-b", "Alex"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "must be different") {
		t.Fatalf("expected distinct-user error, got %v", err)
	}
}

func TestResolveCapacityTopN(t *testing.T) {
	if got := resolveCapacityTopN(0, 256); got != 256 {
		t.Fatalf("inherit denser base: got %d", got)
	}
	if got := resolveCapacityTopN(0, 8); got != 32 {
		t.Fatalf("default floor: got %d", got)
	}
	if got := resolveCapacityTopN(64, 8); got != 64 {
		t.Fatalf("explicit wins: got %d", got)
	}
}

func TestSimulateRejectsSecretAndDevSecret(t *testing.T) {
	err := run([]string{"simulate", "-dev-secret", "-secret", "x", "-user-a", "A", "-user-b", "B"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "either -dev-secret or -secret") {
		t.Fatalf("expected flag conflict, got %v", err)
	}
}


func TestSimulateManualDoesNotAutoDecode(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := conversationstenography.GenerativeConfig{Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1}
	alice, err := conversationstenography.NewConversationChain(simulationTestModel{}, key, "simulation-manual", cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice.SetCapacityOptions(600, 8, 0)
	bob, err := conversationstenography.NewConversationChain(simulationTestModel{}, key, "simulation-manual", cfg)
	if err != nil {
		t.Fatal(err)
	}
	bob.SetCapacityOptions(600, 8, 0)
	var out, errOut bytes.Buffer
	input := strings.NewReader("hello bob\n/quit\n")
	if err := simulateConversation(context.Background(), input, &out, &errOut, alice, bob, "Alice", "Bob", true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "decoded:") {
		t.Fatalf("manual mode auto-decoded:\n%s", text)
	}
	if !strings.Contains(text, "/paste") && !strings.Contains(text, "paste>") {
		t.Fatalf("missing paste prompt:\n%s", text)
	}
}
