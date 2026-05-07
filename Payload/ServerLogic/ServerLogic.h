#pragma once
#include <vector>
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
extern float StartMatchTimer;
extern int NumPlayersSelectedRole;
extern bool DidProcStartMatch;
extern bool canStartMatch;
extern int NumExpectedPlayers;
extern float MatchStartCountdown;
extern float ReplicationFlushAccumulator;
extern std::unordered_map<SDK::APBPlayerController *, bool> PlayerRespawnAllowedMap;
extern std::unordered_set<SDK::APBPlayerController *> PlayersConfirmedRole;
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

// Server startup
void StartServer();