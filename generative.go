package decalgo

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenCandidate is one possible next token and its model score. LogProb may
// contain an unnormalised logit; only the ordering is used by the codec.
type TokenCandidate struct {
	ID      int     `json:"id"`
	LogProb float64 `json:"score"`
	Text    string  `json:"text,omitempty"`
}

// LanguageModel is the deterministic, token-level surface required by the
// generative codec. Sender and receiver must use implementations with the same
// Fingerprint, tokenizer, weights, and candidate filtering rules.
type LanguageModel interface {
	Fingerprint() string
	Tokenize(context.Context, string) ([]int, error)
	Detokenize(context.Context, []int) (string, error)
	Next(context.Context, []int, int) ([]TokenCandidate, error)
}

type copySafeLanguageModel interface {
	NextCopySafe(context.Context, []int, []int, int) ([]TokenCandidate, error)
}

// GenerativeConfig is shared protocol state, not a secret.
type GenerativeConfig struct {
	Prompt            string
	ChainSystem       string
	TopN              int
	Coding            string
	Temperature       float64
	FinishTokens      int
	StrictStyle       bool
	CandidatePool     int
	RefreshSentences  bool
	CarrierTrials     int
	NaturalnessSlack  float64
	SemanticJudge     bool
	SemanticThreshold float64
	LengthBias        float64
	ModelFingerprint  string
	// TokenCallback is called with each generated token's text as it is
	// selected, providing real-time streaming of the carrier generation.
	TokenCallback func(string)
}

// GenerativeCodec embeds framed bytes in deterministic next-token choices.
// It is stateless: each text starts from Config.Prompt.
type GenerativeCodec struct {
	model LanguageModel
	cfg   GenerativeConfig
	bits  int
}

func NewGenerativeCodec(model LanguageModel, cfg GenerativeConfig) (*GenerativeCodec, error) {
	if model == nil {
		return nil, errors.New("language model is required")
	}
	if cfg.Prompt == "" {
		return nil, errors.New("prompt must not be empty")
	}
	if cfg.TopN < 2 {
		return nil, errors.New("top-n must be greater than one")
	}
	if cfg.Coding == "" {
		cfg.Coding = "uniform"
	}
	if cfg.Coding != "uniform" && cfg.Coding != "huffman" && cfg.Coding != "arithmetic" {
		return nil, errors.New("coding must be uniform, huffman, or arithmetic")
	}
	if cfg.Coding == "uniform" && cfg.TopN&(cfg.TopN-1) != 0 {
		return nil, errors.New("top-n must be a power of two for uniform coding")
	}
	if cfg.Coding == "arithmetic" && uint64(cfg.TopN) >= arithmeticTotal {
		return nil, fmt.Errorf("top-n must be below %d for arithmetic coding", arithmeticTotal)
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 1
	}
	if cfg.Temperature < 0.1 || cfg.Temperature > 2 {
		return nil, errors.New("temperature must be between 0.1 and 2")
	}
	if cfg.FinishTokens < 0 || cfg.FinishTokens > 128 {
		return nil, errors.New("finish-tokens must be between 0 and 128")
	}
	if cfg.CandidatePool == 0 {
		cfg.CandidatePool = 4
	}
	if cfg.CandidatePool < 1 || cfg.CandidatePool > 16 {
		return nil, errors.New("candidate-pool must be between 1 and 16")
	}
	if cfg.CarrierTrials == 0 {
		cfg.CarrierTrials = 1
	}
	if cfg.CarrierTrials < 1 || cfg.CarrierTrials > 32 {
		return nil, errors.New("carrier-trials must be between 1 and 32")
	}
	if cfg.NaturalnessSlack == 0 {
		cfg.NaturalnessSlack = 0.35
	}
	if cfg.NaturalnessSlack < 0 || cfg.NaturalnessSlack > 2 {
		return nil, errors.New("naturalness-slack must be between 0 and 2")
	}
	if cfg.SemanticThreshold < -10 || cfg.SemanticThreshold > 10 {
		return nil, errors.New("semantic-threshold must be between -10 and 10")
	}
	if cfg.LengthBias < 0 || cfg.LengthBias > 1 {
		return nil, errors.New("length-bias must be between 0 and 1")
	}
	if cfg.ModelFingerprint == "" {
		cfg.ModelFingerprint = model.Fingerprint()
	}
	if cfg.ModelFingerprint != model.Fingerprint() {
		return nil, fmt.Errorf("model fingerprint mismatch: configured %q, loaded %q", cfg.ModelFingerprint, model.Fingerprint())
	}
	return &GenerativeCodec{model: model, cfg: cfg, bits: bits.TrailingZeros(uint(cfg.TopN))}, nil
}

func (c *GenerativeCodec) Config() GenerativeConfig { return c.cfg }

type CarrierMetrics struct {
	DataTokens        int
	FinishTokens      int
	VisibleCharacters int
	MeanLogitRegret   float64
	WorstLogitRegret  float64
	totalLogitRegret  float64
}

func (m *CarrierMetrics) observe(candidates []TokenCandidate, selected int) {
	if m == nil {
		return
	}
	regret := candidates[0].LogProb - candidates[selected].LogProb
	if regret < 0 {
		regret = 0
	}
	m.DataTokens++
	m.totalLogitRegret += regret
	if regret > m.WorstLogitRegret {
		m.WorstLogitRegret = regret
	}
}

// Encode frames payload with a variable-length byte count and returns generated text.
func (c *GenerativeCodec) Encode(ctx context.Context, payload []byte) (string, error) {
	return c.encode(ctx, payload, nil, true)
}

func (c *GenerativeCodec) EncodeWithMetrics(ctx context.Context, payload []byte) (string, CarrierMetrics, error) {
	metrics := CarrierMetrics{}
	text, err := c.encode(ctx, payload, &metrics, true)
	if metrics.DataTokens > 0 {
		metrics.MeanLogitRegret = metrics.totalLogitRegret / float64(metrics.DataTokens)
	}
	metrics.VisibleCharacters = utf8.RuneCountInString(text)
	return text, metrics, err
}

func (c *GenerativeCodec) EncodeUnframedWithMetrics(ctx context.Context, payload []byte) (string, CarrierMetrics, error) {
	metrics := CarrierMetrics{}
	text, err := c.encode(ctx, payload, &metrics, false)
	if metrics.DataTokens > 0 {
		metrics.MeanLogitRegret = metrics.totalLogitRegret / float64(metrics.DataTokens)
	}
	metrics.VisibleCharacters = utf8.RuneCountInString(text)
	return text, metrics, err
}

func (c *GenerativeCodec) encode(ctx context.Context, payload []byte, metrics *CarrierMetrics, framed bool) (string, error) {
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return "", errors.New("payload is too large")
	}
	data := payload
	if framed {
		data = binary.AppendUvarint(nil, uint64(len(payload)))
		data = append(data, payload...)
	}
	contextTokens, err := c.model.Tokenize(ctx, c.cfg.Prompt)
	if err != nil {
		return "", fmt.Errorf("tokenize prompt: %w", err)
	}
	estimatedTokens := len(data) * 8
	if c.cfg.Coding == "uniform" {
		estimatedTokens = (len(data)*8 + c.bits - 1) / c.bits
	}
	generated := make([]int, 0, estimatedTokens)
	sinceRefresh := 0
	bitOffset := 0
	if c.cfg.Coding == "arithmetic" {
		readOffset := 0
		sourceBit := func(offset int) int {
			if offset < len(data)*8 {
				return readBits(data, offset, 1)
			}
			if (offset-len(data)*8)%2 == 0 {
				return 1
			}
			return 0
		}
		decoder := newArithmeticDecoder(func() int { bit := sourceBit(readOffset); readOffset++; return bit })
		confirmed := 0
		desynchronized := false
		encoder := newArithmeticEncoder(func(bit int) {
			want := sourceBit(confirmed)
			if bit != want {
				desynchronized = true
			}
			confirmed++
		})
		// The frame length makes the stream self-delimiting. Stop as soon as
		// every frame bit has been forced by the chosen token intervals; an
		// additional sentinel would carry no information and lengthen every
		// carrier.
		targetBits := len(data) * 8
		for confirmed < targetBits {
			candidates, err := c.candidates(ctx, contextTokens, generated)
			if err != nil {
				return "", err
			}
			frequencies, err := makeFrequencies(candidates, c.cfg.Temperature, c.cfg.LengthBias)
			if err != nil {
				return "", err
			}
			symbol := decoder.symbol(frequencies)
			encoder.symbol(symbol, frequencies)
			if desynchronized {
				return "", errors.New("arithmetic coder desynchronized")
			}
			metrics.observe(candidates, symbol)
			token := candidates[symbol].ID
			if c.cfg.TokenCallback != nil {
				c.cfg.TokenCallback(candidates[symbol].Text)
			}
			generated = append(generated, token)
			contextTokens = append(contextTokens, token)
			sinceRefresh++
			if confirmed < targetBits && c.cfg.RefreshSentences && sentenceEnded(candidates[symbol].Text, sinceRefresh) {
				contextTokens, err = c.refreshedContext(ctx, generated)
				if err != nil {
					return "", err
				}
				sinceRefresh = 0
			}
			if len(generated) > len(data)*64+1024 {
				return "", fmt.Errorf("arithmetic coder failed to make progress after %d tokens (%d/%d bits confirmed)", len(generated), confirmed, targetBits)
			}
		}
		bitOffset = confirmed
	} else {
		for bitOffset < len(data)*8 {
			candidates, err := c.candidates(ctx, contextTokens, generated)
			if err != nil {
				return "", err
			}
			var token, selected int
			if c.cfg.Coding == "huffman" {
				tree, err := buildHuffman(candidates, c.cfg.Temperature)
				if err != nil {
					return "", err
				}
				leaf := tree
				for leaf.candidate < 0 {
					bit := readBits(data, bitOffset, 1)
					bitOffset++
					if bit == 0 {
						leaf = leaf.left
					} else {
						leaf = leaf.right
					}
				}
				selected = leaf.candidate
				token = candidates[selected].ID
			} else {
				bucket := readBits(data, bitOffset, c.bits)
				bitOffset += c.bits
				selected = bucket
				token = candidates[bucket].ID
			}
			metrics.observe(candidates, selected)
			if c.cfg.TokenCallback != nil {
				c.cfg.TokenCallback(candidates[selected].Text)
			}
			generated = append(generated, token)
			contextTokens = append(contextTokens, token)
		}
	}
	currentText, err := c.model.Detokenize(ctx, generated)
	if err != nil {
		return "", fmt.Errorf("inspect generated ending: %w", err)
	}
	needsFinish := !endsSentence(currentText)
	for i := 0; needsFinish && i < c.cfg.FinishTokens; i++ {
		candidates, err := c.candidates(ctx, contextTokens, generated)
		if err != nil {
			return "", err
		}
		token := candidates[0].ID
		if metrics != nil {
			metrics.FinishTokens++
		}
		if c.cfg.TokenCallback != nil {
			c.cfg.TokenCallback(candidates[0].Text)
		}
		generated = append(generated, token)
		contextTokens = append(contextTokens, token)
		partial, err := c.model.Detokenize(ctx, generated)
		if err != nil {
			return "", fmt.Errorf("detokenize finishing tokens: %w", err)
		}
		trimmed := strings.TrimSpace(partial)
		if i >= 1 && (strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?")) {
			break
		}
	}
	text, err := c.model.Detokenize(ctx, generated)
	if err != nil {
		return "", fmt.Errorf("detokenize generated tokens: %w", err)
	}
	// Text transport is only reversible if the tokenizer recreates the exact IDs.
	roundTrip, err := c.model.Tokenize(ctx, text)
	if err != nil {
		return "", fmt.Errorf("validate generated text: %w", err)
	}
	if !equalInts(generated, roundTrip) {
		position := 0
		for position < len(generated) && position < len(roundTrip) && generated[position] == roundTrip[position] {
			position++
		}
		var generatedID, roundTripID int = -1, -1
		if position < len(generated) {
			generatedID = generated[position]
		}
		if position < len(roundTrip) {
			roundTripID = roundTrip[position]
		}
		return "", fmt.Errorf("tokenizer cannot losslessly represent generated token sequence: mismatch at %d (%d != %d; lengths %d/%d)", position, generatedID, roundTripID, len(generated), len(roundTrip))
	}
	return text, nil
}

// Decode reconstructs the framed payload from a complete generated text.
func (c *GenerativeCodec) Decode(ctx context.Context, text string) ([]byte, error) {
	observed, err := c.model.Tokenize(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("tokenize generated text: %w", err)
	}
	contextTokens, err := c.model.Tokenize(ctx, c.cfg.Prompt)
	if err != nil {
		return nil, fmt.Errorf("tokenize prompt: %w", err)
	}
	if c.cfg.Coding == "arithmetic" {
		return c.decodeArithmetic(ctx, observed, contextTokens)
	}
	decoded := make([]byte, 0, len(observed))
	bitOffset := 0
	dataEnd := len(observed)
	for position, token := range observed {
		candidates, err := c.candidates(ctx, contextTokens, observed[:position])
		if err != nil {
			return nil, err
		}
		bucket := -1
		for i, candidate := range candidates {
			if candidate.ID == token {
				bucket = i
				break
			}
		}
		if bucket < 0 {
			return nil, fmt.Errorf("token %d is outside the top-%d candidate set at generated position %d", token, c.cfg.TopN, len(contextTokens))
		}
		if c.cfg.Coding == "huffman" {
			tree, err := buildHuffman(candidates, c.cfg.Temperature)
			if err != nil {
				return nil, err
			}
			code, ok := tree.codeFor(bucket, nil)
			if !ok {
				return nil, errors.New("internal Huffman code error")
			}
			for _, bit := range code {
				decoded = appendBit(decoded, bitOffset, bit)
				bitOffset++
			}
		} else {
			for i := c.bits - 1; i >= 0; i-- {
				decoded = appendBit(decoded, bitOffset, (bucket>>uint(i))&1)
				bitOffset++
			}
		}
		contextTokens = append(contextTokens, token)
		if bitOffset >= 8 {
			headerBytes, declaredBytes, ready, frameErr := declaredFrame(decoded)
			if frameErr != nil {
				return nil, frameErr
			}
			if ready {
				declaredBits := (headerBytes + declaredBytes) * 8
				if bitOffset >= declaredBits {
					paddingOK := true
					for i := declaredBits; i < bitOffset; i++ {
						if readBits(decoded, i, 1) != 0 {
							paddingOK = false
							break
						}
					}
					if paddingOK {
						dataEnd = position + 1
						break
					}
				}
			}
		}
	}
	if len(observed)-dataEnd > c.cfg.FinishTokens {
		return nil, errors.New("generated text has too many finishing tokens")
	}
	for finishIndex, token := range observed[dataEnd:] {
		candidates, err := c.candidates(ctx, contextTokens, observed[:dataEnd+finishIndex])
		if err != nil {
			return nil, err
		}
		if token != candidates[0].ID {
			return nil, errors.New("generated text has an invalid finishing token")
		}
		contextTokens = append(contextTokens, token)
	}
	headerBytes, want, ready, err := declaredFrame(decoded)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("generated text is too short to contain a frame")
	}
	if want > len(decoded)-headerBytes {
		return nil, fmt.Errorf("truncated generated text: frame declares %d bytes, recovered %d", want, len(decoded)-headerBytes)
	}
	wantBits := (headerBytes + want) * 8
	if bitOffset < wantBits {
		return nil, errors.New("generated text contains an incomplete frame")
	}
	for i := wantBits; i < bitOffset; i++ {
		if readBits(decoded, i, 1) != 0 {
			return nil, errors.New("generated text has non-zero trailing data")
		}
	}
	if c.cfg.Coding == "uniform" {
		expectedTokens := (wantBits + c.bits - 1) / c.bits
		if dataEnd != expectedTokens {
			return nil, fmt.Errorf("generated text has %d data tokens; frame requires %d", dataEnd, expectedTokens)
		}
	}
	return append([]byte(nil), decoded[headerBytes:headerBytes+want]...), nil
}

// DecodeUnframedCandidates recovers the small set of whole-byte payloads that
// could have ended at the observed arithmetic token boundaries. The caller
// must use authentication to select one; no unauthenticated candidate is safe
// to accept as plaintext.
func (c *GenerativeCodec) DecodeUnframedCandidates(ctx context.Context, text string) ([][]byte, error) {
	if c.cfg.Coding != "arithmetic" {
		return nil, errors.New("unframed decoding requires arithmetic coding")
	}
	if c.cfg.RefreshSentences {
		return nil, errors.New("unframed decoding does not support sentence context refresh")
	}
	observed, err := c.model.Tokenize(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("tokenize generated text: %w", err)
	}
	contextTokens, err := c.model.Tokenize(ctx, c.cfg.Prompt)
	if err != nil {
		return nil, fmt.Errorf("tokenize prompt: %w", err)
	}
	decoded := make([]byte, 0, len(observed))
	bitOffset := 0
	encoder := newArithmeticEncoder(func(bit int) { decoded = appendBit(decoded, bitOffset, bit); bitOffset++ })
	offsets := make([]int, len(observed)+1)
	greedy := make([]bool, len(observed))
	visible := make([]int, 0, len(observed))
	for position, token := range observed {
		candidates, err := c.candidates(ctx, contextTokens, visible)
		if err != nil {
			return nil, err
		}
		symbol := -1
		for i, candidate := range candidates {
			if candidate.ID == token {
				symbol = i
				break
			}
		}
		if symbol < 0 {
			return nil, fmt.Errorf("token %d is outside the arithmetic candidate set", token)
		}
		frequencies, err := makeFrequencies(candidates, c.cfg.Temperature, c.cfg.LengthBias)
		if err != nil {
			return nil, err
		}
		encoder.symbol(symbol, frequencies)
		greedy[position] = symbol == 0
		contextTokens = append(contextTokens, token)
		visible = append(visible, token)
		offsets[position+1] = bitOffset
	}

	suffixGreedy := make([]bool, len(observed)+1)
	suffixGreedy[len(observed)] = true
	for i := len(observed) - 1; i >= 0; i-- {
		suffixGreedy[i] = greedy[i] && suffixGreedy[i+1]
	}
	firstEnd := len(observed) - c.cfg.FinishTokens
	if firstEnd < 0 {
		firstEnd = 0
	}
	seen := make(map[string]struct{})
	var payloads [][]byte
	for dataEnd := firstEnd; dataEnd <= len(observed); dataEnd++ {
		if !suffixGreedy[dataEnd] {
			continue
		}
		if dataEnd == 0 {
			payloads = append(payloads, []byte{})
			seen[""] = struct{}{}
			continue
		}
		previousBits := offsets[dataEnd-1]
		confirmedBits := offsets[dataEnd]
		for byteLength := previousBits/8 + 1; byteLength*8 <= confirmedBits; byteLength++ {
			targetBits := byteLength * 8
			valid := true
			for bit := targetBits; bit < confirmedBits; bit++ {
				want := 1
				if (bit-targetBits)%2 == 1 {
					want = 0
				}
				if readBits(decoded, bit, 1) != want {
					valid = false
					break
				}
			}
			if !valid || byteLength > len(decoded) {
				continue
			}
			payload := append([]byte(nil), decoded[:byteLength]...)
			key := string(payload)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			payloads = append(payloads, payload)
		}
	}
	if len(payloads) == 0 {
		return nil, errors.New("generated text contains no complete unframed arithmetic payload")
	}
	return payloads, nil
}

func (c *GenerativeCodec) decodeArithmetic(ctx context.Context, observed, contextTokens []int) ([]byte, error) {
	decoded := make([]byte, 0, len(observed))
	bitOffset := 0
	encoder := newArithmeticEncoder(func(bit int) { decoded = appendBit(decoded, bitOffset, bit); bitOffset++ })
	dataEnd := -1
	visible := make([]int, 0, len(observed))
	sinceRefresh := 0
	for position, token := range observed {
		candidates, err := c.candidates(ctx, contextTokens, visible)
		if err != nil {
			return nil, err
		}
		symbol := -1
		for i, candidate := range candidates {
			if candidate.ID == token {
				symbol = i
				break
			}
		}
		if symbol < 0 {
			return nil, fmt.Errorf("token %d is outside the arithmetic candidate set", token)
		}
		frequencies, err := makeFrequencies(candidates, c.cfg.Temperature, c.cfg.LengthBias)
		if err != nil {
			return nil, err
		}
		encoder.symbol(symbol, frequencies)
		contextTokens = append(contextTokens, token)
		visible = append(visible, token)
		sinceRefresh++
		if bitOffset >= 8 {
			headerBytes, want, ready, frameErr := declaredFrame(decoded)
			if frameErr != nil {
				return nil, frameErr
			}
			if ready {
				wantBits := (headerBytes + want) * 8
				if bitOffset >= wantBits {
					dataEnd = position + 1
					break
				}
			}
		}
		if c.cfg.RefreshSentences && sentenceEnded(candidates[symbol].Text, sinceRefresh) {
			contextTokens, err = c.refreshedContext(ctx, visible)
			if err != nil {
				return nil, err
			}
			sinceRefresh = 0
		}
	}
	if dataEnd < 0 {
		return nil, errors.New("generated text contains an incomplete arithmetic frame")
	}
	headerBytes, want, ready, err := declaredFrame(decoded)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("generated text contains an incomplete arithmetic frame")
	}
	wantBits := (headerBytes + want) * 8
	if bitOffset < wantBits || len(decoded) < headerBytes+want {
		return nil, errors.New("generated text contains a truncated arithmetic frame")
	}
	if len(observed)-dataEnd > c.cfg.FinishTokens {
		return nil, errors.New("generated text has too many finishing tokens")
	}
	for finishIndex, token := range observed[dataEnd:] {
		candidates, err := c.candidates(ctx, contextTokens, observed[:dataEnd+finishIndex])
		if err != nil {
			return nil, err
		}
		if token != candidates[0].ID {
			return nil, errors.New("generated text has an invalid finishing token")
		}
		contextTokens = append(contextTokens, token)
	}
	return append([]byte(nil), decoded[headerBytes:headerBytes+want]...), nil
}

func declaredFrame(decoded []byte) (headerBytes, payloadBytes int, ready bool, err error) {
	length, n := binary.Uvarint(decoded)
	if n == 0 {
		return 0, 0, false, nil
	}
	if n < 0 || length > uint64(^uint32(0)) {
		return 0, 0, false, errors.New("generated text contains an invalid frame length")
	}
	return n, int(length), true, nil
}

func sentenceEnded(tokenText string, tokensSinceRefresh int) bool {
	return tokensSinceRefresh >= 48 && endsSentence(tokenText)
}

func endsSentence(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?")
}

func (c *GenerativeCodec) refreshedContext(ctx context.Context, visibleTokens []int) ([]int, error) {
	visible, err := c.model.Detokenize(ctx, visibleTokens)
	if err != nil {
		return nil, fmt.Errorf("detokenize visible context: %w", err)
	}
	prompt := c.cfg.Prompt + visible +
		"<|eot_id|><|start_header_id|>user<|end_header_id|>\n\nContinue the same casual thought with one more ordinary sentence. Do not add labels or formatting.<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n"
	tokens, err := c.model.Tokenize(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("refresh sentence context: %w", err)
	}
	return tokens, nil
}

func appendBit(dst []byte, offset, bit int) []byte {
	if offset/8 >= len(dst) {
		dst = append(dst, 0)
	}
	if bit != 0 {
		dst[offset/8] |= 1 << uint(7-offset%8)
	}
	return dst
}

func (c *GenerativeCodec) candidates(ctx context.Context, tokens, visibleTokens []int) ([]TokenCandidate, error) {
	want := c.cfg.TopN
	requested := want
	if c.cfg.StrictStyle {
		requested *= c.cfg.CandidatePool
	}
	var got []TokenCandidate
	var err error
	if safe, ok := c.model.(copySafeLanguageModel); ok {
		got, err = safe.NextCopySafe(ctx, tokens, visibleTokens, requested)
	} else {
		got, err = c.model.Next(ctx, tokens, requested)
	}
	if err != nil {
		return nil, fmt.Errorf("predict next token: %w", err)
	}
	if len(got) != requested {
		return nil, fmt.Errorf("model returned %d candidates; want %d", len(got), requested)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].LogProb == got[j].LogProb {
			return got[i].ID < got[j].ID
		}
		return got[i].LogProb > got[j].LogProb
	})
	seen := make(map[int]struct{}, len(got))
	filtered := make([]TokenCandidate, 0, want)
	for _, candidate := range got {
		if _, ok := seen[candidate.ID]; ok {
			return nil, fmt.Errorf("model returned duplicate candidate token %d", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		if c.cfg.StrictStyle && !ordinaryVisibleToken(candidate.Text) {
			continue
		}
		filtered = append(filtered, candidate)
		if len(filtered) == want {
			break
		}
	}
	if len(filtered) != want {
		return nil, fmt.Errorf("only %d of %d model candidates passed strict visible-text filtering; need %d", len(filtered), requested, want)
	}
	return filtered, nil
}

var disallowedVisibleWords = map[string]struct{}{
	"assistant": {}, "example": {}, "format": {}, "input": {}, "instruction": {}, "instructions": {},
	"message": {}, "messages": {}, "metadata": {}, "note": {}, "output": {}, "prompt": {}, "prompts": {},
	"participant": {}, "participants": {},
	"recipient": {}, "recipients": {}, "response": {}, "role": {}, "sender": {}, "sent": {}, "system": {},
	"timestamp": {}, "transcript": {}, "user": {}, "analysis": {},
}

func ordinaryVisibleToken(text string) bool {
	if text == "" {
		return false
	}
	if strings.ContainsAny(text, "\r\n\t0123456789#{}[]()<>*`|\\\":~&=+_^%$@/") {
		return false
	}
	if strings.ContainsAny(text, "“”„‟«»（）【】") {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	hasLetter := false
	for _, r := range trimmed {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	if !hasLetter && !strings.Contains(".,!?;'", trimmed) {
		return false
	}
	word := strings.ToLower(strings.Trim(text, " .,!?;'-_"))
	_, banned := disallowedVisibleWords[word]
	return !banned
}

func readBits(src []byte, offset, count int) int {
	v := 0
	for i := 0; i < count; i++ {
		v <<= 1
		pos := offset + i
		if pos < len(src)*8 && src[pos/8]&(1<<uint(7-pos%8)) != 0 {
			v |= 1
		}
	}
	return v
}

func writeBits(dst []byte, offset, count, value int) {
	for i := 0; i < count; i++ {
		pos := offset + i
		if pos >= len(dst)*8 {
			return
		}
		if value&(1<<uint(count-1-i)) != 0 {
			dst[pos/8] |= 1 << uint(7-pos%8)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
