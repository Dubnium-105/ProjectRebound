#pragma once

#include <string>
#include <unordered_map>
#include "../Libs/safetyhook.hpp"
#include "../SDK.hpp"

// ======================================================
//  HookCore — shared hook infrastructure.
//  SafetyHookInline variables, classification cache,
//  inline-hook helpers, and all three init entry points.
// ======================================================

// --- SafetyHookInline variables (defined in HookCore.cpp) ---

extern SafetyHookInline TickFlush;
extern SafetyHookInline ProcessEvent;
extern SafetyHookInline ProcessEventClient;
extern SafetyHookInline PostLoginHook;
extern SafetyHookInline NotifyActorDestroyed;
extern SafetyHookInline NotifyAcceptingConnection;
extern SafetyHookInline NotifyControlMessage;
extern SafetyHookInline OnFireWeaponHook;
extern SafetyHookInline ClientDeathCrash;
extern SafetyHookInline ObjectNeedsLoad;
extern SafetyHookInline ActorNeedsLoad;
extern SafetyHookInline IsDedicatedServerHook;
extern SafetyHookInline IsServerHook;
extern SafetyHookInline IsStandaloneHook;
// HudFunctionThatCrashesTheGame and GameEngineTick are dead code
// (not in any hook table). They live as static in EnginePatchHooks.cpp.

// --- Hook initialization ---

void InitMessageBoxHook();
void InitServerHooks();
void InitClientHook();

// --- Inline-hook helpers ---

struct FInlineHookSpec
{
    const char* Name;
    SafetyHookInline* Storage;
    uintptr_t Offset;
    void* Detour;
};

void InstallInlineHook(const FInlineHookSpec& spec);

template <size_t Count>
void InstallInlineHooks(const FInlineHookSpec (&specs)[Count])
{
    for (const FInlineHookSpec& spec : specs)
    {
        InstallInlineHook(spec);
    }
}

// --- ProcessEvent classification ---

enum class EServerProcessEventKind
{
    None,
    QuickRespawn,
    ServerRestartPlayer,
    CanPlayerSelectRole,
    CanSelectRole,
    ServerConfirmRoleSelection,
    ReadyToMatchIntroWaitingToStart,
    ClientBeKilled,
    PlayerCanRestart,
    MatchHasEnded,
    StartMatchEnding,
    StartShowingMatchResult
};

enum class EClientProcessEventKind
{
    None,
    EnterGameConstruct,
    EnterGameActivated,
    MainMenuConstruct,
    ConnectMatchServerTimeout
};

struct FCachedProcessEventInfo
{
    std::string FullName;
    EServerProcessEventKind ServerKind = EServerProcessEventKind::None;
    EClientProcessEventKind ClientKind = EClientProcessEventKind::None;
};

// Classification + cache (shared by server and client dispatch)
EServerProcessEventKind ClassifyServerProcessEvent(const std::string& functionName);
EClientProcessEventKind ClassifyClientProcessEvent(const std::string& functionName);
const FCachedProcessEventInfo& GetProcessEventInfo(SDK::UFunction* Function);

bool IsLateJoinRoleQuery(EServerProcessEventKind kind);
