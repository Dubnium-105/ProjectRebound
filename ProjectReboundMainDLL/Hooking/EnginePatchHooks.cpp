// EnginePatchHooks.cpp
// Engine behaviour overrides — not game-logic hooks, just patching engine
// checks that would otherwise break the DS or the client.

#include "EnginePatchHooks.h"
#include "HookCore.h"
#include "../Logging/LogManager.h"

using namespace SDK;

// ======================================================
//  ObjectNeedsLoad / ActorNeedsLoad — always true
// ======================================================

char ObjectNeedsLoadHook(UObject *a1)
{
    return 1;
}

char ActorNeedsLoadHook(UObject *a1)
{
    return 1;
}

// ======================================================
//  HudFunctionThatCrashesTheGame — crash suppressor (dead code)
// ======================================================

static SafetyHookInline HudFunctionThatCrashesTheGame;

__int64 HudFunctionThatCrashesTheGameHook(__int64 a1, __int64 a2)
{
    return 0;
}

// ======================================================
//  GameEngineTick — "NO TICKY" tick-skip hack (dead code)
// ======================================================

static SafetyHookInline GameEngineTick;

__int64 GameEngineTickHook(APlayerController *a1,
                           float a2,
                           __int64 a3,
                           __int64 a4)
{

    static bool flip = true;

    flip = !flip;

    if (flip)
    {
        ServerDebugLog("NO TICKY");
        return 0;
    }

    return GameEngineTick.call<__int64>(a1, a2, a3, a4);
}

// ======================================================
//  IsDedicatedServer / IsServer / IsStandalone
// ======================================================

bool IsDedicatedServer(void *WorldContextOrSomething)
{
    return true;
}

bool IsServer(void *WorldContextOrSomething)
{
    return true;
}

bool IsStandalone(void *WorldContextOrSomething)
{
    return false;
}
