package connection

import "testing"

func TestHubRoutesEventsOnlyToTargetPlayers(t *testing.T) {
	hub := NewHub(2)
	host := hub.Subscribe("host")
	defer host.Close()
	peer := hub.Subscribe("peer")
	defer peer.Close()
	other := hub.Subscribe("other")
	defer other.Close()
	event := Event{Type: "connection.created"}
	hub.Publish([]string{"host", "peer", "host"}, event)
	for name, subscription := range map[string]*Subscription{"host": host, "peer": peer} {
		select {
		case received := <-subscription.Events():
			if received.Type != event.Type {
				t.Fatalf("%s event = %#v", name, received)
			}
		default:
			t.Fatalf("%s did not receive event", name)
		}
	}
	select {
	case received := <-other.Events():
		t.Fatalf("unrelated player received %#v", received)
	default:
	}
}
