package events

import "testing"

func TestHub_PublishDeliversToSubscriber(t *testing.T) {
	h := NewHub(4)
	sub := h.Subscribe("vehicle-1")
	h.Publish(Envelope{VehicleID: "vehicle-1", Revision: 1})
	select {
	case env := <-sub.Envelopes():
		if env.Revision != 1 {
			t.Fatalf("expected revision 1, got %d", env.Revision)
		}
	default:
		t.Fatal("expected envelope to be delivered")
	}
}

func TestHub_PublishIgnoresOtherVehicles(t *testing.T) {
	h := NewHub(4)
	sub := h.Subscribe("vehicle-1")
	h.Publish(Envelope{VehicleID: "vehicle-2", Revision: 1})
	select {
	case <-sub.Envelopes():
		t.Fatal("did not expect delivery for a different vehicle")
	default:
	}
}

func TestHub_OverflowClosesSocket(t *testing.T) {
	h := NewHub(1)
	sub := h.Subscribe("vehicle-1")
	h.Publish(Envelope{VehicleID: "vehicle-1", Revision: 1}) // fills the queue of capacity 1
	h.Publish(Envelope{VehicleID: "vehicle-1", Revision: 2}) // overflows

	// Channel should be closed and sub marked overflowed.
	<-sub.Envelopes() // revision 1, still buffered
	_, ok := <-sub.Envelopes()
	if ok {
		t.Fatal("expected channel closed after overflow")
	}
	if !sub.Overflowed() {
		t.Fatal("expected subscriber marked overflowed")
	}
}

func TestHub_UnsubscribeClosesChannel(t *testing.T) {
	h := NewHub(4)
	sub := h.Subscribe("vehicle-1")
	h.Unsubscribe(sub)
	_, ok := <-sub.Envelopes()
	if ok {
		t.Fatal("expected channel closed after unsubscribe")
	}
}
