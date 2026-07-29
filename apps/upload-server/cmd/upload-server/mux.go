package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// One TCP socket serves both HTTPS (the JSON API + multipart uploads) and SLKT
// control, told apart by the first byte of the connection: a TLS ClientHello
// always starts with the handshake record type 0x16, while an SLKT control
// message starts with a 4-byte big-endian length whose high byte is 0x00 (the
// message is capped at 1 MiB). No other protocol shares the port, so one byte
// disambiguates. SLKT bulk data rides UDP on the same port and needs no demux.
const (
	recordTypeTLSHandshake = 0x16
	detectTimeout          = 10 * time.Second // cap how long a silent client may stall detection
)

// muxListener is a net.Listener the demuxer feeds over a channel, so an
// http.Server and the SLKT server each keep a standard Accept loop while a
// single underlying socket backs both.
type muxListener struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMuxListener(addr net.Addr) *muxListener {
	return &muxListener{addr: addr, conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *muxListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *muxListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *muxListener) Addr() net.Addr { return l.addr }

// deliver hands a routed conn to the listener, dropping it if the listener has
// already closed so a late detection never blocks forever.
func (l *muxListener) deliver(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.closed:
		_ = c.Close()
	}
}

// prefixConn re-prepends the bytes read during protocol detection so the
// downstream server sees the whole stream from its first byte.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// demux accepts on base and routes each connection to the HTTPS or SLKT listener
// by peeking its first byte. It runs until base fails (e.g. is closed), then
// closes both derived listeners so their servers unwind.
func demux(base net.Listener, httpLn, slktLn *muxListener, log *slog.Logger) {
	defer httpLn.Close()
	defer slktLn.Close()
	for {
		c, err := base.Accept()
		if err != nil {
			return
		}
		go route(c, httpLn, slktLn, log)
	}
}

func route(c net.Conn, httpLn, slktLn *muxListener, log *slog.Logger) {
	_ = c.SetReadDeadline(time.Now().Add(detectTimeout))
	var first [1]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		log.Debug("mux: detection read failed", "err", err)
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{}) // clear; the server owns deadlines from here

	pc := &prefixConn{Conn: c, r: io.MultiReader(bytes.NewReader(first[:]), c)}
	if first[0] == recordTypeTLSHandshake {
		httpLn.deliver(pc)
	} else {
		slktLn.deliver(pc)
	}
}
