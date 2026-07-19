package conversationstenography

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// encodeChunkWire builds the large-message wire payload for one cover.
// Part 0 layout: uvarint(part) || uvarint(total) || uvarint(sealedLen) || uvarint(len(piece)) || piece
// Later parts:   uvarint(part) || uvarint(total) || uvarint(len(piece)) || piece
func encodeChunkWire(part, total, sealedLen int, piece []byte) ([]byte, error) {
	if total < 1 {
		return nil, errors.New("total must be at least 1")
	}
	if part < 0 || part >= total {
		return nil, fmt.Errorf("part %d out of range for total %d", part, total)
	}
	if part == 0 && sealedLen < 0 {
		return nil, errors.New("sealed length is required on part 0")
	}
	buf := make([]byte, 0, binary.MaxVarintLen64*4+len(piece))
	tmp := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(tmp, uint64(part))
	buf = append(buf, tmp[:n]...)
	n = binary.PutUvarint(tmp, uint64(total))
	buf = append(buf, tmp[:n]...)
	if part == 0 {
		n = binary.PutUvarint(tmp, uint64(sealedLen))
		buf = append(buf, tmp[:n]...)
	}
	n = binary.PutUvarint(tmp, uint64(len(piece)))
	buf = append(buf, tmp[:n]...)
	buf = append(buf, piece...)
	return buf, nil
}

// decodeChunkWire parses a large-message wire payload.
// sealedLen is -1 when the field is absent (parts i > 0).
func decodeChunkWire(wire []byte) (part, total, sealedLen, pieceLen int, piece []byte, err error) {
	sealedLen = -1
	if len(wire) == 0 {
		return 0, 0, -1, 0, nil, errors.New("empty chunk wire")
	}
	part64, n := binary.Uvarint(wire)
	if n <= 0 {
		return 0, 0, -1, 0, nil, errors.New("malformed part varint")
	}
	wire = wire[n:]
	total64, n := binary.Uvarint(wire)
	if n <= 0 {
		return 0, 0, -1, 0, nil, errors.New("malformed total varint")
	}
	wire = wire[n:]
	if total64 < 1 || total64 > uint64(^uint(0)>>1) {
		return 0, 0, -1, 0, nil, errors.New("invalid total")
	}
	if part64 >= total64 {
		return 0, 0, -1, 0, nil, fmt.Errorf("part %d out of range for total %d", part64, total64)
	}
	part, total = int(part64), int(total64)
	if part == 0 {
		sealed64, n := binary.Uvarint(wire)
		if n <= 0 {
			return 0, 0, -1, 0, nil, errors.New("malformed sealed length varint")
		}
		wire = wire[n:]
		if sealed64 > uint64(^uint(0)>>1) {
			return 0, 0, -1, 0, nil, errors.New("sealed length overflow")
		}
		sealedLen = int(sealed64)
	}
	piece64, n := binary.Uvarint(wire)
	if n <= 0 {
		return 0, 0, -1, 0, nil, errors.New("malformed piece length varint")
	}
	wire = wire[n:]
	if piece64 > uint64(^uint(0)>>1) {
		return 0, 0, -1, 0, nil, errors.New("piece length overflow")
	}
	pieceLen = int(piece64)
	if len(wire) < pieceLen {
		return 0, 0, -1, 0, nil, errors.New("truncated chunk piece")
	}
	if len(wire) != pieceLen {
		return 0, 0, -1, 0, nil, errors.New("trailing bytes after chunk piece")
	}
	piece = append([]byte(nil), wire[:pieceLen]...)
	return part, total, sealedLen, pieceLen, piece, nil
}

// splitSealed divides sealed into pieces of at most maxPiece bytes.
func splitSealed(sealed []byte, maxPiece int) ([][]byte, error) {
	if maxPiece < 1 {
		return nil, errors.New("max piece size must be at least 1")
	}
	if len(sealed) == 0 {
		return [][]byte{{}}, nil
	}
	total := (len(sealed) + maxPiece - 1) / maxPiece
	pieces := make([][]byte, 0, total)
	for offset := 0; offset < len(sealed); offset += maxPiece {
		end := offset + maxPiece
		if end > len(sealed) {
			end = len(sealed)
		}
		pieces = append(pieces, append([]byte(nil), sealed[offset:end]...))
	}
	return pieces, nil
}

// joinSealed concatenates pieces in order.
func joinSealed(pieces [][]byte) []byte {
	n := 0
	for _, p := range pieces {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range pieces {
		out = append(out, p...)
	}
	return out
}

// validateAssembledPieces checks part/total consistency and sealed length.
func validateAssembledPieces(total, sealedLen int, pieces [][]byte) error {
	if total < 1 {
		return errors.New("total must be at least 1")
	}
	if len(pieces) != total {
		return fmt.Errorf("piece count %d; want %d", len(pieces), total)
	}
	sum := 0
	for i, p := range pieces {
		if p == nil {
			return fmt.Errorf("missing piece %d", i)
		}
		sum += len(p)
	}
	if sum != sealedLen {
		return fmt.Errorf("assembled length %d; want sealed_len %d", sum, sealedLen)
	}
	return nil
}
