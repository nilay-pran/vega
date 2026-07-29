package common

import "testing"

func TestEventBusDeliversAndUnsubscribes(t *testing.T) {
	b := NewEventBus()
	id, ch := b.Subscribe(4)

	b.Publish(Event{Kind: "progress", UploadID: "up1"})
	got := <-ch
	if got.Kind != "progress" || got.UploadID != "up1" {
		t.Fatalf("unexpected event %+v", got)
	}

	b.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after Unsubscribe")
	}
}

func TestEventBusPublishNeverBlocks(t *testing.T) {
	b := NewEventBus()
	b.Subscribe(1) // buffer of 1, never drained
	// Many publishes must not block even though the subscriber is full.
	for i := 0; i < 1000; i++ {
		b.Publish(Event{Kind: "progress"})
	}
}
