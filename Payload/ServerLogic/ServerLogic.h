#pragma once
#include <vector>
#include <cstdint>
#include <unordered_map>
#include <unordered_set>
#include "../SDK.hpp"

class LateJoinManager;

// Global server state
extern bool listening;
extern std::vector<SDK::APlayerController *> playerControllersPossessed;
extern int NumPlayersJoined;
extern float PlayerJoinTimerSelectFuck;
extern bool DidProcFlow;
extern bool DidBroadcastRoleSelection;
extern float StartMatchTimer;
extern int NumPlayersSelectedRole;
extern bool DidProcStartMatch;
extern bool canStartMatch;
extern int NumExpectedPlayers;
extern float MatchStartCountdown;
extern float ReplicationFlushAccumulator;
extern std::unordered_map<SDK::APBPlayerController *, bool> PlayerRespawnAllowedMap;
extern std::unordered_set<SDK::APBPlayerController *> PlayersConfirmedRole;
extern std::unordered_set<SDK::APBPlayerController *> ConnectedPlayerControllers;
extern std::unordered_set<SDK::APBPlayerController *> DisconnectedPlayerControllers;
extern std::unordered_set<SDK::APBPlayerController *> PendingNameUpdatePlayers;
extern std::unordered_set<SDK::APBPlayerController *> AppliedNameUpdatePlayers;
extern float PendingNameApplyAccumulator;
extern LateJoinManager *gLateJoinManager;

// Game state helpers
SDK::APBGameState *GetPBGameState();
SDK::APBGameMode *GetPBGameMode();
bool IsRoundCurrentlyInProgress();
int GetCurrentPlayerCount();

// Player name update helpers
void QueuePendingPlayerNameUpdate(SDK::APBPlayerController *PlayerController);
void ApplyPendingPlayerNameUpdates(const char *reason);

// Match-scoped globals must be cleared when the injected process switches
// UWorlds. Seed this before Listen and observe it from game-thread hooks.
void ResetServerMatchStateForWorld(SDK::UWorld *world);
void EnsureServerMatchWorld(SDK::UWorld *world);
bool BeginServerMatchGeneration(
    SDK::UWorld* world,
    SDK::UNetDriver* netDriver);
std::uint64_t GetServerMatchGeneration();

// Preserves the pinned build's native return-to-menu and cleanup sequence.
// A null GameMode can only occur after a failed travel and falls back to the
// engine's RequestExit(false) entry.
void BeginGracefulDedicatedExit(
    SDK::APBGameMode* gameMode,
    const char* reason);

// Server startup
void StartServer();
