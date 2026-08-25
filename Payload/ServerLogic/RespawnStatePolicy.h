#pragma once

namespace RespawnStatePolicy
{
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
}
