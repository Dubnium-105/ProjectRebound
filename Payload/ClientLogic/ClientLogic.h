#pragma once

#include <string>

namespace SDK
{
    class APBPlayerController;
}

// Thread-safe producer API. The actual Unreal calls are performed by
// PumpPendingClientCommands from the ProcessEvent game thread.
[[nodiscard]] bool QueueConnectToMatch(const std::string& target);
void ConnectToMatch();
void AutoConnectToMatchFromCmdline();
void NotifyClientLoginCompleted();
void PumpPendingClientCommands();

// Owned multi-match seamless travel retains the local controller and HUD.
// Arm at the marked ClientTravel RPC, then consume once at the first valid
// destination start RPC so source-match result presentation is left intact.
void ArmOwnedSeamlessDestinationUiCleanup();
bool TryFinalizeOwnedSeamlessDestinationUi(
    SDK::APBPlayerController* playerController);

// Arm at the owned seamless ClientTravel RPC. K2_RoundHasStarted marks the
// final opening phase, then the first following PlayerCameraManager ReceiveTick
// becomes the one-shot settle boundary. If the retained controller still owns
// ViewTarget, invoke the game's local StopThirdPersonCamera teardown once;
// never replay possession or input.
void ArmOwnedSeamlessIntroCameraRecovery();
void NotifyOwnedSeamlessIntroRoundBoundary();
bool TryFinalizeOwnedSeamlessIntroCamera();

// Observe the game's native death -> successful ClientRestart lifecycle. This
// does not dispatch respawn or process any input; it only pairs the retained
// HUD's native death-layer teardown after the new Pawn is already present.
void ArmNativeRespawnUiCleanup();
bool TryFinalizeNativeRespawnUi(
    SDK::APBPlayerController* playerController);
