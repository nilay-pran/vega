// Package protocol defines the SLKT wire format: the framed, multiplexed,
// session-oriented custom protocol (docs/ARCHITECTURE.md §5–§6). This package is
// the carrier-independent framing only; the TCP/TLS (and later QUIC) transport
// that moves these frames lives in packages/transport. TLS provides on-the-wire
// confidentiality and integrity; SLKT adds a header CRC to reject corruption
// before trusting the length field, plus application-level chunk checksums.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const (
	Magic             = uint16(0x534C) // "SL": frame sync + fast garbage reject
	CurrentVersion    = uint8(1)
	HeaderSize        = 26              // bytes; see layout below
	DefaultMaxPayload = uint32(8 << 20) // 8 MiB cap bounds parser memory
)

// Header layout (big-endian), 26 bytes:
//
//	off  size  field
//	 0    2    MAGIC (0x534C)
//	 2    1    VERSION
//	 3    1    TYPE
//	 4    2    FLAGS
//	 6    4    STREAM ID   (0 = control; odd = client data; even = server data)
//	10    8    FRAME SEQUENCE (per-session monotonic; replay defense)
//	18    4    PAYLOAD LENGTH
//	22    4    HEADER CRC32C (covers bytes 0..21)
var crc32c = crc32.MakeTable(crc32.Castagnoli)

// Type is the frame type (docs/ARCHITECTURE.md §6.2).
type Type uint8

const (
	TypeHello           Type = 0x01
	TypeHelloAck        Type = 0x02
	TypeAuth            Type = 0x03
	TypeAuthAck         Type = 0x04
	TypeSessionOpen     Type = 0x05
	TypeSessionResume   Type = 0x06
	TypeSessionReady    Type = 0x07
	TypeChunkData       Type = 0x10
	TypeChunkAck        Type = 0x11
	TypeChunkNak        Type = 0x12
	TypeFlowUpdate      Type = 0x20
	TypeBackpressure    Type = 0x21
	TypePing            Type = 0x22
	TypePong            Type = 0x23
	TypeDedupQuery      Type = 0x30
	TypeDedupHit        Type = 0x31
	TypeSessionComplete Type = 0x40
	TypeCompleted       Type = 0x41
	TypeError           Type = 0x50
	TypeGoodbye         Type = 0x51
)

// Flags is the frame flag bit field.
type Flags uint16

const (
	FlagFin        Flags = 1 << 0
	FlagCompressed Flags = 1 << 1
	FlagLastPart   Flags = 1 << 2
	FlagResume     Flags = 1 << 3
	FlagNeedsAck   Flags = 1 << 4
	FlagUrgent     Flags = 1 << 5
)

// Frame is one SLKT message.
type Frame struct {
	Version  uint8
	Type     Type
	Flags    Flags
	StreamID uint32
	Sequence uint64
	Payload  []byte
}

var (
	ErrBadMagic        = errors.New("protocol: bad magic")
	ErrBadHeaderCRC    = errors.New("protocol: header crc mismatch")
	ErrPayloadTooLarge = errors.New("protocol: payload exceeds limit")
)

// Encode serializes the frame (header + payload) into a new byte slice.
func (f Frame) Encode() []byte {
	version := f.Version
	if version == 0 {
		version = CurrentVersion
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint16(buf[0:], Magic)
	buf[2] = version
	buf[3] = byte(f.Type)
	binary.BigEndian.PutUint16(buf[4:], uint16(f.Flags))
	binary.BigEndian.PutUint32(buf[6:], f.StreamID)
	binary.BigEndian.PutUint64(buf[10:], f.Sequence)
	binary.BigEndian.PutUint32(buf[18:], uint32(len(f.Payload)))
	binary.BigEndian.PutUint32(buf[22:], crc32.Checksum(buf[0:22], crc32c))
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// WriteTo writes the encoded frame to w.
func (f Frame) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(f.Encode())
	return int64(n), err
}

// ReadFrame reads one frame from r, rejecting corruption and oversized payloads.
// maxPayload of 0 means DefaultMaxPayload.
func ReadFrame(r io.Reader, maxPayload uint32) (Frame, error) {
	if maxPayload == 0 {
		maxPayload = DefaultMaxPayload
	}
	var head [HeaderSize]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Frame{}, err
	}
	if binary.BigEndian.Uint16(head[0:]) != Magic {
		return Frame{}, ErrBadMagic
	}
	if binary.BigEndian.Uint32(head[22:]) != crc32.Checksum(head[0:22], crc32c) {
		return Frame{}, ErrBadHeaderCRC
	}
	length := binary.BigEndian.Uint32(head[18:])
	if length > maxPayload {
		return Frame{}, fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, length, maxPayload)
	}
	f := Frame{
		Version:  head[2],
		Type:     Type(head[3]),
		Flags:    Flags(binary.BigEndian.Uint16(head[4:])),
		StreamID: binary.BigEndian.Uint32(head[6:]),
		Sequence: binary.BigEndian.Uint64(head[10:]),
	}
	if length > 0 {
		f.Payload = make([]byte, length)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}
