#pragma once

#include <string_view>

namespace SeamlessIntroCameraPolicy
{
    enum class ERecoveryDecision
    {
        Wait,
        StopThirdPersonCamera
    };

    inline bool IsNativeIntroCompletionEvent(
        const std::string_view fullFunctionName)
    {
        // The pinned client does not receive the PlayerController countdown
        // RPC in this dedicated flow. The destination APBGameState dispatches
        // K2_StartCountdownToStart and then its mode-specific
        // K2_RoundHasStarted event. OSS can restore the retained controller as
        // ViewTarget, and replay the source death HUD, in that final event.
        // Recover only after it returns. Require both the PBGameState owner
        // token and exact suffix so the sibling PlayerController event is
        // excluded.
        constexpr std::string_view suffix =
            ".K2_RoundHasStarted";
        return fullFunctionName.find("PBGameState") !=
                std::string_view::npos &&
            fullFunctionName.size() >= suffix.size() &&
            fullFunctionName.ends_with(suffix);
    }

    inline bool IsNativeCameraSettleEvent(
        const std::string_view fullFunctionName)
    {
        // Process the first PlayerCameraManager Blueprint tick after the
        // round-start event. The engine's camera tick can perform one final
        // AutoManage/ViewTarget update after K2_RoundHasStarted returns.
        constexpr std::string_view suffix = ".ReceiveTick";
        return fullFunctionName.find("PlayerCamera") !=
                std::string_view::npos &&
            fullFunctionName.size() >= suffix.size() &&
            fullFunctionName.ends_with(suffix);
    }

    constexpr ERecoveryDecision Decide(
        const bool pendingOwnedSeamlessDestination,
        const bool isLocalPlayerController,
        const bool hasPlayablePawn,
        const bool acknowledgedPawnMatches,
        const bool pbCharacterMatches,
        const bool pawnIsAlive,
        const bool cameraViewIsNearPawn)
    {
        if (!pendingOwnedSeamlessDestination || !isLocalPlayerController ||
            !hasPlayablePawn || !acknowledgedPawnMatches ||
            !pbCharacterMatches || !pawnIsAlive)
        {
            return ERecoveryDecision::Wait;
        }

        // ViewTarget == Pawn is not a first-person invariant: the PB third-
        // person camera also follows the Pawn and leaves the camera behind the
        // body. A remote cinematic can overwrite StopThirdPersonCamera on its
        // next tick, so wait until its POV has returned to the living Pawn's
        // vicinity, then run the game's own idempotent teardown exactly once.
        return cameraViewIsNearPawn
            ? ERecoveryDecision::StopThirdPersonCamera
            : ERecoveryDecision::Wait;
    }
}
