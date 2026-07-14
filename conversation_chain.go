package decalgo

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// ChainRecord is the public state needed by every member of a group. Plaintext
// is deliberately absent; peers may keep different local views of decrypted
// messages while sharing the same ordered carrier transcript.
type ChainRecord struct {
	Index          uint64 `json:"index"`
	From           string `json:"from"`
	SenderSequence uint64 `json:"sender_sequence"`
	Encrypted      string `json:"encrypted"`
}

// ConversationChain turns a sequence of messages from multiple senders into
// one authenticated, ordered, rolling generative conversation.
type ConversationChain struct {
	model        LanguageModel
	key          []byte
	conversation string
	baseConfig   GenerativeConfig
	records      []ChainRecord
	sequences    map[string]uint64
	chain        [32]byte
}

// EncodingBudget accounts for every bit embedded in a text carrier. Prompt,
// sender, sequence, and chain state are synchronized context and are not part
// of the encoded payload.
type EncodingBudget struct {
	PlaintextBytes      int
	PackedBytes         int
	AuthenticationBytes int
	FrameLengthBytes    int
	TerminationBytes    int
	TotalHiddenBytes    int
}

func (c *ConversationChain) EncodingBudget(plaintext []byte) (EncodingBudget, error) {
	packed, err := packMessage(plaintext)
	if err != nil {
		return EncodingBudget{}, err
	}
	sealedBytes := sivTagSize + len(packed)
	frameLengthBytes := binary.PutUvarint(make([]byte, binary.MaxVarintLen64), uint64(sealedBytes))
	terminationBytes := 0
	if c.baseConfig.Coding == "arithmetic" {
		terminationBytes = arithmeticGuardBits / 8
	}
	return EncodingBudget{
		PlaintextBytes: len(plaintext), PackedBytes: len(packed), AuthenticationBytes: sivTagSize,
		FrameLengthBytes: frameLengthBytes, TerminationBytes: terminationBytes,
		TotalHiddenBytes: frameLengthBytes + sealedBytes + terminationBytes,
	}, nil
}

func NewConversationChain(model LanguageModel, key []byte, conversation string, cfg GenerativeConfig) (*ConversationChain, error) {
	if model == nil {
		return nil, errors.New("language model is required")
	}
	if len(key) < 16 {
		return nil, errors.New("chain key must contain at least 16 bytes of entropy")
	}
	if strings.TrimSpace(conversation) == "" {
		return nil, errors.New("conversation identifier is required")
	}
	seed := sha256.Sum256([]byte("decalgo-group-chain-v1\x00" + conversation))
	return &ConversationChain{model: model, key: append([]byte(nil), key...), conversation: conversation,
		baseConfig: cfg, sequences: make(map[string]uint64), chain: seed}, nil
}

func (c *ConversationChain) Records() []ChainRecord { return append([]ChainRecord(nil), c.records...) }

func (c *ConversationChain) SyncCode() string {
	return fmt.Sprintf("%x", c.chain[:6])
}

// RestorePublic rebuilds rolling prompt, index, sender counters, and hash state
// from a previously persisted public transcript.
func (c *ConversationChain) RestorePublic(records []ChainRecord) error {
	for _, record := range records {
		if record.Index != uint64(len(c.records)) {
			return fmt.Errorf("record index %d; want %d", record.Index, len(c.records))
		}
		wantSequence := c.sequences[record.From]
		if record.SenderSequence != wantSequence {
			return fmt.Errorf("sender %q sequence %d; want %d", record.From, record.SenderSequence, wantSequence)
		}
		if err := validateSender(record.From); err != nil {
			return err
		}
		if record.Encrypted == "" {
			return errors.New("empty encrypted carrier in chain state")
		}
		c.commit(record)
	}
	return nil
}

func (c *ConversationChain) Send(ctx context.Context, from string, plaintext []byte) (ChainRecord, error) {
	if err := validateSender(from); err != nil {
		return ChainRecord{}, err
	}
	record := ChainRecord{Index: uint64(len(c.records)), From: from, SenderSequence: c.sequences[from]}
	codec, err := NewGenerativeCodec(c.model, c.messageConfig(from))
	if err != nil {
		return ChainRecord{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		sealed, err := c.seal(from, record.Index, record.SenderSequence, plaintext)
		if err != nil {
			return ChainRecord{}, err
		}
		record.Encrypted, err = codec.Encode(ctx, sealed)
		if err == nil {
			c.commit(record)
			return record, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "tokenizer cannot losslessly represent") {
			return ChainRecord{}, err
		}
	}
	return ChainRecord{}, fmt.Errorf("could not produce a transport-safe carrier after 8 attempts: %w", lastErr)
}

func (c *ConversationChain) Receive(ctx context.Context, from, encrypted string) ([]byte, ChainRecord, error) {
	if err := validateSender(from); err != nil {
		return nil, ChainRecord{}, err
	}
	if encrypted == "" {
		return nil, ChainRecord{}, errors.New("encrypted carrier is empty")
	}
	record := ChainRecord{Index: uint64(len(c.records)), From: from, SenderSequence: c.sequences[from], Encrypted: encrypted}
	codec, err := NewGenerativeCodec(c.model, c.messageConfig(from))
	if err != nil {
		return nil, ChainRecord{}, err
	}
	sealed, err := codec.Decode(ctx, encrypted)
	if err != nil {
		return nil, ChainRecord{}, err
	}
	plaintext, err := c.open(from, record.Index, record.SenderSequence, sealed)
	if err != nil {
		return nil, ChainRecord{}, fmt.Errorf("%w at index %d for sender %q (local sync %s); verify the same phrase and conversation, exact sender name, exact unedited carrier, and identical prior-message order on both devices", err, record.Index, from, c.SyncCode())
	}
	c.commit(record)
	return plaintext, record, nil
}

func (c *ConversationChain) messageConfig(from string) GenerativeConfig {
	cfg := c.baseConfig
	if cfg.ChainSystem != "" {
		var transcript strings.Builder
		if len(c.records) == 0 {
			transcript.WriteString("The group chat has just started.")
		} else {
			transcript.WriteString("Earlier chat messages, oldest first:\n\n")
			for _, record := range c.records {
				transcript.WriteString(escapePromptControl(record.Encrypted))
				transcript.WriteString("\n\n")
			}
		}
		transcript.WriteString("\nWrite only one natural reply by the current participant. Do not include a name, label, signature, or transcript.")
		cfg.Prompt = "<|begin_of_text|><|start_header_id|>system<|end_header_id|>\n\n" + cfg.ChainSystem +
			"<|eot_id|><|start_header_id|>user<|end_header_id|>\n\n" + transcript.String() +
			"<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n"
		return cfg
	}
	var prompt strings.Builder
	prompt.WriteString(cfg.Prompt)
	prompt.WriteString("\n\nVisible group conversation:\n")
	for _, record := range c.records {
		prompt.WriteString(record.Encrypted)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Write only one natural unlabeled reply: ")
	cfg.Prompt = prompt.String()
	return cfg
}

func escapePromptControl(text string) string { return strings.ReplaceAll(text, "<|", "< |") }

func (c *ConversationChain) commit(record ChainRecord) {
	h := sha256.New()
	h.Write([]byte("decalgo-group-commit-v1\x00"))
	h.Write(c.chain[:])
	h.Write(chainNumbers(record.Index, record.SenderSequence))
	h.Write([]byte(record.From))
	h.Write([]byte{0})
	h.Write([]byte(record.Encrypted))
	copy(c.chain[:], h.Sum(nil))
	c.records = append(c.records, record)
	c.sequences[record.From]++
}

func (c *ConversationChain) seal(from string, index, sequence uint64, plaintext []byte) ([]byte, error) {
	packed, err := packMessage(plaintext)
	if err != nil {
		return nil, fmt.Errorf("pack chained message: %w", err)
	}
	return sealSIV(c.key, c.aad(from, index, sequence), packed)
}

func (c *ConversationChain) open(from string, index, sequence uint64, sealed []byte) ([]byte, error) {
	packed, err := openSIV(c.key, c.aad(from, index, sequence), sealed)
	if err != nil {
		// Decode records created by the former nonce-bearing AES-GCM format.
		aead, legacyErr := newAEAD(c.key)
		if legacyErr != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
			return nil, errors.New("chained carrier authentication failed")
		}
		packed, legacyErr = aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], c.aad(from, index, sequence))
		if legacyErr != nil {
			return nil, errors.New("chained carrier authentication failed")
		}
	}
	plaintext, err := unpackMessage(packed)
	if err != nil {
		return nil, fmt.Errorf("unpack chained message: %w", err)
	}
	return plaintext, nil
}

func (c *ConversationChain) aad(from string, index, sequence uint64) []byte {
	b := make([]byte, 0, len(c.conversation)+len(from)+96)
	b = append(b, "decalgo-group-aad-v1\x00"...)
	b = append(b, c.conversation...)
	b = append(b, 0)
	b = append(b, from...)
	b = append(b, 0)
	b = append(b, chainNumbers(index, sequence)...)
	b = append(b, c.chain[:]...)
	return b
}

func chainNumbers(index, sequence uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[:8], index)
	binary.BigEndian.PutUint64(b[8:], sequence)
	return b
}

func validateSender(from string) error {
	if strings.TrimSpace(from) == "" {
		return errors.New("sender name is required")
	}
	if strings.ContainsAny(from, "\r\n\x00") {
		return errors.New("sender name contains a forbidden character")
	}
	if len(from) > 128 {
		return errors.New("sender name is too long")
	}
	return nil
}
