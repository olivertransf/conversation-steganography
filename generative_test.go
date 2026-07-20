package conversationstenography

import (
	"bytes"
	"context"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

type fakeModel struct{ fingerprint string }

func (m fakeModel) Fingerprint() string { return m.fingerprint }
func (m fakeModel) Tokenize(_ context.Context, text string) ([]int, error) {
	ids := make([]int, 0, len(text))
	for _, r := range text {
		if r >= 'a' && r <= 'h' {
			ids = append(ids, int(r-'a'))
			continue
		}
		ids = append(ids, 1000+int(r))
	}
	return ids, nil
}

type lengthModel struct{ fakeModel }

func (m lengthModel) Next(_ context.Context, tokens []int, n int) ([]TokenCandidate, error) {
	out := make([]TokenCandidate, n)
	for i := range out {
		out[i] = TokenCandidate{ID: (len(tokens) + i) % n, LogProb: float64(n - i), Text: strings.Repeat("x", i+1)}
	}
	return out, nil
}
func (m fakeModel) Detokenize(_ context.Context, ids []int) (string, error) {
	r := make([]rune, len(ids))
	for i, id := range ids {
		r[i] = rune('a' + id)
	}
	return string(r), nil
}
func (m fakeModel) Next(_ context.Context, tokens []int, n int) ([]TokenCandidate, error) {
	out := make([]TokenCandidate, n)
	for i := range out {
		out[i] = TokenCandidate{ID: (len(tokens) + i) % n, LogProb: float64(n - i)}
	}
	return out, nil
}

func TestGenerativeRoundTrip(t *testing.T) {
	ctx := context.Background()
	codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{Prompt: "P", TopN: 8})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 1, 2, 127, 128, 254, 255}
	text, err := codec.Encode(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ctx, text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGenerativeEmptyRoundTrip(t *testing.T) {
	ctx := context.Background()
	codec, _ := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{Prompt: "P", TopN: 4})
	text, err := codec.Encode(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ctx, text)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestHuffmanGenerativeRoundTrip(t *testing.T) {
	ctx := context.Background()
	codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{Prompt: "P", TopN: 8, Coding: "huffman", Temperature: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("probability weighted")
	text, err := codec.Encode(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ctx, text)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestArithmeticGenerativeRoundTrip(t *testing.T) {
	ctx := context.Background()
	codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 0.7, FinishTokens: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 1, 2, 3, 127, 128, 254, 255}
	text, err := codec.Encode(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ctx, text)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestCarrierMetricsAreCollectedDuringEncoding(t *testing.T) {
	ctx := context.Background()
	codec, err := NewGenerativeCodec(lengthModel{fakeModel{"fixture-v1"}}, GenerativeConfig{
		Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 0.7, FinishTokens: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, metrics, err := codec.EncodeWithMetrics(ctx, []byte("metrics"))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.DataTokens == 0 || metrics.VisibleCharacters != len([]rune(text)) {
		t.Fatalf("incomplete metrics for %q: %#v", text, metrics)
	}
	if metrics.MeanLogitRegret < 0 || metrics.WorstLogitRegret < metrics.MeanLogitRegret {
		t.Fatalf("invalid regret metrics: %#v", metrics)
	}
}

func TestLengthBiasedArithmeticRoundTrip(t *testing.T) {
	ctx := context.Background()
	codec, err := NewGenerativeCodec(lengthModel{fakeModel{"fixture-v1"}}, GenerativeConfig{
		Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1, LengthBias: 0.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("length-aware arithmetic")
	text, err := codec.Encode(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ctx, text)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestArithmeticExactTerminationRandomized(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))
	for _, topN := range []int{3, 7, 8} {
		codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{
			Prompt: "P", TopN: topN, Coding: "arithmetic", Temperature: 0.7,
		})
		if err != nil {
			t.Fatal(err)
		}
		for size := 0; size <= 64; size++ {
			want := make([]byte, size)
			if _, err := rng.Read(want); err != nil {
				t.Fatal(err)
			}
			text, err := codec.Encode(ctx, want)
			if err != nil {
				t.Fatalf("top-n %d size %d encode: %v", topN, size, err)
			}
			got, err := codec.Decode(ctx, text)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("top-n %d size %d: got %x, %v; want %x", topN, size, got, err, want)
			}
		}
	}
}

func TestUnframedArithmeticBoundariesRandomized(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(3))
	for _, topN := range []int{3, 7, 8} {
		codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{
			Prompt: "P", TopN: topN, Coding: "arithmetic", Temperature: 0.7, FinishTokens: 3,
		})
		if err != nil {
			t.Fatal(err)
		}
		for size := 0; size <= 64; size++ {
			want := make([]byte, size)
			if _, err := rng.Read(want); err != nil {
				t.Fatal(err)
			}
			text, _, err := codec.EncodeUnframedWithMetrics(ctx, want)
			if err != nil {
				t.Fatalf("top-n %d size %d encode: %v", topN, size, err)
			}
			candidates, err := codec.DecodeUnframedCandidates(ctx, text)
			if err != nil {
				t.Fatalf("top-n %d size %d decode: %v", topN, size, err)
			}
			found := false
			for _, candidate := range candidates {
				if bytes.Equal(candidate, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("top-n %d size %d missing from %d boundary candidates", topN, size, len(candidates))
			}
		}
	}
}

func TestGenerativeRejectsInvalidConfiguration(t *testing.T) {
	model := fakeModel{"fixture-v1"}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 7, Coding: "uniform"}); err == nil {
		t.Fatal("non-power-of-two top-n accepted")
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 7, Coding: "arithmetic"}); err != nil {
		t.Fatalf("arithmetic coding unnecessarily rejected top-n 7: %v", err)
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 8, ModelFingerprint: "other"}); err == nil {
		t.Fatal("wrong model fingerprint accepted")
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 8, CarrierTrials: 33}); err == nil {
		t.Fatal("excessive carrier trial count accepted")
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 8, NaturalnessSlack: -0.1}); err == nil {
		t.Fatal("negative naturalness slack accepted")
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 8, SemanticThreshold: -11}); err == nil {
		t.Fatal("invalid semantic threshold accepted")
	}
	if _, err := NewGenerativeCodec(model, GenerativeConfig{Prompt: "P", TopN: 8, LengthBias: 1.1}); err == nil {
		t.Fatal("invalid length bias accepted")
	}
}

func TestGenerativeRejectsTruncation(t *testing.T) {
	ctx := context.Background()
	codec, _ := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{Prompt: "P", TopN: 8})
	text, _ := codec.Encode(ctx, []byte("payload"))
	if _, err := codec.Decode(ctx, text[:len(text)-1]); err == nil {
		t.Fatal("truncated generated text accepted")
	}
}

func TestOrdinaryVisibleTokenFilter(t *testing.T) {
	allowed := []string{" hello", "it's", " sunny", ",", "?"}
	for _, token := range allowed {
		if !ordinaryVisibleToken(token) {
			t.Errorf("ordinary token rejected: %q", token)
		}
	}
	rejected := []string{" 09:42", "\n", " message", " participants", "(sent", "#", " assistant", "example", "\"hello\"", " ~", "&", " - ", "..."}
	for _, token := range rejected {
		if ordinaryVisibleToken(token) {
			t.Errorf("metadata token accepted: %q", token)
		}
	}
}

type styleModel struct{ fakeModel }

func (m styleModel) Next(_ context.Context, _ []int, n int) ([]TokenCandidate, error) {
	out := make([]TokenCandidate, n)
	for i := range out {
		text := " normal"
		if i < n/2 {
			text = " message"
		}
		out[i] = TokenCandidate{ID: i, LogProb: float64(n - i), Text: text}
	}
	return out, nil
}

func TestStrictStyleFiltersBeforeCoding(t *testing.T) {
	codec, err := NewGenerativeCodec(styleModel{fakeModel{"style-v1"}}, GenerativeConfig{Prompt: "P", TopN: 8, StrictStyle: true, CandidatePool: 2})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := codec.candidates(context.Background(), []int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 8 {
		t.Fatalf("got %d candidates", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.Text != " normal" {
			t.Fatalf("banned candidate survived: %#v", candidate)
		}
	}
}

func TestCandidatePoolAllowsCapacityProfile(t *testing.T) {
	codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{
		Prompt: "P", TopN: 32, Coding: "arithmetic", CandidatePool: 32, Temperature: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if codec.Config().CandidatePool != 32 {
		t.Fatalf("got pool %d", codec.Config().CandidatePool)
	}
}

func TestEncodeReportsProgress(t *testing.T) {
	ctx := context.Background()
	var lastDone, lastTotal, calls int
	codec, err := NewGenerativeCodec(fakeModel{"fixture-v1"}, GenerativeConfig{
		Prompt: "P", TopN: 8, Coding: "arithmetic", Temperature: 1,
		ProgressCallback: func(done, total int) {
			calls++
			lastDone, lastTotal = done, total
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := codec.EncodeUnframedWithMetrics(ctx, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || lastTotal <= 0 || lastDone < lastTotal {
		t.Fatalf("progress not completed: calls=%d done=%d total=%d", calls, lastDone, lastTotal)
	}
}
