package decalgo

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

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
	if budget.PlaintextBytes != len(message) || budget.AuthenticationBytes != 16 || budget.FrameLengthBytes != 1 || budget.TerminationBytes != 4 {
		t.Fatalf("unexpected budget: %#v", budget)
	}
	if budget.TotalHiddenBytes != budget.PackedBytes+budget.AuthenticationBytes+budget.FrameLengthBytes+budget.TerminationBytes {
		t.Fatalf("budget does not add up: %#v", budget)
	}
	if budget.TotalHiddenBytes >= len(message) {
		t.Fatalf("common message did not shrink after all overhead: %#v", budget)
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

	first, err := sam.Send(ctx, "samir", []byte("hi alex"))
	if err != nil {
		t.Fatal(err)
	}
	got, accepted, err := alex.Receive(ctx, "samir", first.Encrypted)
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

	third, err := sam.Send(ctx, "samir", []byte("yes, the maths homework"))
	if err != nil {
		t.Fatal(err)
	}
	got, accepted, err = alex.Receive(ctx, "samir", third.Encrypted)
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
	first, _ := sender.Send(ctx, "samir", []byte("first"))
	second, _ := sender.Send(ctx, "samir", []byte("second"))

	wrongOrder := newTestChain(t, "friends")
	if _, _, err := wrongOrder.Receive(ctx, "samir", second.Encrypted); err == nil {
		t.Fatal("out-of-order carrier accepted")
	}
	wrongSender := newTestChain(t, "friends")
	if _, _, err := wrongSender.Receive(ctx, "alex", first.Encrypted); err == nil {
		t.Fatal("wrong sender accepted")
	}
	wrongConversation := newTestChain(t, "other")
	if _, _, err := wrongConversation.Receive(ctx, "samir", first.Encrypted); err == nil {
		t.Fatal("wrong conversation accepted")
	}
}

func TestConversationChainRestorePublicState(t *testing.T) {
	ctx := context.Background()
	original := newTestChain(t, "friends")
	first, _ := original.Send(ctx, "samir", []byte("first"))
	restored := newTestChain(t, "friends")
	if err := restored.RestorePublic([]ChainRecord{first}); err != nil {
		t.Fatal(err)
	}
	second, err := restored.Send(ctx, "samir", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Index != 1 || second.SenderSequence != 1 {
		t.Fatalf("bad restored counters: %#v", second)
	}
}
