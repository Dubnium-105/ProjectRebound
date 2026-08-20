package metaserver

import (
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestTCPRateStatePruningRetainsActiveConnections(t *testing.T) {
	server := NewTCPServer(
		config.MetaServerConfig{
			MaxConnectionsPerIP:        2,
			ConnectionsPerIPPerMinute:  2,
			RPCCallsPerPlayerPerMinute: 2,
		},
		nil, nil, NewMetaMetrics(), nil,
	)
	now := time.Now().UTC()
	server.byIP["stale"] = &connectionRate{
		windowStart: now.Add(-3 * time.Minute),
		lastSeen:    now.Add(-3 * time.Minute),
	}
	server.byIP["active"] = &connectionRate{
		active:      1,
		windowStart: now.Add(-3 * time.Minute),
		lastSeen:    now.Add(-3 * time.Minute),
	}
	server.rpcRate["stale-player"] = &connectionRate{
		windowStart: now.Add(-3 * time.Minute),
		lastSeen:    now.Add(-3 * time.Minute),
	}

	if !server.admit("fresh") {
		t.Fatal("fresh connection was rejected")
	}
	if _, ok := server.byIP["stale"]; ok {
		t.Fatal("inactive stale IP rate state was not pruned")
	}
	if _, ok := server.byIP["active"]; !ok {
		t.Fatal("active IP rate state was pruned")
	}
	if _, ok := server.rpcRate["stale-player"]; ok {
		t.Fatal("stale player RPC rate state was not pruned")
	}
	server.release("fresh")
}

func TestTCPRateLimitsRemainEnforced(t *testing.T) {
	server := NewTCPServer(
		config.MetaServerConfig{
			MaxConnectionsPerIP:        1,
			ConnectionsPerIPPerMinute:  1,
			RPCCallsPerPlayerPerMinute: 1,
		},
		nil, nil, NewMetaMetrics(), nil,
	)
	if !server.admit("198.51.100.10") || server.admit("198.51.100.10") {
		t.Fatal("per-IP connection limit was not enforced")
	}
	if !server.allowPlayerRPC("player") || server.allowPlayerRPC("player") {
		t.Fatal("per-player RPC limit was not enforced")
	}
	server.release("198.51.100.10")
}

func TestNativeTextFilterDoesNotConsumePlayerRPCBudget(t *testing.T) {
	server := NewTCPServer(
		config.MetaServerConfig{RPCCallsPerPlayerPerMinute: 1},
		nil, nil, NewMetaMetrics(), nil,
	)

	for range 600 {
		if !server.allowNativeRPC("player", "/chat.chat/TextFilter") {
			t.Fatal("TextFilter compatibility burst was rejected")
		}
	}
	if !server.allowNativeRPC("player", "/assets.Assets/UpdateWeaponArchiveV2") {
		t.Fatal("TextFilter compatibility burst consumed the stateful RPC budget")
	}
	if server.allowNativeRPC("player", "/assets.Assets/UpdateRoleArchiveV2") {
		t.Fatal("stateful RPC budget was no longer enforced")
	}
}

func TestOnlyNativeTextFilterIsExemptFromPlayerRPCBudget(t *testing.T) {
	for _, rpcPath := range []string{
		"/assets.Assets/GetPlayerArchiveV2",
		"/assets.Assets/QueryAssets",
		"/assets.Assets/UpdateRoleArchiveV2",
		"/assets.Assets/UpdateWeaponArchiveV2",
		"/matchmaking.Matchmaking/StartUnityMatchmaking",
		"/unknown.Service/UnknownMethod",
	} {
		if nativeRPCExemptFromPlayerBudget(rpcPath) {
			t.Fatalf("stateful or unknown RPC unexpectedly exempt: %s", rpcPath)
		}
	}
}
