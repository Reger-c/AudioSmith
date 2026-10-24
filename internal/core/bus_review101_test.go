package core

import "testing"

type pingEvent struct{}

func (pingEvent) Topic() string { return "test.ping" }

// #101 L11: a subscriber that panics does not crash the publisher, nor
// keep the event from the other subscribers.
func TestBusSurvivesPanickingSubscriber(t *testing.T) {
	b := NewBus()
	got := 0
	b.Subscribe(func(Event) { panic("boom") })
	b.Subscribe(func(Event) { got++ })
	b.Publish(pingEvent{})
	b.Publish(pingEvent{})
	if got != 2 {
		t.Fatalf("the other subscriber got %d events", got)
	}
}
