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

    // PB's quick-respawn implementation performs controller-specific cleanup
    // before delegating to Engine.ServerRestartPlayer. A direct engine request
    // for a managed explicit respawn must therefore enter the PB wrapper first;
    // otherwise the pawn can be live while the client remains in death UI.
    inline bool ShouldNormalizeEngineRestartToQuickRespawn(
        ExplicitRequestAction action,
        bool isEngineRestartRequest)
    {
        return action == ExplicitRequestAction::QueueAndForwardNative &&
            isEngineRestartRequest;
    }
}
