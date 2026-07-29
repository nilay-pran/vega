package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	in := Frame{
		Type:     TypeChunkData,
		Flags:    FlagLastPart | FlagNeedsAck,
		StreamID: 3,
		Sequence: 42,
		Payload:  []byte("chunk bytes"),
	}
	var buf bytes.Buffer
	if _, err := in.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Version != CurrentVersion || out.Type != in.Type || out.Flags != in.Flags ||
		out.StreamID != in.StreamID || out.Sequence != in.Sequence || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestReadFrameBadMagic(t *testing.T) {
	b := Frame{Type: TypePing}.Encode()
	b[0] = 0x00 // corrupt magic
	if _, err := ReadFrame(bytes.NewReader(b), 0); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("err = %v, want ErrBadMagic", err)
	}
}

func TestReadFrameCorruptHeader(t *testing.T) {
	b := Frame{Type: TypePing, Sequence: 7}.Encode()
	b[10] ^= 0xFF // flip a sequence byte; header CRC must catch it
	if _, err := ReadFrame(bytes.NewReader(b), 0); !errors.Is(err, ErrBadHeaderCRC) {
		t.Fatalf("err = %v, want ErrBadHeaderCRC", err)
	}
}

func TestReadFramePayloadTooLarge(t *testing.T) {
	b := Frame{Type: TypeChunkData, Payload: make([]byte, 100)}.Encode()
	if _, err := ReadFrame(bytes.NewReader(b), 10); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add(Frame{Type: TypeHello, Payload: []byte("hi")}.Encode())
	f.Add([]byte{0x53, 0x4C, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must never panic, regardless of input.
		_, _ = ReadFrame(bytes.NewReader(data), DefaultMaxPayload)
	})
}
