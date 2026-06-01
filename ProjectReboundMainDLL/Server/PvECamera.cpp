// PvECamera.cpp
// Detects the end of the PvE intro LevelSequence and routes every affected
// player through the LateJoinManager spawn pipeline to fix the first-life
// camera detachment.
#include "../SDK.hpp"
#include "PvECamera.h"
#include "LateJoin.h"
#include "../Logging/LogManager.h"

using namespace SDK;

extern LateJoinManager *gLateJoinManager;

static ALevelSequenceActor *g_SeqActor  = nullptr;
static bool                 g_SeqWasPlaying = false;

static bool IsSeqPlaying(ALevelSequenceActor *Actor)
{
    if (!Actor) return false;
    auto *Player = *reinterpret_cast<ULevelSequencePlayer **>(
        reinterpret_cast<uintptr_t>(Actor) + 0x0250);
    if (!Player) return false;
    uint8 status = *reinterpret_cast<uint8 *>(
        reinterpret_cast<uintptr_t>(Player) + 0x02B0);
    return status == 1;
}

static ALevelSequenceActor *FindPlayingSequence()
{
    for (int i = 0; i < UObject::GObjects->Num(); ++i)
    {
        UObject *Obj = UObject::GObjects->GetByIndex(i);
        if (!Obj || !Obj->IsA(ALevelSequenceActor::StaticClass()))
            continue;
        if (IsSeqPlaying(static_cast<ALevelSequenceActor *>(Obj)))
            return static_cast<ALevelSequenceActor *>(Obj);
    }
    return nullptr;
}

static void FixAllPlayers(UNetDriver *NetDriver)
{
    if (!gLateJoinManager || !NetDriver) return;
    for (UNetConnection *pc : NetDriver->ClientConnections)
    {
        if (!pc->PlayerController) continue;
        auto *PBPC = static_cast<APBPlayerController *>(pc->PlayerController);
        gLateJoinManager->ForceFirstLifeSpawn(PBPC);
    }
}

void PVECamFix_Tick(UNetDriver *NetDriver, float DeltaTime)
{
    if (!NetDriver) return;

    if (!g_SeqActor)
    {
        g_SeqActor = FindPlayingSequence();
        if (g_SeqActor)
            g_SeqWasPlaying = true;
        return;
    }

    bool playing = IsSeqPlaying(g_SeqActor);

    if (g_SeqWasPlaying && !playing)
    {
        ServerDebugLog("[CAM-FIX] Intro sequence ended. Fixing players via LateJoin chain.");
        FixAllPlayers(NetDriver);
        g_SeqActor      = nullptr;
        g_SeqWasPlaying = false;
        return;
    }

    g_SeqWasPlaying = playing;
}
