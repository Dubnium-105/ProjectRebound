#pragma once

namespace RespawnStatePolicy
{
    enum class LiveRoleConfirmationAction
    {
        NativeConfirmAndRestart,
        CommitForNextRespawn,
        CommitDuringRespawnCooldown,
    };

    enum class ExplicitRequestAction
    {
        PassThrough,
        Deny,
        QueueAndSuppress,
        QueueAndForwardNative,
    };

    // Explicit restart RPCs are user intent only while the managed lifecycle
    // is waiting for F/ESC. Internal fallback calls carry a per-controller
    // permit and must pass through without being mistaken for a second F.
    inline ExplicitRequestAction DecideExplicitRequest(
        bool managedPlayer,
        bool hasManagedPermit,
        bool awaitingRespawnInput,
        bool canQueueManagedRespawn,
        bool nativeForwardEnabled)
    {
        if (!managedPlayer || hasManagedPermit)
            return ExplicitRequestAction::PassThrough;
        if (!awaitingRespawnInput || !canQueueManagedRespawn)
            return ExplicitRequestAction::Deny;
        return nativeForwardEnabled
            ? ExplicitRequestAction::QueueAndForwardNative
            : ExplicitRequestAction::QueueAndSuppress;
    }

    // The death screen can emit Engine.ServerRestartPlayer before its PB
    // ServerQuickRespawn RPC. Replacing the engine request with a server-side
    // PB call creates a Pawn, but it does not reproduce the client's local
    // ExitObserverState transition and leaves the death UI/input layer alive.
    // Preserve the native PB sequence by waiting for the exact quick-respawn
    // RPC instead of manufacturing it on the server.
    inline bool ShouldDeferEngineRestartToPBQuickRespawn(
        ExplicitRequestAction action,
        bool isEngineRestartRequest)
    {
        return action == ExplicitRequestAction::QueueAndForwardNative &&
            isEngineRestartRequest;
    }

    // ServerConfirmRoleSelection always commits the selected role and merged
    // pre-ordering state before it calls APBGameMode::RestartPlayer.  That
    // restart is correct for first spawn and for a post-cooldown confirmation,
    // but a live managed player opened the in-match role screen only to stage
    // the next life. Preserve the native commit and suppress only its
    // synchronous restart for this exact lifecycle shape.
    inline LiveRoleConfirmationAction DecideLiveRoleConfirmation(
        bool managedPlayer,
        bool playerAlreadySpawned,
        bool hasPlayablePawn,
        bool requestedRoleIsConcrete)
    {
        return managedPlayer && playerAlreadySpawned && hasPlayablePawn &&
            requestedRoleIsConcrete
            ? LiveRoleConfirmationAction::CommitForNextRespawn
            : LiveRoleConfirmationAction::NativeConfirmAndRestart;
    }

    // The post-death role screen also uses ServerConfirmRoleSelection. Keep
    // the native role/pre-order commit, but mark that confirmation separately
    // so its synchronous RestartPlayer can be replaced by the PB-specific
    // ServerQuickRespawn entry point after the commit has completed.
    inline LiveRoleConfirmationAction DecideRoleConfirmationRestart(
        bool stageForNextRespawn,
        bool wasAwaitingRespawnInput,
        bool requestedRoleIsConcrete)
    {
        if (!requestedRoleIsConcrete)
            return LiveRoleConfirmationAction::NativeConfirmAndRestart;
        if (stageForNextRespawn)
            return LiveRoleConfirmationAction::CommitForNextRespawn;
        if (wasAwaitingRespawnInput)
            return LiveRoleConfirmationAction::CommitDuringRespawnCooldown;
        return LiveRoleConfirmationAction::NativeConfirmAndRestart;
    }

    // APBGameMode::RestartPlayer is not the post-death request entry point on
    // this build. With a retained corpse it sends ClientRestart for stale
    // identity; with Pawn already cleared it still skips the PB cooldown and
    // observer/controller transition performed by ServerQuickRespawn. Suppress
    // it for every same-controller death-wait confirmation, independent of
    // Pawn presence and the unrelated +0x428 deferred-queue mode.
    inline bool ShouldSuppressRoleConfirmationRestart(
        LiveRoleConfirmationAction action,
        bool sameController)
    {
        if (!sameController)
            return false;
        return action == LiveRoleConfirmationAction::CommitForNextRespawn ||
            action ==
                LiveRoleConfirmationAction::CommitDuringRespawnCooldown;
    }

    // Selecting a role from the death screen can commit before the native
    // respawn cooldown expires. The first argument must be captured at RPC
    // entry: the synchronous confirmation/restart stack can mutate the managed
    // state before it returns. A suppressed queue-mode restart cannot have
    // produced a Pawn; otherwise verify the requested Pawn after the native
    // call. Keep failures in the explicit F/ESC wait state instead of racing
    // the cooldown with the generic three-attempt fallback.
    inline bool ShouldRemainAwaitingRespawnAfterPostDeathRoleConfirmation(
        bool wasAwaitingRespawnInput,
        bool confirmationRestartWasSuppressed,
        bool requestedPawnIsPlayable)
    {
        return wasAwaitingRespawnInput &&
            (confirmationRestartWasSuppressed || !requestedPawnIsPlayable);
    }

    // ServerPreOrderInventory is configuration intent, not respawn intent.
    // The death/role screen replays all role pre-orders before it confirms the
    // selected role; queuing on the selected-role replay consumes the explicit
    // AwaitingRespawnInput state before ServerConfirmRoleSelection can observe
    // it. Preserve the existing recovery behavior for other blocked lifecycle
    // states, but keep a pre-order that entered during the death wait staged.
    inline bool ShouldQueueManagedRespawnAfterPreOrderReturn(
        bool isCurrentSelectedRole,
        bool respawnIsBlocked,
        bool wasAwaitingRespawnInput)
    {
        return isCurrentSelectedRole && respawnIsBlocked &&
            !wasAwaitingRespawnInput;
    }
}
