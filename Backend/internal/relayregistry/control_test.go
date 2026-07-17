package relayregistry

import (
	"testing"
	"time"
)

func TestControlHubTargetsNodeAndUnregisters(t *testing.T) {
	hub := NewControlHub()
	first := hub.Register("relay_a")
	second := hub.Register("relay_b")
	defer second.Close()
	hub.Publish("relay_a", ControlMessage{Type: "EnterDrain"})
	select {
	case message := <-first.Events():
		if message.Type != "EnterDrain" {
			t.Fatalf("message = %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("targeted control message was not delivered")
	}
	select {
	case message := <-second.Events():
		t.Fatalf("message leaked to another node: %#v", message)
	default:
	}
	first.Close()
	hub.Publish("relay_a", ControlMessage{Type: "Shutdown"})
	if _, exists := hub.subscribers["relay_a"]; exists {
		t.Fatal("closed subscription remained registered")
	}
}
