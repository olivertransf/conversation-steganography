package conversationstenography

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// ReceiveStatus reports progress while assembling a multi-cover logical message.
type ReceiveStatus struct {
	Waiting      bool
	Part         int
	Total        int
	ReceivedMask string
	SyncCode     string
}

// PendingAssembly holds received pieces of an incomplete logical message.
type PendingAssembly struct {
	From            string
	Total           int
	SealedLen       int
	NextPart        int
	Pieces          [][]byte
	ChainBefore     [32]byte
	SenderSeqBefore uint64
}

func (c *ConversationChain) PendingAssemblyFor(from string) (PendingAssembly, bool) {
	if c.pending == nil {
		return PendingAssembly{}, false
	}
	p, ok := c.pending[from]
	if !ok || p == nil {
		return PendingAssembly{}, false
	}
	return clonePending(*p), true
}

func (c *ConversationChain) ExportPending() []PendingAssembly {
	if len(c.pending) == 0 {
		return nil
	}
	out := make([]PendingAssembly, 0, len(c.pending))
	for _, p := range c.pending {
		if p != nil {
			out = append(out, clonePending(*p))
		}
	}
	return out
}

func (c *ConversationChain) RestorePending(pending []PendingAssembly) error {
	c.pending = make(map[string]*PendingAssembly, len(pending))
	for _, p := range pending {
		if err := validateSender(p.From); err != nil {
			return err
		}
		if p.Total < 1 || len(p.Pieces) != p.Total {
			return fmt.Errorf("invalid pending assembly for %q", p.From)
		}
		cp := clonePending(p)
		c.pending[p.From] = &cp
	}
	return nil
}

func clonePending(p PendingAssembly) PendingAssembly {
	out := p
	if p.Pieces != nil {
		out.Pieces = make([][]byte, len(p.Pieces))
		for i, piece := range p.Pieces {
			if piece != nil {
				out.Pieces[i] = append([]byte(nil), piece...)
			}
		}
	}
	return out
}

func (c *ConversationChain) logicalAAD(from string, chainBefore [32]byte, senderSeqBefore uint64, trial int, mode byte) []byte {
	b := make([]byte, 0, len(c.conversation)+len(from)+64)
	b = append(b, "decalgo-large-msg-v1\x00"...)
	b = append(b, c.conversation...)
	b = append(b, 0)
	b = append(b, from...)
	b = append(b, 0)
	b = append(b, chainBefore[:]...)
	seq := make([]byte, 8)
	binary.BigEndian.PutUint64(seq, senderSeqBefore)
	b = append(b, seq...)
	if trial != 0 {
		b = append(b, 0, 't', 'r', 'i', 'a', 'l', byte(trial))
	}
	b = append(b, 0, 'p', 'a', 'c', 'k', mode)
	return b
}

func (c *ConversationChain) messageConfigWithExtra(from string, extra []ChainRecord) GenerativeConfig {
	saved := c.records
	if len(extra) > 0 {
		c.records = append(append([]ChainRecord(nil), saved...), extra...)
	}
	cfg := c.messageConfig(from)
	c.records = saved
	capCfg := c.capacityConfig()
	cfg.Coding = capCfg.Coding
	cfg.TopN = capCfg.TopN
	cfg.LengthBias = capCfg.LengthBias
	if c.baseConfig.StrictStyle {
		cfg.CandidatePool = capCfg.CandidatePool
	}
	return cfg
}

func (c *ConversationChain) capacityMessageConfig(from string) GenerativeConfig {
	return c.messageConfigWithExtra(from, nil)
}

// SendMessage packs and seals once, splits into cover-sized pieces, and commits
// all chain records only after every cover encodes successfully.
func (c *ConversationChain) SendMessage(ctx context.Context, from string, plaintext []byte) ([]ChainRecord, error) {
	if err := validateSender(from); err != nil {
		return nil, err
	}
	chainBefore := c.chain
	seqBefore := c.sequences[from]
	startIndex := uint64(len(c.records))

	mode, packed, err := packMessageDetached(plaintext, c.compressionDictionary())
	if err != nil {
		return nil, fmt.Errorf("pack chained message: %w", err)
	}

	maxPiece := estimateMaxPieceBytes(c.maxCoverChars, c.capacityTopN)
	records, err := c.encodeLogicalMessage(ctx, from, mode, packed, chainBefore, seqBefore, startIndex, maxPiece)
	if err != nil && strings.Contains(err.Error(), "cover exceeds max_cover_chars") {
		shrunk := maxPiece / 2
		if shrunk < 1 {
			shrunk = 1
		}
		if shrunk != maxPiece {
			records, err = c.encodeLogicalMessage(ctx, from, mode, packed, chainBefore, seqBefore, startIndex, shrunk)
		}
	}
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		c.commit(record)
	}
	return append([]ChainRecord(nil), records...), nil
}

func (c *ConversationChain) encodeLogicalMessage(ctx context.Context, from string, mode byte, packed []byte, chainBefore [32]byte, seqBefore uint64, startIndex uint64, maxPiece int) ([]ChainRecord, error) {
	trialCount := c.baseConfig.CarrierTrials
	if trialCount == 0 {
		trialCount = 1
	}
	var lastErr error
	for trial := 0; trial < trialCount; trial++ {
		sealed, err := sealSIV(c.key, c.logicalAAD(from, chainBefore, seqBefore, trial, mode), packed)
		if err != nil {
			return nil, err
		}
		pieces, err := splitSealed(sealed, maxPiece)
		if err != nil {
			return nil, err
		}
		records := make([]ChainRecord, 0, len(pieces))
		buffered := make([]ChainRecord, 0, len(pieces))
		ok := true
		for i, piece := range pieces {
			wire, err := encodeChunkWire(i, len(pieces), len(sealed), piece)
			if err != nil {
				return nil, err
			}
			codec, err := NewGenerativeCodec(c.model, c.messageConfigWithExtra(from, buffered))
			if err != nil {
				return nil, err
			}
			var carrier string
			var metrics CarrierMetrics
			if codec.Config().Coding == "arithmetic" && !codec.Config().RefreshSentences {
				carrier, metrics, err = codec.EncodeUnframedWithMetrics(ctx, wire)
			} else {
				carrier, metrics, err = codec.EncodeWithMetrics(ctx, wire)
			}
			if err != nil {
				lastErr = err
				if strings.Contains(err.Error(), "tokenizer cannot losslessly represent") {
					ok = false
					break
				}
				ok = false
				break
			}
			if metrics.VisibleCharacters > c.maxCoverChars {
				lastErr = fmt.Errorf("cover exceeds max_cover_chars (%d > %d)", metrics.VisibleCharacters, c.maxCoverChars)
				ok = false
				break
			}
			human := humanWrittenCarrier(carrier)
			semanticMargin := math.Inf(1)
			if codec.Config().SemanticJudge {
				semanticMargin = math.Inf(-1)
			}
			if human && codec.Config().SemanticJudge {
				_, semanticMargin, err = semanticHumanWritten(ctx, c.model, carrier)
				if err != nil {
					return nil, fmt.Errorf("judge generated carrier: %w", err)
				}
				human = semanticMargin >= codec.Config().SemanticThreshold
			}
			if codec.Config().StrictStyle && !human {
				lastErr = fmt.Errorf("carrier trial %d chunk %d failed human-writing checks", trial, i)
				ok = false
				break
			}
			record := ChainRecord{
				Index:          startIndex + uint64(i),
				From:           from,
				SenderSequence: seqBefore + uint64(i),
				Encrypted:      carrier,
			}
			records = append(records, record)
			buffered = append(buffered, record)
		}
		if ok {
			return records, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("could not encode logical message after %d trials: %w", trialCount, lastErr)
	}
	return nil, fmt.Errorf("could not encode logical message after %d trials", trialCount)
}

// ReceiveMessage accepts one cover of a logical message. When all parts are
// present it opens the sealed blob and returns plaintext with done=true.
func (c *ConversationChain) ReceiveMessage(ctx context.Context, from, encrypted string) ([]byte, bool, ReceiveStatus, error) {
	if err := validateSender(from); err != nil {
		return nil, false, ReceiveStatus{}, err
	}
	if encrypted == "" {
		return nil, false, ReceiveStatus{}, errors.New("encrypted carrier is empty")
	}

	wires, err := c.decodeCapacityWires(ctx, from, encrypted)
	if err == nil {
		for _, wire := range wires {
			part, total, sealedLen, _, piece, parseErr := decodeChunkWire(wire)
			if parseErr != nil {
				continue
			}
			return c.acceptChunk(from, encrypted, part, total, sealedLen, piece)
		}
	}

	plaintext, _, legacyErr := c.Receive(ctx, from, encrypted)
	if legacyErr != nil {
		if err != nil {
			return nil, false, ReceiveStatus{}, fmt.Errorf("%w (also not a large-message chunk: %v)", legacyErr, err)
		}
		return nil, false, ReceiveStatus{}, legacyErr
	}
	return plaintext, true, ReceiveStatus{SyncCode: c.SyncCode()}, nil
}

func (c *ConversationChain) decodeCapacityWires(ctx context.Context, from, encrypted string) ([][]byte, error) {
	codec, err := NewGenerativeCodec(c.model, c.capacityMessageConfig(from))
	if err != nil {
		return nil, err
	}
	if codec.Config().Coding == "arithmetic" && !codec.Config().RefreshSentences {
		candidates, err := codec.DecodeUnframedCandidates(ctx, encrypted)
		if err != nil {
			return nil, err
		}
		if _, searchErr := authenticationHypotheses(len(candidates), 1); searchErr != nil {
			return nil, searchErr
		}
		return candidates, nil
	}
	sealed, err := codec.Decode(ctx, encrypted)
	if err != nil {
		return nil, err
	}
	return [][]byte{sealed}, nil
}

func (c *ConversationChain) acceptChunk(from, encrypted string, part, total, sealedLen int, piece []byte) ([]byte, bool, ReceiveStatus, error) {
	if c.pending == nil {
		c.pending = make(map[string]*PendingAssembly)
	}
	pending := c.pending[from]
	if pending == nil {
		if part != 0 {
			return nil, false, ReceiveStatus{SyncCode: c.SyncCode()}, fmt.Errorf("expected part 0 to start assembly from %q (sync %s)", from, c.SyncCode())
		}
		if sealedLen < 0 {
			return nil, false, ReceiveStatus{}, errors.New("part 0 missing sealed length")
		}
		pending = &PendingAssembly{
			From:            from,
			Total:           total,
			SealedLen:       sealedLen,
			NextPart:        0,
			Pieces:          make([][]byte, total),
			ChainBefore:     c.chain,
			SenderSeqBefore: c.sequences[from],
		}
		c.pending[from] = pending
	} else {
		if total != pending.Total {
			return nil, false, c.statusFor(pending), fmt.Errorf("inconsistent total %d; want %d (sync %s)", total, pending.Total, c.SyncCode())
		}
		if part == 0 {
			if sealedLen >= 0 && sealedLen != pending.SealedLen {
				return nil, false, c.statusFor(pending), fmt.Errorf("inconsistent sealed_len %d; want %d", sealedLen, pending.SealedLen)
			}
		}
		if part != pending.NextPart {
			return nil, false, c.statusFor(pending), fmt.Errorf("expected part %d from %q; got %d (sync %s)", pending.NextPart, from, part, c.SyncCode())
		}
		if pending.Pieces[part] != nil {
			return nil, false, c.statusFor(pending), fmt.Errorf("duplicate part %d from %q", part, from)
		}
	}

	pending.Pieces[part] = append([]byte(nil), piece...)
	pending.NextPart = part + 1
	record := ChainRecord{Index: uint64(len(c.records)), From: from, SenderSequence: c.sequences[from], Encrypted: encrypted}
	c.commit(record)

	status := c.statusFor(pending)
	if pending.NextPart < pending.Total {
		status.Waiting = true
		return nil, false, status, nil
	}

	if err := validateAssembledPieces(pending.Total, pending.SealedLen, pending.Pieces); err != nil {
		delete(c.pending, from)
		return nil, false, status, fmt.Errorf("assembled chunks invalid: %w", err)
	}
	sealed := joinSealed(pending.Pieces)
	// Packing used the dictionary from before this logical message; strip its covers.
	prefix := len(c.records) - pending.Total
	if prefix < 0 {
		prefix = 0
	}
	dict := compressionDictionaryFrom(c.records[:prefix])
	plaintext, err := c.openLogical(from, pending.ChainBefore, pending.SenderSeqBefore, sealed, dict)
	delete(c.pending, from)
	if err != nil {
		return nil, false, ReceiveStatus{SyncCode: c.SyncCode()}, fmt.Errorf("authentication failed / desync: %w", err)
	}
	status.Waiting = false
	status.SyncCode = c.SyncCode()
	status.ReceivedMask = strings.Repeat("1", pending.Total)
	return plaintext, true, status, nil
}

func (c *ConversationChain) openLogical(from string, chainBefore [32]byte, senderSeqBefore uint64, sealed, dictionary []byte) ([]byte, error) {
	trials := c.baseConfig.CarrierTrials
	if trials == 0 {
		trials = 1
	}
	var packed []byte
	var err error
	for trial := 0; trial < trials; trial++ {
		for _, mode := range packingModes() {
			var body []byte
			body, err = openSIV(c.key, c.logicalAAD(from, chainBefore, senderSeqBefore, trial, mode), sealed)
			if err == nil {
				packed = append([]byte{mode}, body...)
				break
			}
		}
		if packed != nil {
			break
		}
	}
	if packed == nil {
		return nil, errors.New("chained carrier authentication failed")
	}
	return unpackMessageWithDictionary(packed, dictionary)
}

func (c *ConversationChain) statusFor(pending *PendingAssembly) ReceiveStatus {
	mask := make([]byte, pending.Total)
	for i, piece := range pending.Pieces {
		if piece != nil {
			mask[i] = '1'
		} else {
			mask[i] = '0'
		}
	}
	return ReceiveStatus{
		Waiting:      pending.NextPart < pending.Total,
		Part:         pending.NextPart,
		Total:        pending.Total,
		ReceivedMask: string(mask),
		SyncCode:     c.SyncCode(),
	}
}
