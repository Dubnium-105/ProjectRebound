#pragma once

#include <cstdint>
#include <string>
#include <string_view>
#include "../Libs/json.hpp"
#include "NativeMatchScope.h"

namespace SDK
{
    class APBPlayerController;
}

struct AuthorizedJoinResult
{
    bool accepted = false;
    std::string code;
    std::string message;
    // Correlates the join ACK with the one staged native transition.  It is
    // never reused for another Grant or world.
    std::uint64_t operationSequence = 0;
};

// Thread-safe producer API. The actual Unreal calls are performed by
// PumpPendingClientCommands from the ProcessEvent game thread.
[[nodiscard]] bool QueueConnectToMatch(const std::string& target);
// Strict joins carry their grant in the fixed NMT_Login field2 serializer.
// The queue remains fail-closed until that pinned hook is installed and never
// falls back to an unscoped direct `open` transition.
// Strict online joins must carry the frozen scope delivered by the guarded
// Toolbox pipe alongside the opaque Grant.  Payload does not parse that Grant
// as a source of trust; it binds this correlation scope to this operation and
// waits for the later exact backend/native confirmation.
[[nodiscard]] AuthorizedJoinResult QueueConnectToMatchAuthorizedDetailed(
    const std::string& target,
    std::string_view joinGrant,
    const nlohmann::json& expectedScope);
// P2P HOST uses its local native socket rather than a remote Grant. The
// explicit HOST scope must carry room_role=HOST and an empty grant_jti; the
// native nonce is the exact nonce returned by StartAuthorityForAllocatedHost.
// This only stages the local-authority observation. Playable still requires
// the later scoped backend HOST CONNECTED confirmation plus local readiness.
[[nodiscard]] nlohmann::json StageLocalAuthorityClientScope(
    const nlohmann::json& hostScope,
    std::string_view nativeConnectionNonce);
// Called by the guarded command channel after Backend ConfirmConnected and
// the native authority have returned the exact scope/nonce.  The command is
// idempotent for the same operation sequence and rejects stale or mismatched
// scope data.
[[nodiscard]] nlohmann::json ConfirmClientMatchConnection(
    const nlohmann::json& arguments);
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
