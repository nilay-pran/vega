// Package slkt is the SLKT transport: a hybrid UDP + TCP design, the same shape
// as Aspera FASP / UDT.
//
//   - Bulk DATA travels over UDP. A single UDP flow is not throttled by TCP's
//     congestion control, so it sustains throughput across high bandwidth-delay,
//     lossy WAN links where a single TCP stream collapses.
//   - CONTROL and reliability feedback travel over a reliable TCP connection:
//     session/part setup, completion, and — crucially — aggregated *selective
//     NAKs* (the gaps still missing), not per-packet ACKs. Gap-based feedback is
//     what keeps the reliable channel cheap; per-packet ACKs over TCP would add
//     latency and defeat the purpose.
//
// The client implements storage.ObjectStore, so the upload engine uses it
// unchanged — SLKT is just another object-store adapter alongside HTTPStore and
// the direct-to-Spaces path (docs/ARCHITECTURE.md §5, §13).
//
// v1 scope: reliable UDP with NAK-driven retransmit, header + payload integrity,
// and a bounded retransmit loop. Congestion control / BBR-style pacing and
// disk-backed reassembly for multi-GB parts are follow-ons; until then a part is
// buffered in memory on both ends, so adaptive chunk sizing should cap SLKT
// parts to a memory-safe size.
package slkt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"code.sli.ke/go/vega/packages/protocol"
)

// dataPerPacket keeps each UDP datagram comfortably under a typical path MTU to
// avoid IP fragmentation (26-byte frame header + 8-byte offset + this payload).
const dataPerPacket = 1200

const maxControlMsg = 1 << 20 // 1 MiB cap on a control message

var (
	errControlTooBig = errors.New("slkt: control message too large")
	errShortPacket   = errors.New("slkt: short data packet")
)

// ctrlMsg is one control message on the TCP channel (JSON, length-prefixed).
// The same struct is used for requests and responses; unused fields stay zero.
type ctrlMsg struct {
	Op          string      `json:"op"`
	Version     uint8       `json:"v,omitempty"` // SLKT protocol version; server rejects mismatches
	Key         string      `json:"key,omitempty"`
	MultipartID string      `json:"multipartId,omitempty"`
	PartNumber  int         `json:"partNumber,omitempty"`
	Size        int64       `json:"size,omitempty"`
	TransferID  uint32      `json:"transferId,omitempty"`
	CRC32C      uint32      `json:"crc32c,omitempty"`
	ETag        string      `json:"etag,omitempty"`
	Parts       []partRef   `json:"parts,omitempty"`
	Missing     []byteRange `json:"missing,omitempty"`
	OK          bool        `json:"ok,omitempty"`
	Err         string      `json:"err,omitempty"`
}

type partRef struct {
	PartNumber int    `json:"n"`
	ETag       string `json:"e"`
}

type byteRange struct {
	Off int64 `json:"o"`
	Len int64 `json:"l"`
}

func writeMsg(w io.Writer, m ctrlMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func readMsg(r io.Reader) (ctrlMsg, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return ctrlMsg{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxControlMsg {
		return ctrlMsg{}, errControlTooBig
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return ctrlMsg{}, err
	}
	var m ctrlMsg
	return m, json.Unmarshal(buf, &m)
}

// buildDataPacket wraps a data segment in a SLKT frame for UDP. The frame's
// header CRC rejects corrupt headers; payload integrity is verified per-part via
// CRC32C over the reassembled buffer (see server), and end-to-end via the whole
// file SHA256 the engine checks after assembly.
func buildDataPacket(transferID uint32, seq uint64, offset int64, data []byte) []byte {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(payload[:8], uint64(offset))
	copy(payload[8:], data)
	return protocol.Frame{
		Type:     protocol.TypeChunkData,
		StreamID: transferID,
		Sequence: seq,
		Payload:  payload,
	}.Encode()
}

func parseDataPacket(b []byte) (transferID uint32, offset int64, data []byte, err error) {
	f, err := protocol.ReadFrame(bytes.NewReader(b), protocol.DefaultMaxPayload)
	if err != nil {
		return 0, 0, nil, err
	}
	if len(f.Payload) < 8 {
		return 0, 0, nil, errShortPacket
	}
	return f.StreamID, int64(binary.BigEndian.Uint64(f.Payload[:8])), f.Payload[8:], nil
}
