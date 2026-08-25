#pragma once

#include <cstdint>

namespace ManagedPossessionSyncPolicy
{
    constexpr bool IsCurrentGenerationSynced(
        const void* currentPawn,
        const std::uint64_t currentLifecycleId,
        const void* lastSyncedPawn,
        const std::uint64_t lastSyncedLifecycleId)
    {
        return currentPawn != nullptr &&
            currentPawn == lastSyncedPawn &&
            currentLifecycleId == lastSyncedLifecycleId;
    }

    struct FPlan
    {
        bool ShouldSync = false;
        bool SendMatchHasStarted = false;
        bool SendRoundHasStarted = false;
        bool SendNotifyGameStarted = false;
        bool SendReadyAtStartSpot = false;
        bool SendGotoPlaying = false;
        bool SendRestartAndAcknowledge = false;
    };

    constexpr FPlan BuildPlan(
        const bool isInitialJoin,
        const bool isSeamlessRebound,
        const bool hasCompletedSpawn,
        const bool alreadySyncedCurrentPawn)
    {
        if (alreadySyncedCurrentPawn)
            return {};

        // Every initial-role spawn is fully owned by the native
        // RestartAtStartSpot -> replicated possess -> character-ready chain.
        // This includes a fresh seamless destination: that path is now queued
        // through initial role selection instead of managed respawn. Replaying
        // GotoPlaying/ClientRestart before Character::ClientReadyAtStartSpot
        // leaves the first-person arms on the pre-ready pose even though Pawn,
        // acknowledgement, weapon and ViewTarget are otherwise valid.
        // The first fresh seamless destination Pawn reaches this boundary
        // before the native start-spot/intro sequence has completed. Keep the
        // possession chain native. A separate post-intro policy may return the
        // camera ViewTarget only; it must never re-enter this restart plan.
        if (isInitialJoin && !hasCompletedSpawn)
            return {};

        FPlan plan{};
        plan.ShouldSync = true;
        plan.SendGotoPlaying = true;
        plan.SendRestartAndAcknowledge = true;

        // A legacy managed rebound without a carried Pawn still needs the
        // ordinary possession handshake. Fresh seamless destinations do not
        // reach this branch because they are marked as initial-role spawns.
        if (isSeamlessRebound)
        {
            return plan;
        }

        if (!hasCompletedSpawn)
        {
            plan.SendReadyAtStartSpot = true;
            plan.SendMatchHasStarted = true;
            plan.SendRoundHasStarted = true;
            plan.SendNotifyGameStarted = true;
        }

        return plan;
    }
}
