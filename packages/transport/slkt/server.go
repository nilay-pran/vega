package slkt

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"code.sli.ke/go/vega/packages/protocol"
	"code.sli.ke/go/vega/packages/storage"
)

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// Server terminates SLKT: it accepts control on TCP and data on UDP, reassembles
// each part, and writes it to the backing ObjectStore. It is a second inbound
// adapter over the same storage core the HTTP server uses.
type Server struct {
	objects storage.ObjectStore
	log     *slog.Logger

	mu        sync.Mutex
	transfers map[uint32]*transfer
	nextID    atomic.Uint32
}

// transfer reassembles one part. Every data packet is dataPerPacket-aligned, so
// receipt is tracked as a per-packet bitset — O(1) per packet, which keeps the
// single UDP reader draining fast enough to avoid socket-buffer overrun.
type transfer struct {
	mu         sync.Mutex
	key        string
	multipart  string
	partNumber int
	size       int64
	buf        []byte
	got        []bool
	nPackets   int
	sealed     bool // once true, buf is frozen for the storage write; no more records
}

func newTransfer(key, multipart string, partNumber int, size int64) *transfer {
	n := int((size + dataPerPacket - 1) / dataPerPacket)
	if n == 0 {
		n = 1 // a zero-length part is one (empty) packet
	}
	return &transfer{key: key, multipart: multipart, partNumber: partNumber, size: size, buf: make([]byte, size), got: make([]bool, n), nPackets: n}
}

// record marks the packet at a dataPerPacket-aligned offset as received.
func (t *transfer) record(off int64, data []byte) {
	if t.sealed || off < 0 || off%dataPerPacket != 0 || off+int64(len(data)) > t.size {
		return
	}
	idx := int(off / dataPerPacket)
	if idx >= t.nPackets {
		return
	}
	copy(t.buf[off:], data)
	t.got[idx] = true
}

// missingRanges coalesces not-yet-received packets into byte ranges (selective NAK).
func (t *transfer) missingRanges() []byteRange {
	var miss []byteRange
	for i := 0; i < t.nPackets; {
		if t.got[i] {
			i++
			continue
		}
		start := i
		for i < t.nPackets && !t.got[i] {
			i++
		}
		off := int64(start) * dataPerPacket
		end := min(int64(i)*dataPerPacket, t.size)
		miss = append(miss, byteRange{Off: off, Len: end - off})
	}
	return miss
}

func NewServer(objects storage.ObjectStore, log *slog.Logger) *Server {
	return &Server{objects: objects, log: log, transfers: make(map[uint32]*transfer)}
}

// Serve accepts control connections on tcpLn and data packets on udp until ctx
// is done. Both listeners are supplied by the caller so the app owns lifecycle.
func (s *Server) Serve(ctx context.Context, tcpLn net.Listener, udp net.PacketConn) error {
	go s.readUDP(udp)
	go func() {
		<-ctx.Done()
		_ = tcpLn.Close()
		_ = udp.Close()
	}()
	for {
		conn, err := tcpLn.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleControl(ctx, conn)
	}
}

func (s *Server) readUDP(udp net.PacketConn) {
	// A large receive buffer absorbs bursts from parallel senders so datagrams
	// are not dropped by the kernel before the reader drains them (§16 tuning).
	if uc, ok := udp.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(16 << 20)
	}
	buf := make([]byte, 64<<10)
	for {
		n, _, err := udp.ReadFrom(buf)
		if err != nil {
			return
		}
		tid, off, data, err := parseDataPacket(buf[:n])
		if err != nil {
			continue // drop malformed datagram; NAK will ask again
		}
		s.mu.Lock()
		t := s.transfers[tid]
		s.mu.Unlock()
		if t == nil {
			continue
		}
		t.mu.Lock()
		t.record(off, data)
		t.mu.Unlock()
	}
}

func (s *Server) handleControl(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	for {
		m, err := readMsg(conn)
		if err != nil {
			return // EOF or closed
		}
		// Protocol version negotiation: reject a client speaking a version we do
		// not implement before acting on the message (docs/ARCHITECTURE.md §14).
		if m.Version != 0 && m.Version != protocol.CurrentVersion {
			_ = writeMsg(conn, ctrlMsg{Err: fmt.Sprintf("unsupported slkt version %d (want %d)", m.Version, protocol.CurrentVersion)})
			return
		}
		if done := s.dispatch(ctx, conn, m); done {
			return
		}
	}
}

// dispatch handles one control message. It returns true when the connection
// should close afterwards (get streams its body and ends the conn).
func (s *Server) dispatch(ctx context.Context, conn net.Conn, m ctrlMsg) bool {
	switch m.Op {
	case "ping":
		_ = writeMsg(conn, ctrlMsg{OK: true})

	case "init":
		mp, err := s.objects.InitMultipart(ctx, m.Key)
		reply(conn, mp, err)

	case "begin":
		id := s.nextID.Add(1)
		s.mu.Lock()
		s.transfers[id] = newTransfer(m.Key, m.MultipartID, m.PartNumber, m.Size)
		s.mu.Unlock()
		_ = writeMsg(conn, ctrlMsg{OK: true, TransferID: id})

	case "end":
		s.handleEnd(ctx, conn, m)

	case "complete":
		parts := make([]storage.Part, len(m.Parts))
		for i, p := range m.Parts {
			parts[i] = storage.Part{PartNumber: p.PartNumber, ETag: p.ETag}
		}
		reply(conn, "", s.objects.CompleteMultipart(ctx, m.Key, m.MultipartID, parts))

	case "abort":
		reply(conn, "", s.objects.AbortMultipart(ctx, m.Key, m.MultipartID))

	case "get":
		s.handleGet(ctx, conn, m)
		return true

	default:
		_ = writeMsg(conn, ctrlMsg{Err: "unknown op"})
	}
	return false
}

func (s *Server) handleEnd(ctx context.Context, conn net.Conn, m ctrlMsg) {
	s.mu.Lock()
	t := s.transfers[m.TransferID]
	s.mu.Unlock()
	if t == nil {
		_ = writeMsg(conn, ctrlMsg{Err: "unknown transfer"})
		return
	}

	// Evaluate under the transfer lock and, if complete, seal the buffer so no
	// late/duplicate datagram writes it while we stream it to storage. Sealing
	// (rather than holding the lock across UploadPart) keeps the single UDP
	// reader from head-of-line blocking on a slow storage write.
	t.mu.Lock()
	if miss := t.missingRanges(); len(miss) > 0 {
		t.mu.Unlock()
		_ = writeMsg(conn, ctrlMsg{Missing: miss}) // selective NAK
		return
	}
	if crc32.Checksum(t.buf, crc32cTable) != m.CRC32C {
		for i := range t.got {
			t.got[i] = false // integrity failure → force a full retransmit
		}
		t.mu.Unlock()
		_ = writeMsg(conn, ctrlMsg{Missing: []byteRange{{Off: 0, Len: t.size}}})
		return
	}
	t.sealed = true
	t.mu.Unlock()

	part, err := s.objects.UploadPart(ctx, t.key, t.multipart, t.partNumber, bytes.NewReader(t.buf), t.size)
	if err != nil {
		reply(conn, "", err)
		return
	}
	s.mu.Lock()
	delete(s.transfers, m.TransferID)
	s.mu.Unlock()
	_ = writeMsg(conn, ctrlMsg{OK: true, ETag: part.ETag})
}

func (s *Server) handleGet(ctx context.Context, conn net.Conn, m ctrlMsg) {
	rc, err := s.objects.Get(ctx, m.Key)
	if err != nil {
		_ = writeMsg(conn, ctrlMsg{Err: err.Error()})
		return
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		_ = writeMsg(conn, ctrlMsg{Err: err.Error()})
		return
	}
	if err := writeMsg(conn, ctrlMsg{OK: true, Size: int64(len(body))}); err != nil {
		return
	}
	_, _ = conn.Write(body)
}

func reply(conn net.Conn, multipartID string, err error) {
	if err != nil {
		_ = writeMsg(conn, ctrlMsg{Err: err.Error()})
		return
	}
	_ = writeMsg(conn, ctrlMsg{OK: true, MultipartID: multipartID})
}
