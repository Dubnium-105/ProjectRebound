#include "../ServerLogic/RespawnStatePolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    using RespawnStatePolicy::ExplicitRequestAction;
    using RespawnStatePolicy::LiveRoleConfirmationAction;

    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }

    void TestUnmanagedAndPermittedCallsPassThrough()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            false, false, false, false, true) ==
            ExplicitRequestAction::PassThrough,
            "unmanaged native restart must remain untouched");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, true, false, false, true) ==
            ExplicitRequestAction::PassThrough,
            "manager fallback under a permit must not be re-queued");
    }

    void TestOnlyAwaitingInputAcceptsExplicitIntent()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, false, true, true) ==
            ExplicitRequestAction::Deny,
            "duplicate F during an active spawn must be denied");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, false, true) ==
            ExplicitRequestAction::Deny,
            "an invalid lifecycle must not swallow-and-pretend to queue");
    }

    void TestABSelectsOnlyTheDispatchWiring()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, true, false) ==
            ExplicitRequestAction::QueueAndSuppress,
            "A must preserve the legacy replacement chain");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, true, true) ==
            ExplicitRequestAction::QueueAndForwardNative,
            "B must forward the exact explicit native request");
    }

    void TestManagedEngineRestartWaitsForNativePBQuickRespawn()
    {
        Expect(RespawnStatePolicy::ShouldDeferEngineRestartToPBQuickRespawn(
            ExplicitRequestAction::QueueAndForwardNative, true),
            "managed engine restart must wait for the native PB quick RPC");
        Expect(!RespawnStatePolicy::ShouldDeferEngineRestartToPBQuickRespawn(
            ExplicitRequestAction::QueueAndForwardNative, false),
            "PB quick requests must remain exact native requests");
        Expect(!RespawnStatePolicy::ShouldDeferEngineRestartToPBQuickRespawn(
            ExplicitRequestAction::QueueAndSuppress, true),
            "legacy A wiring must retain its managed replacement path");
        Expect(!RespawnStatePolicy::ShouldDeferEngineRestartToPBQuickRespawn(
            ExplicitRequestAction::PassThrough, true),
            "permitted and unmanaged engine requests must remain untouched");
    }

    void TestLiveRoleConfirmationStagesOnlyTheNextLife()
    {
        Expect(RespawnStatePolicy::DecideLiveRoleConfirmation(
            true, true, true, true) ==
            LiveRoleConfirmationAction::CommitForNextRespawn,
            "a live spawned managed player must stage role confirmation");
        Expect(RespawnStatePolicy::DecideLiveRoleConfirmation(
            false, true, true, true) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "unmanaged native role confirmation must remain untouched");
        Expect(RespawnStatePolicy::DecideLiveRoleConfirmation(
            true, false, true, true) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "first spawn must retain native restart");
        Expect(RespawnStatePolicy::DecideLiveRoleConfirmation(
            true, true, false, true) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "post-death role selection must defer to the death-wait action");
        Expect(RespawnStatePolicy::DecideLiveRoleConfirmation(
            true, true, true, false) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "an invalid role must never arm restart suppression");

        Expect(RespawnStatePolicy::DecideRoleConfirmationRestart(
            true, false, true) ==
            LiveRoleConfirmationAction::CommitForNextRespawn,
            "a live staged confirmation must arm unconditional suppression");
        Expect(RespawnStatePolicy::DecideRoleConfirmationRestart(
            false, true, true) ==
            LiveRoleConfirmationAction::CommitDuringRespawnCooldown,
            "a death-wait confirmation must select the PB quick replacement");
        Expect(RespawnStatePolicy::DecideRoleConfirmationRestart(
            false, false, true) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "ordinary and initial confirmation must retain native restart");
        Expect(RespawnStatePolicy::DecideRoleConfirmationRestart(
            false, true, false) ==
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            "an invalid death-wait role must not arm suppression");

        Expect(RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::CommitForNextRespawn,
            true),
            "the scoped restart for the same controller must be suppressed");
        Expect(!RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::CommitForNextRespawn,
            false),
            "another controller's restart must remain native");
        Expect(RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::CommitDuringRespawnCooldown,
            true),
            "a death-wait confirmation must not restart its retained corpse");
        Expect(RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::CommitDuringRespawnCooldown,
            true),
            "a cleared death-wait controller must still use PB quick respawn");
        Expect(!RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::CommitDuringRespawnCooldown,
            false),
            "another controller's death-wait restart must remain native");
        Expect(!RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            LiveRoleConfirmationAction::NativeConfirmAndRestart,
            true),
            "ordinary confirmation must retain native restart");
    }

    void TestPostDeathRoleConfirmationRespectsNativeCooldown()
    {
        Expect(RespawnStatePolicy::
            ShouldRemainAwaitingRespawnAfterPostDeathRoleConfirmation(
                true, false, false),
            "a role committed during the death cooldown must keep waiting");
        Expect(RespawnStatePolicy::
            ShouldRemainAwaitingRespawnAfterPostDeathRoleConfirmation(
                true, true, true),
            "a suppressed same-role confirmation must not mistake the corpse "
            "for a new pawn");
        Expect(!RespawnStatePolicy::
            ShouldRemainAwaitingRespawnAfterPostDeathRoleConfirmation(
                true, false, true),
            "a native role confirmation that produced its pawn may finalize");
        Expect(!RespawnStatePolicy::
            ShouldRemainAwaitingRespawnAfterPostDeathRoleConfirmation(
                false, true, false),
            "initial and ordinary role confirmation must keep their flow");
    }

    void TestPreOrderEditingDoesNotBecomeRespawnIntent()
    {
        Expect(!RespawnStatePolicy::
            ShouldQueueManagedRespawnAfterPreOrderReturn(
                true, true, true),
            "a selected-role pre-order during the death wait must stay staged");
        Expect(RespawnStatePolicy::
            ShouldQueueManagedRespawnAfterPreOrderReturn(
                true, true, false),
            "a blocked non-awaiting lifecycle may retain managed recovery");
        Expect(!RespawnStatePolicy::
            ShouldQueueManagedRespawnAfterPreOrderReturn(
                false, true, false),
            "another role's replay must not queue the selected player");
        Expect(!RespawnStatePolicy::
            ShouldQueueManagedRespawnAfterPreOrderReturn(
                true, false, false),
            "an already-allowed player does not need managed recovery");
    }
}

int main()
{
    TestUnmanagedAndPermittedCallsPassThrough();
    TestOnlyAwaitingInputAcceptsExplicitIntent();
    TestABSelectsOnlyTheDispatchWiring();
    TestManagedEngineRestartWaitsForNativePBQuickRespawn();
    TestLiveRoleConfirmationStagesOnlyTheNextLife();
    TestPostDeathRoleConfirmationRespectsNativeCooldown();
    TestPreOrderEditingDoesNotBecomeRespawnIntent();
    std::cout << "respawn state policy tests passed\n";
    return 0;
}
