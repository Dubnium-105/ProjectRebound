package metaserver

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Provision the dedicated test role with deployments/control-plane/
// provision-meta-postgres.sh and the Redis user with the Compose ACL first.
// Administrator connections cannot establish that deployment permissions work.
func TestRestrictedMetaServicePermissions(t *testing.T) {
	databaseURL := os.Getenv("TEST_META_DATABASE_URL")
	redisAddress := os.Getenv("TEST_META_REDIS_ADDRESS")
	redisUsername := os.Getenv("TEST_META_REDIS_USERNAME")
	redisPassword := os.Getenv("TEST_META_REDIS_PASSWORD")
	if databaseURL == "" || redisAddress == "" || redisUsername == "" || redisPassword == "" {
		t.Skip("restricted TEST_META_DATABASE_URL and TEST_META_REDIS_* fixtures are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.NewMigrator(pool).VerifyCurrent(ctx); err != nil {
		t.Fatalf("restricted MetaServer schema verification: %v", err)
	}
	t.Log("restricted schema version/name/checksum verification succeeded")
	var elevated bool
	if err := pool.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole
		FROM pg_roles WHERE rolname = current_user`).Scan(&elevated); err != nil || elevated {
		t.Fatalf("MetaServer role is not a restricted service role: elevated=%t error=%v", elevated, err)
	}
	for _, query := range []string{
		`SELECT refresh_token_hash FROM auth_sessions LIMIT 0`,
		`SELECT password_hash FROM admin_users LIMIT 0`,
		`SELECT id FROM match_lobbies LIMIT 0`,
		`UPDATE schema_migrations SET checksum = checksum WHERE false`,
		`CREATE TABLE public.rebound_meta_forbidden_ddl_probe (id integer)`,
	} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, queryErr := tx.Exec(ctx, query)
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(queryErr, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("restricted query must fail with insufficient privilege: query=%q error=%v", query, queryErr)
		}
		t.Logf("denied restricted SQL with SQLSTATE 42501: %s", query)
	}

	client := redis.NewClient(&redis.Options{
		Addr: redisAddress, Username: redisUsername, Password: redisPassword,
	})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("restricted Redis authentication: %v", err)
	}
	store := NewGateStore(client, time.Minute)
	ticket, err := store.Issue(ctx, GateSession{
		PlayerID: "restricted_meta_fixture", AuthSessionID: "restricted_meta_session",
		ClientVersion: "permission-integration", ProtocolVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Del(context.Background(), gateKey(ticket))
	var successes atomic.Int32
	var failures atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			session, consumeErr := store.Consume(ctx, ticket)
			if consumeErr == nil && session.PlayerID == "restricted_meta_fixture" {
				successes.Add(1)
			} else if !errors.Is(consumeErr, ErrGateTicketInvalid) {
				failures.Add(1)
			}
		})
	}
	group.Wait()
	if successes.Load() != 1 || failures.Load() != 0 {
		t.Fatalf("restricted Lua consumption: successes=%d unexpected_errors=%d", successes.Load(), failures.Load())
	}
	if _, err := store.Consume(ctx, ticket); !errors.Is(err, ErrGateTicketInvalid) {
		t.Fatalf("consumed ticket replay: %v", err)
	}
	t.Log("restricted Redis gate: 16 concurrent consumers, one success, replay denied")
	for label, err := range map[string]error{
		"write outside meta namespace":            client.Set(ctx, "rebound:e2e21:forbidden", "synthetic", time.Second).Err(),
		"read outside meta namespace":             client.Get(ctx, "rebound:e2e21:forbidden").Err(),
		"Lua declared key outside meta namespace": client.Eval(ctx, `return redis.call("GET", KEYS[1])`, []string{"rebound:e2e21:forbidden"}).Err(),
		"Lua dynamic key outside meta namespace":  client.Eval(ctx, `return redis.call("GET", ARGV[1])`, nil, "rebound:e2e21:forbidden").Err(),
		"administrator ACL access":                client.Do(ctx, "ACL", "LIST").Err(),
	} {
		if err == nil || !(strings.Contains(err.Error(), "NOPERM") || strings.Contains(err.Error(), "no permissions")) {
			t.Fatalf("%s must be denied by ACL, error=%v", label, err)
		}
		t.Logf("denied restricted Redis operation: %s", label)
	}
}
