#pragma once

namespace ServerHookPolicy
{
    struct InstallPlan
    {
        bool ForceServerOnlyObjectLoading{};
        bool ForceDedicatedNetMode{};
        bool GuardRemotePlayerViewportLayers{};
    };

    // The pinned dedicated bootstrap needs its compatibility groups because
    // it starts from a client-capable executable image. A native listen host
    // already has the correct client/server load filters and NetMode. It does,
    // however, expose PBGameViewportClient while spawning remote controllers;
    // their client-HUD calls require the explicit ULocalPlayer guard below.
    inline constexpr InstallPlan BuildInstallPlan(
        const bool forceDedicatedMode) noexcept
    {
        return InstallPlan{
            .ForceServerOnlyObjectLoading = forceDedicatedMode,
            .ForceDedicatedNetMode = forceDedicatedMode,
            .GuardRemotePlayerViewportLayers = !forceDedicatedMode,
        };
    }

    inline constexpr bool ShouldForwardPlayerViewportLayerRequest(
        const bool guardEnabled,
        const bool hasPlayerController,
        const bool hasLocalPlayer) noexcept
    {
        // Preserve the native null-controller no-op and every dedicated/client
        // path. A listen host must suppress only the client HUD work issued by
        // a remote PlayerController, which has no ULocalPlayer by definition.
        return !guardEnabled || !hasPlayerController || hasLocalPlayer;
    }

    inline constexpr bool ShouldRegisterListenHostParticipant(
        const bool isListenAuthority,
        const bool isCurrentWorldPlayer,
        const bool hasLocalPlayer,
        const bool alreadyRegistered) noexcept
    {
        // A listen host's local controller can be created without traversing
        // the remote PostLogin hook, or it can be cleared by the destination
        // world's first generation reset. Re-admit only the exact local
        // controller that is already present in the current GameState.
        return isListenAuthority && isCurrentWorldPlayer && hasLocalPlayer &&
            !alreadyRegistered;
    }

    inline constexpr bool ShouldRecoverListenHostRoleConfirmation(
        const bool isListenAuthority,
        const bool isSynthesizedListenHost,
        const bool isCurrentWorldPlayer,
        const bool isInitialJoin,
        const bool didBroadcastRoleSelection,
        const bool alreadyConfirmed,
        const bool alreadyAttempted,
        const bool hasSelectedRole,
        const bool hasConcreteRole) noexcept
    {
        // A listen host can arrive in the match world with its native role
        // already committed, so ClientSelectRole has no transition left that
        // would emit ServerConfirmRoleSelection. Recover only that proven
        // local initial participant, once, after the shared role prompt opens.
        return isListenAuthority && isSynthesizedListenHost &&
            isCurrentWorldPlayer && isInitialJoin &&
            didBroadcastRoleSelection && !alreadyConfirmed &&
            !alreadyAttempted && hasSelectedRole && hasConcreteRole;
    }
}
