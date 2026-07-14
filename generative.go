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
	Prompt           string
	ChainSystem      string
	TopN             int
	Coding           string
	Temperature      float64
	FinishTokens     int
	StrictStyle      bool
	CandidatePool    int
	RefreshSentences bool
	ModelFingerprint string
}

// GenerativeCodec embeds framed bytes in deterministic next-token choices.
// It is stateless: each text starts from Config.Prompt.
type GenerativeCodec struct {
	model LanguageModel
	cfg   GenerativeConfig
	bits  int
}

const arithmeticGuardBits = 32

func NewGenerativeCodec(model LanguageModel, cfg GenerativeConfig) (*GenerativeCodec, error) {
	if model == nil {
		return nil, errors.New("language model is required")
	}
	if cfg.Prompt == "" {
		return nil, errors.New("prompt must not be empty")
	}
	if cfg.TopN < 2 || cfg.TopN&(cfg.TopN-1) != 0 {
		return nil, errors.New("top-n must be a power of two greater than one")
	}
	if cfg.Coding == "" {
		cfg.Coding = "uniform"
	}
	if cfg.Coding != "uniform" && cfg.Coding != "huffman" && cfg.Coding != "arithmetic" {
		return nil, errors.New("coding must be uniform, huffman, or arithmetic")
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
	if cfg.ModelFingerprint == "" {
		cfg.ModelFingerprint = model.Fingerprint()
	}
	if cfg.ModelFingerprint != model.Fingerprint() {
		return nil, fmt.Errorf("model fingerprint mismatch: configured %q, loaded %q", cfg.ModelFingerprint, model.Fingerprint())
	}
	return &GenerativeCodec{model: model, cfg: cfg, bits: bits.TrailingZeros(uint(cfg.TopN))}, nil
}

func (c *GenerativeCodec) Config() GenerativeConfig { return c.cfg }

// Encode frames payload with a variable-length byte count and returns generated text.
func (c *GenerativeCodec) Encode(ctx context.Context, payload []byte) (string, error) {
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return "", errors.New("payload is too large")
	}
	frame := binary.AppendUvarint(nil, uint64(len(payload)))
	frame = append(frame, payload...)
	contextTokens, err := c.model.Tokenize(ctx, c.cfg.Prompt)
	if err != nil {
		return "", fmt.Errorf("tokenize prompt: %w", err)
	}
	generated := make([]int, 0, (len(frame)*8+c.bits-1)/c.bits)
	sinceRefresh := 0
	bitOffset := 0
	if c.cfg.Coding == "arithmetic" {
		readOffset := 0
		sourceBit := func(offset int) int {
			if offset < len(frame)*8 {
				return readBits(frame, offset, 1)
			}
			if (offset-len(frame)*8)%2 == 0 {
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
		targetBits := len(frame)*8 + arithmeticGuardBits
		for confirmed < targetBits {
			candidates, err := c.candidates(ctx, contextTokens, generated)
			if err != nil {
				return "", err
			}
			frequencies, err := makeFrequencies(candidates, c.cfg.Temperature)
			if err != nil {
				return "", err
			}
			symbol := decoder.symbol(frequencies)
			encoder.symbol(symbol, frequencies)
			if desynchronized {
				return "", errors.New("arithmetic coder desynchronized")
			}
			token := candidates[symbol].ID
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
			if len(generated) > len(frame)*64+1024 {
				return "", fmt.Errorf("arithmetic coder failed to make progress after %d tokens (%d/%d bits confirmed)", len(generated), confirmed, targetBits)
			}
		}
		bitOffset = confirmed
	} else {
		for bitOffset < len(frame)*8 {
			candidates, err := c.candidates(ctx, contextTokens, generated)
			if err != nil {
				return "", err
			}
			var token int
			if c.cfg.Coding == "huffman" {
				tree, err := buildHuffman(candidates, c.cfg.Temperature)
				if err != nil {
					return "", err
				}
				leaf := tree
				for leaf.candidate < 0 {
					bit := readBits(frame, bitOffset, 1)
					bitOffset++
					if bit == 0 {
						leaf = leaf.left
					} else {
						leaf = leaf.right
					}
				}
				token = candidates[leaf.candidate].ID
			} else {
				bucket := readBits(frame, bitOffset, c.bits)
				bitOffset += c.bits
				token = candidates[bucket].ID
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
		frequencies, err := makeFrequencies(candidates, c.cfg.Temperature)
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
				targetBits := wantBits + arithmeticGuardBits
				if bitOffset >= targetBits {
					guardOK := true
					for i := wantBits; i < targetBits; i++ {
						want := 0
						if (i-wantBits)%2 == 0 {
							want = 1
						}
						if readBits(decoded, i, 1) != want {
							guardOK = false
							break
						}
					}
					if guardOK {
						dataEnd = position + 1
						break
					}
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
