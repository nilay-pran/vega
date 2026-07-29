// Package bench holds a throwaway link emulator and comparative benchmark for
// the SLKT transport versus HTTP/3 (QUIC). It is test-only; nothing in the
// shipping binaries imports it.
package bench

import (
	"math/rand"
	"net"
	"sync"
	"time"
)

// relay is a single-client UDP path emulator. It forwards datagrams between a
// client-facing socket and an upstream server socket, dropping a fraction
// (loss) and delaying survivors by a one-way latency (RTT ≈ 2·delay).
//
// It is a real socket relay rather than a PacketConn wrapper on purpose: both
// the client and the server keep their own real *net.UDPConn, so read
// deadlines, timers, and batch I/O all behave normally. quic-go's event loop
// depends on read deadlines to fire its timers, so a wrapper that intercepts
// ReadFrom would stall QUIC under any latency and make the comparison invalid.
//
// Delivery is order-preserving. An earlier version scheduled each datagram with
// its own time.AfterFunc, so survivors were delivered by independent goroutines
// and raced each other to WriteTo — the client saw heavy reordering even at 0%
// loss, which QUIC's loss detector read as loss and pinned cwnd at the minimum
// (~0.4 MiB/s). A single delivery goroutine per direction, fed an in-order
// queue, keeps datagrams FIFO (the per-direction delay is constant, so arrival
// order equals send order) and models a plain propagation-delay link.
type relay struct {
	clientConn net.PacketConn // faces the client (QUIC/SLKT dials this addr)
	upstream   net.PacketConn // faces the server
	serverAddr net.Addr
	loss       float64
	delay      time.Duration

	mu         sync.Mutex
	clientAddr net.Addr
	rng        *rand.Rand
	closed     chan struct{}
	once       sync.Once
}

// timed is one datagram awaiting delivery at a fixed wall-clock instant.
type timed struct {
	b   []byte
	dst net.Addr
	at  time.Time
}

// startRelay begins forwarding to serverAddr and returns the host:port clients
// should dial and a stop function.
func startRelay(serverAddr net.Addr, loss float64, delay time.Duration, seed int64) (string, func(), error) {
	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return "", nil, err
	}
	r := &relay{
		clientConn: client,
		upstream:   upstream,
		serverAddr: serverAddr,
		loss:       loss,
		delay:      delay,
		rng:        rand.New(rand.NewSource(seed)),
		closed:     make(chan struct{}),
	}
	// One queue + one sender goroutine per direction preserves order.
	toServer := make(chan timed, 8192)
	toClient := make(chan timed, 8192)
	go r.deliver(r.upstream, toServer)   // client -> server
	go r.deliver(r.clientConn, toClient) // server -> client
	go r.pump(r.clientConn, true, toServer)
	go r.pump(r.upstream, false, toClient)
	stop := func() { r.once.Do(func() { close(r.closed) }); _ = client.Close(); _ = upstream.Close() }
	return r.clientConn.LocalAddr().String(), stop, nil
}

func (r *relay) drop() bool {
	if r.loss <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rng.Float64() < r.loss
}

// pump reads one side, applies loss, and enqueues survivors (stamped with a
// delivery time) onto the direction's queue. When fromClient it records the
// client's address (for the return path) and targets the fixed server address;
// otherwise it targets the recorded client address.
func (r *relay) pump(in net.PacketConn, fromClient bool, out chan<- timed) {
	buf := make([]byte, 64<<10)
	for {
		n, addr, err := in.ReadFrom(buf)
		if err != nil {
			return
		}
		if r.drop() {
			continue
		}
		b := make([]byte, n)
		copy(b, buf[:n])

		var dst net.Addr
		if fromClient {
			r.mu.Lock()
			r.clientAddr = addr
			r.mu.Unlock()
			dst = r.serverAddr
		} else {
			r.mu.Lock()
			dst = r.clientAddr
			r.mu.Unlock()
			if dst == nil {
				continue
			}
		}

		msg := timed{b: b, dst: dst, at: time.Now().Add(r.delay)}
		select {
		case out <- msg:
		case <-r.closed:
			return
		}
	}
}

// deliver drains one direction's queue in order, sleeping until each datagram's
// delivery instant before writing it. A single goroutine per direction means no
// two WriteTo calls race, so datagrams leave in the order they arrived.
func (r *relay) deliver(out net.PacketConn, in <-chan timed) {
	for {
		select {
		case msg := <-in:
			if d := time.Until(msg.at); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-r.closed:
					t.Stop()
					return
				}
			}
			_, _ = out.WriteTo(msg.b, msg.dst)
		case <-r.closed:
			return
		}
	}
}
