#include "../ServerLogic/ManagedPossessionSyncPolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }

    void ExpectPlayingPossessionSync(
        const ManagedPossessionSyncPolicy::FPlan& plan)
    {
        Expect(plan.ShouldSync, "a fresh Pawn generation must be synchronized");
        Expect(plan.SendGotoPlaying, "synchronization must enter Playing");
        Expect(plan.SendRestartAndAcknowledge,
            "synchronization must restart and acknowledge possession");
    }
}

int main()
{
    int pawnA = 0;
    int pawnB = 0;
    Expect(ManagedPossessionSyncPolicy::IsCurrentGenerationSynced(
        &pawnA, 7, &pawnA, 7),
        "the same Pawn and lifecycle must be recognized as synchronized");
    Expect(!ManagedPossessionSyncPolicy::IsCurrentGenerationSynced(
        &pawnA, 8, &pawnA, 7),
        "a new lifecycle at the same address must synchronize again");
    Expect(!ManagedPossessionSyncPolicy::IsCurrentGenerationSynced(
        &pawnB, 7, &pawnA, 7),
        "a new Pawn in the same lifecycle must synchronize again");
    Expect(!ManagedPossessionSyncPolicy::IsCurrentGenerationSynced(
        nullptr, 7, nullptr, 7),
        "null Pawns must never be marked synchronized");

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            true, false, false, false);
        Expect(!plan.ShouldSync,
            "initial first spawn must remain owned by the native restart path");
        Expect(!plan.SendReadyAtStartSpot,
            "initial first spawn must not replay ready-at-start-spot");
        Expect(!plan.SendMatchHasStarted,
            "initial first spawn must not duplicate match start");
        Expect(!plan.SendRoundHasStarted,
            "initial first spawn must not duplicate round start");
        Expect(!plan.SendNotifyGameStarted,
            "initial first spawn must not duplicate game-start notification");
        Expect(!plan.SendGotoPlaying,
            "initial first spawn must not race native Playing state");
        Expect(!plan.SendRestartAndAcknowledge,
            "initial first spawn must not replay possession RPCs");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            false, false, false, false);
        ExpectPlayingPossessionSync(plan);
        Expect(plan.SendReadyAtStartSpot,
            "late first spawn must become ready at its start spot");
        Expect(plan.SendMatchHasStarted,
            "late first spawn must catch up match state");
        Expect(plan.SendRoundHasStarted,
            "late first spawn must catch up round state");
        Expect(plan.SendNotifyGameStarted,
            "late first spawn must catch up game-start notification");
    }

    for (int initialJoin = 0; initialJoin <= 1; ++initialJoin)
    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            initialJoin != 0, false, true, false);
        ExpectPlayingPossessionSync(plan);
        Expect(!plan.SendReadyAtStartSpot,
            "later spawns must not replay first-spawn ready");
        Expect(!plan.SendMatchHasStarted,
            "later spawns must not replay match start");
        Expect(!plan.SendRoundHasStarted,
            "later spawns must not replay round start");
        Expect(!plan.SendNotifyGameStarted,
            "later spawns must not replay game-start notification");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            false, false, true, true);
        Expect(!plan.ShouldSync,
            "an already-synchronized generation must be a no-op");
        Expect(!plan.SendMatchHasStarted, "no-op must not send match start");
        Expect(!plan.SendRoundHasStarted, "no-op must not send round start");
        Expect(!plan.SendNotifyGameStarted,
            "no-op must not send game-start notification");
        Expect(!plan.SendReadyAtStartSpot, "no-op must not send ready");
        Expect(!plan.SendGotoPlaying, "no-op must not send Playing");
        Expect(!plan.SendRestartAndAcknowledge,
            "no-op must not restart possession");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            false, true, true, true);
        Expect(!plan.ShouldSync,
            "an exactly synchronized seamless carry must remain native");
        Expect(!plan.SendGotoPlaying,
            "a seamless carry must not race native Playing state");
        Expect(!plan.SendRestartAndAcknowledge,
            "a seamless carry must not replay possession RPCs");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            true, true, true, false);
        ExpectPlayingPossessionSync(plan);
        Expect(!plan.SendReadyAtStartSpot,
            "a later seamless respawn must preserve native character ready");
        Expect(!plan.SendMatchHasStarted,
            "a later seamless respawn must not replay match start");
        Expect(!plan.SendRoundHasStarted,
            "a later seamless respawn must not replay round start");
        Expect(!plan.SendNotifyGameStarted,
            "a later seamless respawn must not replay game-start UI");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            false, true, false, false);
        ExpectPlayingPossessionSync(plan);
        Expect(!plan.SendReadyAtStartSpot,
            "the racy no-arg Ready call must not impersonate native start-spot readiness");
        Expect(!plan.SendMatchHasStarted,
            "a rebound whose Pawn was rebuilt must not replay match start");
        Expect(!plan.SendRoundHasStarted,
            "a rebound whose Pawn was rebuilt must not replay round start");
        Expect(!plan.SendNotifyGameStarted,
            "a rebound whose Pawn was rebuilt must not replay game-start UI");
    }

    {
        const auto plan = ManagedPossessionSyncPolicy::BuildPlan(
            true, true, false, false);
        Expect(!plan.ShouldSync,
            "fresh seamless initial-role spawn must remain native");
        Expect(!plan.SendReadyAtStartSpot,
            "fresh seamless initial-role spawn must not use no-arg ready");
        Expect(!plan.SendMatchHasStarted,
            "fresh seamless initial-role spawn must not replay match start");
        Expect(!plan.SendRoundHasStarted,
            "fresh seamless initial-role spawn must not replay round start");
        Expect(!plan.SendNotifyGameStarted,
            "fresh seamless initial-role spawn must not replay game-start UI");
        Expect(!plan.SendGotoPlaying,
            "fresh seamless initial-role spawn must not race native Playing state");
        Expect(!plan.SendRestartAndAcknowledge,
            "fresh seamless initial-role spawn must not replay possession RPCs");
    }

    std::cout << "managed possession sync policy tests passed\n";
    return 0;
}
