package conversationstenography

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestChunkWireRoundTrip(t *testing.T) {
	piece := []byte("hello-sealed-piece")
	wire0, err := encodeChunkWire(0, 3, 40, piece)
	if err != nil {
		t.Fatal(err)
	}
	part, total, sealedLen, pieceLen, got, err := decodeChunkWire(wire0)
	if err != nil {
		t.Fatal(err)
	}
	if part != 0 || total != 3 || sealedLen != 40 || pieceLen != len(piece) || !bytes.Equal(got, piece) {
		t.Fatalf("part0 decode: part=%d total=%d sealed=%d len=%d got=%q", part, total, sealedLen, pieceLen, got)
	}

	wire1, err := encodeChunkWire(1, 3, -1, piece)
	if err != nil {
		t.Fatal(err)
	}
	part, total, sealedLen, pieceLen, got, err = decodeChunkWire(wire1)
	if err != nil {
		t.Fatal(err)
	}
	if part != 1 || total != 3 || sealedLen != -1 || pieceLen != len(piece) || !bytes.Equal(got, piece) {
		t.Fatalf("part1 decode: part=%d total=%d sealed=%d len=%d got=%q", part, total, sealedLen, pieceLen, got)
	}
}

func TestChunkWireRejectsMalformed(t *testing.T) {
	if _, _, _, _, _, err := decodeChunkWire(nil); err == nil {
		t.Fatal("expected empty wire error")
	}
	wire, err := encodeChunkWire(0, 2, 10, []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := decodeChunkWire(wire[:len(wire)-1]); err == nil {
		t.Fatal("expected truncated piece error")
	}
	if _, err := encodeChunkWire(2, 2, 10, []byte("x")); err == nil {
		t.Fatal("expected part out of range")
	}
	if _, err := encodeChunkWire(0, 0, 10, []byte("x")); err == nil {
		t.Fatal("expected total < 1")
	}
}

func TestSplitJoinRoundTrip(t *testing.T) {
	sealed := make([]byte, 97)
	if _, err := rand.Read(sealed); err != nil {
		t.Fatal(err)
	}
	pieces, err := splitSealed(sealed, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 7 {
		t.Fatalf("got %d pieces; want 7", len(pieces))
	}
	joined := joinSealed(pieces)
	if !bytes.Equal(joined, sealed) {
		t.Fatal("join did not restore sealed blob")
	}
	if err := validateAssembledPieces(len(pieces), len(sealed), pieces); err != nil {
		t.Fatal(err)
	}
}

func TestSplitJoinEmptyAndRejects(t *testing.T) {
	pieces, err := splitSealed(nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 1 || len(pieces[0]) != 0 {
		t.Fatalf("empty sealed should yield one empty piece: %#v", pieces)
	}
	if _, err := splitSealed([]byte("x"), 0); err == nil {
		t.Fatal("expected maxPiece < 1 error")
	}
	parts := [][]byte{[]byte("ab"), []byte("cd")}
	if err := validateAssembledPieces(2, 3, parts); err == nil {
		t.Fatal("expected sealed length mismatch")
	}
	if err := validateAssembledPieces(2, 4, [][]byte{[]byte("ab"), nil}); err == nil {
		t.Fatal("expected missing piece")
	}
	swapped := [][]byte{parts[1], parts[0]}
	if bytes.Equal(joinSealed(swapped), joinSealed(parts)) {
		t.Fatal("swap should change joined bytes")
	}
	dropped := [][]byte{parts[0]}
	if err := validateAssembledPieces(2, 4, dropped); err == nil {
		t.Fatal("expected drop to fail validation")
	}
}

func TestChunkWireSinglePart(t *testing.T) {
	sealed := []byte{1, 2, 3, 4, 5}
	pieces, err := splitSealed(sealed, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 1 {
		t.Fatalf("want 1 piece, got %d", len(pieces))
	}
	wire, err := encodeChunkWire(0, 1, len(sealed), pieces[0])
	if err != nil {
		t.Fatal(err)
	}
	part, total, sealedLen, _, piece, err := decodeChunkWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if part != 0 || total != 1 || sealedLen != len(sealed) || !bytes.Equal(piece, sealed) {
		t.Fatalf("single part mismatch: part=%d total=%d sealed=%d piece=%v", part, total, sealedLen, piece)
	}
}
