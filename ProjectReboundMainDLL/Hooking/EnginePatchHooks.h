#pragma once

#include "../SDK.hpp"

// ======================================================
//  EnginePatchHooks — engine behaviour overrides:
//  IsDedicatedServer / IsServer / IsStandalone,
//  ObjectNeedsLoad / ActorNeedsLoad,
//  GameEngineTick (dead code), HudCrash fix.
// ======================================================

bool IsDedicatedServer(void* WorldContextOrSomething);
bool IsServer(void* WorldContextOrSomething);
bool IsStandalone(void* WorldContextOrSomething);

char ObjectNeedsLoadHook(SDK::UObject* a1);
char ActorNeedsLoadHook(SDK::UObject* a1);

// Dead code — not in any hook table
__int64 HudFunctionThatCrashesTheGameHook(__int64 a1, __int64 a2);
__int64 GameEngineTickHook(SDK::APlayerController* a1, float a2, __int64 a3, __int64 a4);
