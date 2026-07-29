package common

import "sync"

// Event is a fact the engine publishes for controllers (the UI, the CLI, logs)
// to observe. It is best-effort telemetry, never a command channel.
type Event struct {
	Kind     string         // e.g. "upload.created", "progress", "upload.completed"
	UploadID UploadID       // "" for engine-wide events
	At       int64          // unix seconds
	Fields   map[string]any // small, JSON-friendly payload
}

// EventBus is a tiny in-process pub/sub. Publish never blocks: a subscriber with
// a full buffer simply misses events, so a slow or dead UI can never stall the
// upload engine.
type EventBus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

func NewEventBus() *EventBus { return &EventBus{subs: make(map[int]chan Event)} }

// Subscribe returns a handle and a buffered channel of events. Call Unsubscribe
// with the handle when done.
func (b *EventBus) Subscribe(buffer int) (int, <-chan Event) {
	if buffer < 1 {
		buffer = 64
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Event, buffer)
	b.subs[id] = ch
	return id, ch
}

func (b *EventBus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// Publish delivers to every subscriber that has room, dropping for those that do not.
func (b *EventBus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber is behind; drop rather than block the engine
		}
	}
}
