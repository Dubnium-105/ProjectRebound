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

func TestTelemetryStoreOrdersReportsAndAcceptsProcessReset(t *testing.T) {
	store := NewTelemetryStore()
	base := map[string]any{
		"process_id": "process-a", "sequence": "2",
		"packets_received_total": "10", "packets_forwarded_total": "9",
		"packets_dropped_total": "1", "bytes_forwarded_total": "1000",
		"bind_success_total": "3", "bind_failed_total": "1",
		"token_invalid_total": "2", "rate_limit_drops_total": "4",
		"control_reconnects_total": "5",
	}
	if err := store.Record("relay_a", base, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	stale := make(map[string]any, len(base))
	for key, value := range base {
		stale[key] = value
	}
	stale["sequence"] = "1"
	stale["packets_received_total"] = "99"
	if err := store.Record("relay_a", stale, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot()["relay_a"]; got.Sequence != 2 || got.PacketsReceived != 10 {
		t.Fatalf("stale report replaced current telemetry: %#v", got)
	}
	reset := stale
	reset["process_id"] = "process-b"
	reset["packets_received_total"] = "1"
	if err := store.Record("relay_a", reset, time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot()["relay_a"]; got.ProcessID != "process-b" || got.PacketsReceived != 1 {
		t.Fatalf("new process report was not accepted: %#v", got)
	}
}

func TestTelemetryStoreRejectsImpreciseOrMissingCounters(t *testing.T) {
	store := NewTelemetryStore()
	if err := store.Record("relay_a", map[string]any{"process_id": "p", "sequence": "1"}, time.Now()); err == nil {
		t.Fatal("incomplete telemetry report was accepted")
	}
}
