package conversationstenography

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type plainStyleModel struct{ fakeModel }

func (m plainStyleModel) Next(_ context.Context, _ []int, n int) ([]TokenCandidate, error) {
	out := make([]TokenCandidate, n)
	for i := range out {
		out[i] = TokenCandidate{ID: i, LogProb: float64(n - i), Text: " normal"}
	}
	return out, nil
}

type judgeModel struct {
	fakeModel
	approve bool
}

func (m judgeModel) Next(_ context.Context, _ []int, n int) ([]TokenCandidate, error) {
	out := make([]TokenCandidate, n)
	for i := range out {
		out[i] = TokenCandidate{ID: i, LogProb: float64(-i), Text: " other"}
	}
	yes, no := 2.0, 1.0
	if !m.approve {
		yes, no = 1, 2
	}
	out[0] = TokenCandidate{ID: 0, LogProb: yes, Text: " YES"}
	out[1] = TokenCandidate{ID: 1, LogProb: no, Text: " NO"}
	return out, nil
}

func TestModelPromptDoesNotExposeSenderName(t *testing.T) {
	chain := newTestChain(t, "friends")
	chain.baseConfig.ChainSystem = "Write a casual reply."
	chain.records = []ChainRecord{{From: "private-sender-name", Encrypted: "ordinary earlier message"}}
	prompt := chain.messageConfig("another-private-name").Prompt
	if strings.Contains(prompt, "private-sender-name") || strings.Contains(prompt, "another-private-name") {
		t.Fatalf("sender identity leaked into model prompt: %q", prompt)
	}
}

func TestEncodingBudgetContainsOnlyRequiredFields(t *testing.T) {
	chain := newTestChain(t, "budget")
	chain.baseConfig.Coding = "arithmetic"
	message := []byte("i think i might've fucked up a bit too hard")
	budget, err := chain.EncodingBudget(message)
	if err != nil {
		t.Fatal(err)
	}
	if budget.PlaintextBytes != len(message) || budget.AuthenticationBytes != 16 || budget.FrameLengthBytes != 0 || budget.TerminationBytes != 0 {
		t.Fatalf("unexpected budget: %#v", budget)
	}
	if budget.TotalHiddenBytes != budget.PackedBytes+budget.AuthenticationBytes+budget.FrameLengthBytes+budget.TerminationBytes {
		t.Fatalf("budget does not add up: %#v", budget)
	}
	if budget.TotalHiddenBytes >= len(message) {
		t.Fatalf("common message did not shrink after all overhead: %#v", budget)
	}
}

func TestShortChatEncodingBudget(t *testing.T) {
	chain := newTestChain(t, "compact-budget")
	chain.baseConfig.Coding = "arithmetic"
	chain.baseConfig.CarrierTrials = 8
	budget, err := chain.EncodingBudget([]byte("meet me after lunch"))
	if err != nil {
		t.Fatal(err)
	}
	if budget.PackedBytes != 0 || budget.AuthenticationBytes != sivTagSize || budget.FrameLengthBytes != 0 || budget.TerminationBytes != 0 || budget.TotalHiddenBytes != sivTagSize {
		t.Fatalf("unexpected compact budget: %#v", budget)
	}
	if budget.BaselineAuthenticationHypotheses != 8*(len(packingModes())+1)+1 || budget.BaselineAuthenticationBits < 118 {
		t.Fatalf("phrase authentication fell below its bound: %#v", budget)
	}
}

func TestDenseConnectiveEncodingBudget(t *testing.T) {
	chain := newTestChain(t, "dense-budget")
	chain.baseConfig.Coding = "arithmetic"
	message := []byte("i that it is in the and it is for me")
	budget, err := chain.EncodingBudget(message)
	if err != nil {
		t.Fatal(err)
	}
	if budget.PackedBytes != 7 || budget.AuthenticationBytes != 16 || budget.FrameLengthBytes != 0 || budget.TotalHiddenBytes != 23 {
		t.Fatalf("unexpected dense budget: %#v", budget)
	}
}

func newTestChain(t *testing.T, conversation string) *ConversationChain {
	t.Helper()
	chain, err := NewConversationChain(fakeModel{"fixture-v1"}, []byte("0123456789abcdef0123456789abcdef"), conversation,
		GenerativeConfig{Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1})
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

func TestMultiPartyConversationChain(t *testing.T) {
	ctx := context.Background()
	alex := newTestChain(t, "friends")
	sam := newTestChain(t, "friends")

	first, err := sam.Send(ctx, "bob", []byte("hi alex"))
	if err != nil {
		t.Fatal(err)
	}
	got, accepted, err := alex.Receive(ctx, "bob", first.Encrypted)
	if err != nil || string(got) != "hi alex" || accepted.SenderSequence != 0 {
		t.Fatalf("first: %q %#v %v", got, accepted, err)
	}

	reply, err := alex.Send(ctx, "alex", []byte("did you do your homework?"))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err = sam.Receive(ctx, "alex", reply.Encrypted)
	if err != nil || string(got) != "did you do your homework?" {
		t.Fatalf("reply: %q %v", got, err)
	}

	third, err := sam.Send(ctx, "bob", []byte("yes, the maths homework"))
	if err != nil {
		t.Fatal(err)
	}
	got, accepted, err = alex.Receive(ctx, "bob", third.Encrypted)
	if err != nil || string(got) != "yes, the maths homework" || accepted.SenderSequence != 1 || accepted.Index != 2 {
		t.Fatalf("third: %q %#v %v", got, accepted, err)
	}
	if !reflect.DeepEqual(alex.Records(), sam.Records()) {
		t.Fatal("peer public chains diverged")
	}
	karan := newTestChain(t, "friends")
	if err := karan.RestorePublic(alex.Records()); err != nil {
		t.Fatal(err)
	}
	fourth, err := karan.Send(ctx, "karan", []byte("can I join?"))
	if err != nil {
		t.Fatal(err)
	}
	got, accepted, err = alex.Receive(ctx, "karan", fourth.Encrypted)
	if err != nil || string(got) != "can I join?" || accepted.SenderSequence != 0 || accepted.Index != 3 {
		t.Fatalf("new participant: %q %#v %v", got, accepted, err)
	}
}

func TestConversationChainRejectsWrongOrderAndSender(t *testing.T) {
	ctx := context.Background()
	sender := newTestChain(t, "friends")
	first, _ := sender.Send(ctx, "bob", []byte("first"))
	second, _ := sender.Send(ctx, "bob", []byte("second"))

	wrongOrder := newTestChain(t, "friends")
	if _, _, err := wrongOrder.Receive(ctx, "bob", second.Encrypted); err == nil {
		t.Fatal("out-of-order carrier accepted")
	}
	wrongSender := newTestChain(t, "friends")
	if _, _, err := wrongSender.Receive(ctx, "alex", first.Encrypted); err == nil {
		t.Fatal("wrong sender accepted")
	}
	wrongConversation := newTestChain(t, "other")
	if _, _, err := wrongConversation.Receive(ctx, "bob", first.Encrypted); err == nil {
		t.Fatal("wrong conversation accepted")
	}
}

func TestConversationChainRestorePublicState(t *testing.T) {
	ctx := context.Background()
	original := newTestChain(t, "friends")
	first, _ := original.Send(ctx, "bob", []byte("first"))
	restored := newTestChain(t, "friends")
	if err := restored.RestorePublic([]ChainRecord{first}); err != nil {
		t.Fatal(err)
	}
	second, err := restored.Send(ctx, "bob", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Index != 1 || second.SenderSequence != 1 {
		t.Fatalf("bad restored counters: %#v", second)
	}
}

func TestConversationCompressionDictionaryRestoresExactly(t *testing.T) {
	original := newTestChain(t, "dictionary-restore")
	original.records = []ChainRecord{
		{Index: 0, From: "bob", Encrypted: "We should meet beside the northern greenhouse at Riverside Botanical Garden."},
		{Index: 1, From: "alex", Encrypted: "The northern greenhouse works for me."},
	}
	restored := newTestChain(t, "dictionary-restore")
	if err := restored.RestorePublic(original.records); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.compressionDictionary(), original.compressionDictionary()) {
		t.Fatal("restored conversation derived a different compression dictionary")
	}
	if len(restored.compressionDictionary()) > maxConversationDictionary {
		t.Fatal("conversation dictionary exceeded the DEFLATE window")
	}
}

func TestImplicitCarrierTrialAddsNoPayloadBytes(t *testing.T) {
	chain := newTestChain(t, "trials")
	plaintext := []byte("same message")
	first, err := chain.sealTrial("bob", 0, 0, 0, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := chain.sealTrial("bob", 0, 0, 3, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(fourth) || reflect.DeepEqual(first, fourth) {
		t.Fatalf("trial must change ciphertext without changing size: %d/%d equal=%v", len(first), len(fourth), reflect.DeepEqual(first, fourth))
	}
	if _, err := chain.openTrials("bob", 0, 0, fourth, 3); err == nil {
		t.Fatal("opened a trial outside the synchronized search range")
	}
	got, err := chain.openTrials("bob", 0, 0, fourth, 4)
	if err != nil || !reflect.DeepEqual(got, plaintext) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAuthenticationHypothesisSecurityBound(t *testing.T) {
	hypotheses, err := authenticationHypotheses(1, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := 8*(len(packingModes())+1) + 1
	if hypotheses != want {
		t.Fatalf("got %d hypotheses; want %d", hypotheses, want)
	}
	bits := effectiveAuthenticationBits(hypotheses)
	if bits < 118 || bits >= 128 {
		t.Fatalf("unexpected effective authentication strength %.3f bits", bits)
	}
	maximumCandidates := maxAuthenticationHypotheses / hypotheses
	if _, err := authenticationHypotheses(maximumCandidates, 8); err != nil {
		t.Fatalf("bounded search rejected: %v", err)
	}
	if _, err := authenticationHypotheses(maximumCandidates+1, 8); err == nil {
		t.Fatal("oversized authentication search accepted")
	}
	if floor := effectiveAuthenticationBits(maxAuthenticationHypotheses + hypotheses); floor < 112 {
		t.Fatalf("configured search floor fell below 112 bits: %.3f", floor)
	}
}

func TestPackingModeIsAuthenticatedWithoutPayloadByte(t *testing.T) {
	chain := newTestChain(t, "detached-mode")
	plaintext := []byte("meet me after lunch")
	mode, body, err := packMessageDetached(plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := chain.sealTrial("bob", 0, 0, 0, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 || len(sealed) != sivTagSize {
		t.Fatalf("mode consumed payload space: mode=%d body=%d sealed=%d", mode, len(body), len(sealed))
	}
	wrongMode := mode + 1
	if _, err := openSIV(chain.key, chain.trialModeAAD("bob", 0, 0, 0, wrongMode), sealed); err == nil {
		t.Fatal("detached packing mode was not authenticated")
	}
	got, err := chain.openTrials("bob", 0, 0, sealed, 1)
	if err != nil || !reflect.DeepEqual(got, plaintext) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestInlinePackingModeSIVCompatibility(t *testing.T) {
	chain := newTestChain(t, "inline-mode-compatibility")
	plaintext := []byte("older packed record")
	packed, err := packMessage(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSIV(chain.key, chain.trialAAD("bob", 0, 0, 0), packed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := chain.openTrials("bob", 0, 0, sealed, 1)
	if err != nil || !reflect.DeepEqual(got, plaintext) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestFramedGenerativeCarrierCompatibility(t *testing.T) {
	ctx := context.Background()
	sender := newTestChain(t, "framed-carrier-compatibility")
	receiver := newTestChain(t, "framed-carrier-compatibility")
	plaintext := []byte("legacy framed carrier")
	sealed, err := sender.sealTrial("bob", 0, 0, 0, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewGenerativeCodec(sender.model, sender.messageConfig("bob"))
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := codec.Encode(ctx, sealed)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := receiver.Receive(ctx, "bob", carrier)
	if err != nil || !reflect.DeepEqual(got, plaintext) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSendSelectsShortestCarrierTrial(t *testing.T) {
	ctx := context.Background()
	chain := newTestChain(t, "shortest-trial")
	chain.baseConfig.CarrierTrials = 4
	plaintext := []byte("choose the shortest")
	codec, err := NewGenerativeCodec(chain.model, chain.messageConfig("bob"))
	if err != nil {
		t.Fatal(err)
	}
	shortest := 0
	for trial := 0; trial < 4; trial++ {
		sealed, err := chain.sealTrial("bob", 0, 0, trial, plaintext)
		if err != nil {
			t.Fatal(err)
		}
		carrier, _, err := codec.EncodeUnframedWithMetrics(ctx, sealed)
		if err != nil {
			t.Fatal(err)
		}
		length := len([]rune(carrier))
		if shortest == 0 || length < shortest {
			shortest = length
		}
	}
	record, err := chain.Send(ctx, "bob", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(record.Encrypted)); got != shortest {
		t.Fatalf("selected carrier length %d; shortest trial was %d", got, shortest)
	}
}

func TestStrictChainRefusesNonHumanFallback(t *testing.T) {
	chain := newTestChain(t, "strict-carrier")
	chain.model = plainStyleModel{fakeModel{"fixture-v1"}}
	chain.baseConfig.StrictStyle = true
	chain.baseConfig.CandidatePool = 1
	chain.baseConfig.CarrierTrials = 2
	if _, err := chain.Send(context.Background(), "bob", []byte("hello")); err == nil || !strings.Contains(err.Error(), "human-writing checks") {
		t.Fatalf("strict chain accepted fake non-prose carrier: %v", err)
	}
}

func TestNaturalnessConstrainedShortestSelection(t *testing.T) {
	options := []carrierOption{
		{text: "too-short", human: true, metrics: CarrierMetrics{VisibleCharacters: 9, MeanLogitRegret: 1}},
		{text: "most natural carrier", human: true, metrics: CarrierMetrics{VisibleCharacters: 20, MeanLogitRegret: 0.2}},
		{text: "balanced choice", human: true, metrics: CarrierMetrics{VisibleCharacters: 15, MeanLogitRegret: 0.4}},
		{text: "bad", human: false, metrics: CarrierMetrics{VisibleCharacters: 3, MeanLogitRegret: 0}},
	}
	if got := selectCarrier(options, true, 0.35); got != "balanced choice" {
		t.Fatalf("selected %q; want shortest carrier inside naturalness band", got)
	}
	if got := selectCarrier(options, false, 0.35); got != "bad" {
		t.Fatalf("non-strict selection got %q; want absolute shortest", got)
	}
}

func TestSemanticMarginInfluencesQualityBand(t *testing.T) {
	options := []carrierOption{
		{text: "short odd", human: true, semanticMargin: -5, metrics: CarrierMetrics{VisibleCharacters: 9, MeanLogitRegret: 0.2}},
		{text: "longer coherent text", human: true, semanticMargin: 2, metrics: CarrierMetrics{VisibleCharacters: 20, MeanLogitRegret: 0.2}},
	}
	if got := selectCarrier(options, true, 0.35); got != "longer coherent text" {
		t.Fatalf("semantic ranking selected %q", got)
	}
}

func TestHumanWrittenCarrierGate(t *testing.T) {
	accepted := []string{
		"I finally watched it last night, and the ending genuinely surprised me.",
		"That sounds pretty good. What did you think?",
	}
	for _, text := range accepted {
		if !humanWrittenCarrier(text) {
			t.Errorf("natural carrier rejected: %q", text)
		}
	}
	rejected := []string{
		"unfinished thought",
		"What? Why? How? Where? When?",
		"This is is obviously broken.",
		"one two three four one two three four.",
		"line one.\nline two.",
	}
	for _, text := range rejected {
		if humanWrittenCarrier(text) {
			t.Errorf("generation failure accepted: %q", text)
		}
	}
}

func TestSemanticHumanWritingJudge(t *testing.T) {
	ctx := context.Background()
	approved, margin, err := semanticHumanWritten(ctx, judgeModel{fakeModel{"judge"}, true}, "I finally made it home, and the rain was ridiculous.")
	if err != nil || !approved || margin <= 0 {
		t.Fatalf("approved prose rejected: approved=%v margin=%f err=%v", approved, margin, err)
	}
	approved, margin, err = semanticHumanWritten(ctx, judgeModel{fakeModel{"judge"}, false}, "One thought. Different topic. Why? Metadata output.")
	if err != nil || approved || margin >= 0 {
		t.Fatalf("bad prose approved: approved=%v margin=%f err=%v", approved, margin, err)
	}
	if _, _, err := semanticHumanWritten(ctx, fakeModel{"missing-answers"}, "ordinary text"); err == nil {
		t.Fatal("judge accepted a model without explicit YES and NO scores")
	}
}

func TestCapacityConfigAndPieceEstimate(t *testing.T) {
	chain := newTestChain(t, "capacity")
	chain.baseConfig.StrictStyle = true
	chain.SetCapacityOptions(600, 32, 0)
	cfg := chain.capacityConfig()
	if cfg.Coding != "arithmetic" || cfg.TopN != 32 || cfg.LengthBias != 0 || cfg.CandidatePool != 32 {
		t.Fatalf("unexpected capacity config: %#v", cfg)
	}
	chain.SetCapacityOptions(600, 256, 0)
	cfg = chain.capacityConfig()
	if cfg.TopN != 256 || cfg.CandidatePool != 64 {
		t.Fatalf("candidate pool should clamp at 64 for large top_n: %#v", cfg)
	}
	piece := estimateMaxPieceBytes(600, 32)
	if piece < 1 {
		t.Fatalf("piece size %d", piece)
	}
	smaller := estimateMaxPieceBytes(100, 32)
	if smaller >= piece {
		t.Fatalf("smaller cover budget should yield smaller pieces: %d vs %d", smaller, piece)
	}
}

type countingNextModel struct {
	fakeModel
	calls int
	limit int // fail when calls > limit; 0 means unlimited
}

func (m *countingNextModel) Next(ctx context.Context, tokens []int, n int) ([]TokenCandidate, error) {
	m.calls++
	if m.limit > 0 && m.calls > m.limit {
		return nil, errors.New("forced encode failure")
	}
	return m.fakeModel.Next(ctx, tokens, n)
}

func newMessageTestChain(t *testing.T, conversation string, model LanguageModel) *ConversationChain {
	t.Helper()
	if model == nil {
		model = fakeModel{"fixture-v1"}
	}
	chain, err := NewConversationChain(model, []byte("0123456789abcdef0123456789abcdef"), conversation,
		GenerativeConfig{Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1})
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

func TestSendMessageSingleChunk(t *testing.T) {
	ctx := context.Background()
	alice := newMessageTestChain(t, "friends", nil)
	bob := newMessageTestChain(t, "friends", nil)
	alice.SetCapacityOptions(600, 8, 0)
	bob.SetCapacityOptions(600, 8, 0)

	records, err := alice.SendMessage(ctx, "alice", []byte("short hi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 1 {
		t.Fatal("expected at least one cover")
	}
	var plaintext []byte
	var done bool
	var status ReceiveStatus
	for i, record := range records {
		plaintext, done, status, err = bob.ReceiveMessage(ctx, "alice", record.Encrypted)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	if !done || string(plaintext) != "short hi" {
		t.Fatalf("got %q done=%v status=%#v", plaintext, done, status)
	}
}

func TestSendMessageMultiChunkRoundTrip(t *testing.T) {
	ctx := context.Background()
	alice := newMessageTestChain(t, "friends", nil)
	bob := newMessageTestChain(t, "friends", nil)
	// Tiny cover budget forces multiple pieces for ~2KiB plaintext.
	alice.SetCapacityOptions(80, 8, 0)
	bob.SetCapacityOptions(80, 8, 0)

	plaintext := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 50)
	records, err := alice.SendMessage(ctx, "alice", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 {
		t.Fatalf("expected multiple covers, got %d", len(records))
	}

	var got []byte
	var done bool
	for i, record := range records {
		var status ReceiveStatus
		got, done, status, err = bob.ReceiveMessage(ctx, "alice", record.Encrypted)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if i < len(records)-1 {
			if done || !status.Waiting {
				t.Fatalf("part %d: expected waiting, done=%v status=%#v", i, done, status)
			}
			continue
		}
		if !done || !bytes.Equal(got, plaintext) {
			t.Fatalf("final: done=%v len(got)=%d want=%d", done, len(got), len(plaintext))
		}
	}
}

func TestReceiveMessageRejectsSkippedCover(t *testing.T) {
	ctx := context.Background()
	alice := newMessageTestChain(t, "friends", nil)
	bob := newMessageTestChain(t, "friends", nil)
	alice.SetCapacityOptions(80, 8, 0)
	bob.SetCapacityOptions(80, 8, 0)
	plaintext := bytes.Repeat([]byte("abcdefghij"), 80)
	records, err := alice.SendMessage(ctx, "alice", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 {
		t.Fatalf("need >=2 covers, got %d", len(records))
	}
	if _, _, _, err := bob.ReceiveMessage(ctx, "alice", records[0].Encrypted); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := bob.ReceiveMessage(ctx, "alice", records[2%len(records)].Encrypted); err == nil && len(records) > 2 {
		// If only 2 covers, skipping to index 0 is duplicate; index 1 is correct next.
	}
	if len(records) > 2 {
		if _, done, _, err := bob.ReceiveMessage(ctx, "alice", records[2].Encrypted); err == nil || done {
			t.Fatal("skipped cover should error without plaintext")
		}
	} else {
		// With exactly 2 covers, feeding cover 0 again is a duplicate/wrong part.
		if _, done, _, err := bob.ReceiveMessage(ctx, "alice", records[0].Encrypted); err == nil || done {
			t.Fatal("duplicate/skipped cover should error")
		}
	}
}

func TestSendMessageAllOrNothing(t *testing.T) {
	ctx := context.Background()
	probe := &countingNextModel{fakeModel: fakeModel{"fixture-v1"}}
	alice := newMessageTestChain(t, "friends", probe)
	alice.SetCapacityOptions(80, 8, 0)
	plaintext := bytes.Repeat([]byte("abcdefghij"), 80)
	records, err := alice.SendMessage(ctx, "alice", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 {
		t.Fatalf("need >=2 covers to test last-chunk failure, got %d", len(records))
	}
	needed := probe.calls

	failing := &countingNextModel{fakeModel: fakeModel{"fixture-v1"}, limit: needed / 2}
	if failing.limit < 1 {
		failing.limit = 1
	}
	alice2 := newMessageTestChain(t, "friends", failing)
	alice2.SetCapacityOptions(80, 8, 0)
	if _, err := alice2.SendMessage(ctx, "alice", plaintext); err == nil {
		t.Fatal("expected encode failure")
	}
	if len(alice2.Records()) != 0 {
		t.Fatalf("all-or-nothing violated: committed %d records", len(alice2.Records()))
	}
}

func TestPendingAssemblyRestore(t *testing.T) {
	ctx := context.Background()
	alice := newMessageTestChain(t, "friends", nil)
	bob := newMessageTestChain(t, "friends", nil)
	alice.SetCapacityOptions(80, 8, 0)
	bob.SetCapacityOptions(80, 8, 0)
	plaintext := bytes.Repeat([]byte("pending restore "), 40)
	records, err := alice.SendMessage(ctx, "alice", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 {
		t.Fatalf("need >=2 covers, got %d", len(records))
	}
	if _, done, _, err := bob.ReceiveMessage(ctx, "alice", records[0].Encrypted); err != nil || done {
		t.Fatalf("first part: done=%v err=%v", done, err)
	}
	exported := bob.ExportPending()
	public := bob.Records()

	bob2 := newMessageTestChain(t, "friends", nil)
	bob2.SetCapacityOptions(80, 8, 0)
	if err := bob2.RestorePublic(public); err != nil {
		t.Fatal(err)
	}
	if err := bob2.RestorePending(exported); err != nil {
		t.Fatal(err)
	}
	var got []byte
	var done bool
	for i := 1; i < len(records); i++ {
		got, done, _, err = bob2.ReceiveMessage(ctx, "alice", records[i].Encrypted)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	if !done || !bytes.Equal(got, plaintext) {
		t.Fatalf("restore failed: done=%v equal=%v", done, bytes.Equal(got, plaintext))
	}
}

func TestReceiveMessageLegacyFallback(t *testing.T) {
	ctx := context.Background()
	alice := newMessageTestChain(t, "friends", nil)
	bob := newMessageTestChain(t, "friends", nil)
	record, err := alice.Send(ctx, "alice", []byte("legacy path"))
	if err != nil {
		t.Fatal(err)
	}
	got, done, _, err := bob.ReceiveMessage(ctx, "alice", record.Encrypted)
	if err != nil || !done || string(got) != "legacy path" {
		t.Fatalf("legacy fallback: %q done=%v err=%v", got, done, err)
	}
}

func TestEncodingBudgetReportsChunks(t *testing.T) {
	chain := newTestChain(t, "budget-chunks")
	chain.SetCapacityOptions(80, 8, 0)
	message := bytes.Repeat([]byte("budget chunk fields "), 40)
	budget, err := chain.EncodingBudget(message)
	if err != nil {
		t.Fatal(err)
	}
	if budget.SealedBytes != budget.PackedBytes+budget.AuthenticationBytes {
		t.Fatalf("sealed bytes: %#v", budget)
	}
	if budget.ChunkCount < 1 || budget.MaxCoverChars != 80 {
		t.Fatalf("chunk fields: %#v", budget)
	}
	if budget.TotalHiddenBytes != budget.PackedBytes+budget.AuthenticationBytes+budget.FrameLengthBytes+budget.TerminationBytes {
		t.Fatalf("budget does not add up: %#v", budget)
	}
}

func TestCapacityProfileDensitySmoke(t *testing.T) {
	plaintext := bytes.Repeat([]byte("density smoke plaintext "), 60)
	low := newTestChain(t, "density-low")
	low.SetCapacityOptions(200, 8, 0.1)
	high := newTestChain(t, "density-high")
	high.SetCapacityOptions(200, 32, 0)
	lowBudget, err := low.EncodingBudget(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	highBudget, err := high.EncodingBudget(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Soft assertion: higher top_n should not require more chunks for the same cover budget.
	if highBudget.ChunkCount > lowBudget.ChunkCount {
		t.Fatalf("capacity profile used more chunks (%d) than sparse profile (%d)", highBudget.ChunkCount, lowBudget.ChunkCount)
	}
	lowPiece := estimateMaxPieceBytes(200, 8)
	highPiece := estimateMaxPieceBytes(200, 32)
	if highPiece < lowPiece {
		t.Fatalf("higher top_n produced smaller pieces: %d < %d", highPiece, lowPiece)
	}
}

func TestContinuationPromptAdvancesThought(t *testing.T) {
	chain := newTestChain(t, "friends")
	chain.baseConfig.ChainSystem = "Write a casual reply."
	open := chain.messageConfig("alice").Prompt
	if !strings.Contains(open, "Topic can be anything ordinary") {
		t.Fatalf("opening prompt missing variety cue: %q", open)
	}
	if strings.Contains(open, "back-to-back follow-up") {
		t.Fatalf("opening prompt should not use continuation cue: %q", open)
	}
	chain.records = []ChainRecord{{From: "alice", Encrypted: "I grabbed coffee on the way in."}}
	cont := chain.messageConfig("alice").Prompt
	if !strings.Contains(cont, "coherent train of thought") || !strings.Contains(cont, "back-to-back follow-up") {
		t.Fatalf("continuation prompt missing cues: %q", cont)
	}
	if strings.Contains(cont, "alice") {
		t.Fatalf("sender name leaked into continuation prompt")
	}
	other := chain.messageConfig("bob").Prompt
	if strings.Contains(other, "coherent train of thought") {
		t.Fatalf("other sender should not get continuation cue: %q", other)
	}
}
