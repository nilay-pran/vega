package slkt

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"sync"
	"time"

	"code.sli.ke/go/vega/packages/protocol"
	"code.sli.ke/go/vega/packages/storage"
)

// defaultPaceBytesPerSec bounds the client's *aggregate* send rate across all
// concurrent parts, so the sender never overruns the receive path. This is a
// fixed-rate placeholder; adaptive, RTT-driven pacing (BBR-style) is the P4 work
// in docs/ARCHITECTURE.md §5.4.
const defaultPaceBytesPerSec = 64 << 20 // 64 MiB/s

// pollGrace lets in-flight datagrams land before we ask the server what is still
// missing, so we don't NAK packets that were merely still in transit.
const pollGrace = 10 * time.Millisecond

// pacer is a shared token-bucket rate limiter. One pacer serves the whole client
// so N workers together stay within the target rate. It gates by holding the
// lock across the (short) sleep, which naturally serializes bursts.
type pacer struct {
	mu     sync.Mutex
	rate   float64 // bytes/sec; <=0 disables pacing
	burst  float64 // max accumulated tokens (bytes)
	tokens float64
	last   time.Time
}

func newPacer(bytesPerSec float64) *pacer {
	return &pacer{rate: bytesPerSec, burst: bytesPerSec * 0.002} // ~2 ms burst
}

func (p *pacer) send(n int) {
	if p.rate <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.last.IsZero() {
		p.last = now
	}
	p.tokens = min(p.burst, p.tokens+p.rate*now.Sub(p.last).Seconds())
	p.last = now
	p.tokens -= float64(n)
	if p.tokens < 0 {
		time.Sleep(time.Duration(-p.tokens / p.rate * float64(time.Second)))
		p.tokens = 0
		p.last = time.Now()
	}
}

// maxRetransmitRounds bounds the NAK/retransmit loop so a black-holed path fails
// fast instead of looping forever. Each round retransmits only the gaps the
// server reported still missing.
const maxRetransmitRounds = 16

// Client is the SLKT client. It implements storage.ObjectStore by sending data
// over UDP and control/NAK feedback over TCP, both to addr (same host:port for
// both protocols). It holds no per-upload state: each call uses its own short
// connections, so the engine's worker pool drives many parts concurrently.
type Client struct {
	addr    string
	pace    *pacer
	version uint8 // SLKT protocol version advertised on every control message

	// dropPacket, when set, drops a UDP datagram before sending it. Test-only
	// hook to simulate loss; nil in production.
	dropPacket func(offset int64) bool
}

var _ storage.ObjectStore = (*Client)(nil)

func NewClient(addr string) *Client {
	return &Client{addr: addr, pace: newPacer(defaultPaceBytesPerSec), version: protocol.CurrentVersion}
}

// Probe checks the SLKT server is reachable (used by the transport chooser). A
// blocked custom port — the enterprise-firewall case — fails here and the
// selector falls back to the direct/HTTP transport.
func (c *Client) Probe(ctx context.Context) error {
	_, err := c.rpc(ctx, ctrlMsg{Op: "ping"})
	return err
}

func (c *Client) InitMultipart(ctx context.Context, key string) (string, error) {
	resp, err := c.rpc(ctx, ctrlMsg{Op: "init", Key: key})
	if err != nil {
		return "", err
	}
	return resp.MultipartID, nil
}

func (c *Client) CompleteMultipart(ctx context.Context, key, uploadID string, parts []storage.Part) error {
	refs := make([]partRef, len(parts))
	for i, p := range parts {
		refs[i] = partRef{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	_, err := c.rpc(ctx, ctrlMsg{Op: "complete", Key: key, MultipartID: uploadID, Parts: refs})
	return err
}

func (c *Client) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := c.rpc(ctx, ctrlMsg{Op: "abort", Key: key, MultipartID: uploadID})
	return err
}

// UploadPart streams a part's bytes over UDP and drives the reliable NAK loop
// over TCP until the server acknowledges the whole part.
func (c *Client) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (storage.Part, error) {
	data, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return storage.Part{}, err
	}
	if int64(len(data)) != size {
		return storage.Part{}, fmt.Errorf("slkt: short read: got %d want %d", len(data), size)
	}

	tcp, err := c.dialTCP(ctx)
	if err != nil {
		return storage.Part{}, err
	}
	defer tcp.Close()
	udp, err := net.Dial("udp", c.addr)
	if err != nil {
		return storage.Part{}, err
	}
	defer udp.Close()
	if uc, ok := udp.(*net.UDPConn); ok {
		_ = uc.SetWriteBuffer(16 << 20)
	}

	if err := writeMsg(tcp, ctrlMsg{Op: "begin", Version: c.version, Key: key, MultipartID: uploadID, PartNumber: partNumber, Size: size}); err != nil {
		return storage.Part{}, err
	}
	begun, err := readMsg(tcp)
	if err != nil {
		return storage.Part{}, err
	}
	if begun.Err != "" {
		return storage.Part{}, fmt.Errorf("slkt begin: %s", begun.Err)
	}
	tid := begun.TransferID
	crc := crc32.Checksum(data, crc32cTable)

	// First pass: send everything.
	var seq uint64
	c.sendRange(udp, tid, data, 0, size, &seq)

	// Reliable loop: prompt the server, retransmit the gaps it reports.
	for round := 0; round < maxRetransmitRounds; round++ {
		time.Sleep(pollGrace) // let in-flight datagrams arrive before polling
		if err := writeMsg(tcp, ctrlMsg{Op: "end", Version: c.version, TransferID: tid, CRC32C: crc}); err != nil {
			return storage.Part{}, err
		}
		resp, err := readMsg(tcp)
		if err != nil {
			return storage.Part{}, err
		}
		if resp.Err != "" {
			return storage.Part{}, fmt.Errorf("slkt end: %s", resp.Err)
		}
		if resp.OK {
			return storage.Part{PartNumber: partNumber, ETag: resp.ETag}, nil
		}
		for _, mr := range resp.Missing {
			c.sendRange(udp, tid, data, mr.Off, mr.Len, &seq)
		}
	}
	return storage.Part{}, fmt.Errorf("slkt: part %d not acknowledged after %d rounds", partNumber, maxRetransmitRounds)
}

func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	tcp, err := c.dialTCP(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeMsg(tcp, ctrlMsg{Op: "get", Version: c.version, Key: key}); err != nil {
		tcp.Close()
		return nil, err
	}
	resp, err := readMsg(tcp)
	if err != nil {
		tcp.Close()
		return nil, err
	}
	if resp.Err != "" {
		tcp.Close()
		return nil, storage.ErrNoSuchKey
	}
	return connReader{Reader: io.LimitReader(tcp, resp.Size), conn: tcp}, nil
}

// sendRange segments data[off:off+ln) into MTU-sized UDP datagrams, paced by the
// client's shared rate limiter.
func (c *Client) sendRange(udp net.Conn, tid uint32, data []byte, off, ln int64, seq *uint64) {
	end := off + ln
	for p := off; p < end; p += dataPerPacket {
		q := min(p+dataPerPacket, end)
		*seq++
		if c.dropPacket != nil && c.dropPacket(p) {
			continue // simulated loss; server will NAK this gap
		}
		pkt := buildDataPacket(tid, *seq, p, data[p:q])
		_, _ = udp.Write(pkt)
		c.pace.send(len(pkt))
	}
}

func (c *Client) rpc(ctx context.Context, req ctrlMsg) (ctrlMsg, error) {
	req.Version = c.version
	conn, err := c.dialTCP(ctx)
	if err != nil {
		return ctrlMsg{}, err
	}
	defer conn.Close()
	if err := writeMsg(conn, req); err != nil {
		return ctrlMsg{}, err
	}
	resp, err := readMsg(conn)
	if err != nil {
		return ctrlMsg{}, err
	}
	if resp.Err != "" {
		return ctrlMsg{}, fmt.Errorf("slkt %s: %s", req.Op, resp.Err)
	}
	return resp, nil
}

func (c *Client) dialTCP(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", c.addr)
}

type connReader struct {
	io.Reader
	conn net.Conn
}

func (r connReader) Close() error { return r.conn.Close() }
