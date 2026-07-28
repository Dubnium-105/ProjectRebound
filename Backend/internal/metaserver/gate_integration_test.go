package metaserver

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestGateTicketIsAtomicallySingleUse(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("TEST_REDIS_ADDRESS is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	store := NewGateStore(client, time.Minute)
	ctx := context.Background()
	ticket, err := store.Issue(ctx, GateSession{
		PlayerID: "player_gate_test", AuthSessionID: "session_gate_test",
		ClientVersion: "test", ProtocolVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.Consume(ctx, ticket); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful concurrent consumers = %d, want 1", successes.Load())
	}
	if _, err := store.Consume(ctx, ticket); err != ErrGateTicketInvalid {
		t.Fatalf("replay error = %v, want %v", err, ErrGateTicketInvalid)
	}
}
