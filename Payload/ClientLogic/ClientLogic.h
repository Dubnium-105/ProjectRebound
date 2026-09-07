#pragma once

#include <string>
#include <string_view>
#include "../Libs/json.hpp"

namespace SDK
{
    class APBPlayerController;
}

struct AuthorizedJoinResult
{
    bool accepted = false;
    std::string code;
    std::string message;
};

// Thread-safe producer API. The actual Unreal calls are performed by
// PumpPendingClientCommands from the ProcessEvent game thread.
[[nodiscard]] bool QueueConnectToMatch(const std::string& target);
// Strict joins carry their grant in the fixed NMT_Login field2 serializer.
// The queue remains fail-closed until that pinned hook is installed and never
// falls back to an unscoped direct `open` transition.
[[nodiscard]] bool QueueConnectToMatchAuthorized(
    const std::string& target,
    std::string_view joinGrant);
// Structured result for the command channel.  A false return from the
// legacy bool wrapper cannot distinguish a real queue conflict from the
// deliberately fail-closed, unverified native NMT_Login injection path.
[[nodiscard]] AuthorizedJoinResult QueueConnectToMatchAuthorizedDetailed(
    const std::string& target,
    std::string_view joinGrant);
// The fixed-build NMT_Login serializer borrows this copy only for the
// synchronous save call.  The grant remains staged until the native travel
// operation reaches a terminal state so reliable retransmits receive the same
// signed value; callers never receive the owner string itself.
[[nodiscard]] bool CopyStagedNativeLoginGrant(std::string& grant);
[[nodiscard]] bool CopyStagedNativeSteamTicket(std::string& encodedTicket);
void MarkStagedNativeLoginGrantInjected();
void ClearStagedNativeLoginGrant();
[[nodiscard]] nlohmann::json GetClientMatchStatus();
[[nodiscard]] nlohmann::json CancelPendingClientTransition();
void ConnectToMatch();
void AutoConnectToMatchFromCmdline();
void NotifyClientLoginCompleted();
[[nodiscard]] bool IsClientLoginCompleted();
[[nodiscard]] bool IsClientLoginReadyForTravel();
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
