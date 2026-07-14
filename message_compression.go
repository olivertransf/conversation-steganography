package decalgo

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
)

// This dictionary is protocol state shared by both peers. It targets ordinary
// private-chat language; DEFLATE uses it without placing it in the carrier.
var chatDictionary = []byte(`
the of and to a in is it you that for on with this i was are be as have at or
not but we your from can all my me they so if about just what there one like
do don't did didn't i'm i've i'll i'd you're you've you'll you'd we're we've
we'll we'd they're they've they'll they'd it's that's there's what's who's
how's can't couldn't wouldn't shouldn't won't isn't wasn't weren't haven't
hasn't had been really think know want wanted need needed feel felt might maybe
probably actually honestly seriously sorry thanks please okay ok yeah yes no
hey hi hello bye later today tonight tomorrow yesterday morning afternoon
evening day week weekend home work school class dinner lunch breakfast coffee
food pizza movie game going coming got get go come see talk tell said say
something anything everything nothing good great fine bad hard little bit much
too very more less up down out over back around here there now then when where
why how who which because though still already again never always sometimes
someone anyone everyone friend friends guys dude bro love hate miss hope worry
wrong right sure thing stuff time way make made take took give gave let keep
i think i might have i might've i think i've i feel like i don't know
i fucked up i think i fucked up i might have fucked up a bit too hard
i think i might've fucked up a bit too hard i fucked up really hard today
what happened are you okay do you want to talk about it call me when you can
let me know talk to you later see you soon sounds good that makes sense
`)

const maxDecompressedMessage = 16 << 20

func packMessage(plaintext []byte) ([]byte, error) {
	var compressed bytes.Buffer
	w, err := flate.NewWriterDict(&compressed, flate.BestCompression, chatDictionary)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() < len(plaintext) {
		return append([]byte{1}, compressed.Bytes()...), nil
	}
	return append([]byte{0}, plaintext...), nil
}

func unpackMessage(packed []byte) ([]byte, error) {
	if len(packed) == 0 {
		return nil, errors.New("empty packed message")
	}
	if packed[0] == 0 {
		return append([]byte(nil), packed[1:]...), nil
	}
	if packed[0] != 1 {
		// Compatibility with records created before message packing existed.
		return append([]byte(nil), packed...), nil
	}
	r := flate.NewReaderDict(bytes.NewReader(packed[1:]), chatDictionary)
	defer r.Close()
	decoded, err := io.ReadAll(io.LimitReader(r, maxDecompressedMessage+1))
	if err != nil {
		return nil, fmt.Errorf("decompress message: %w", err)
	}
	if len(decoded) > maxDecompressedMessage {
		return nil, errors.New("decompressed message exceeds size limit")
	}
	return decoded, nil
}
