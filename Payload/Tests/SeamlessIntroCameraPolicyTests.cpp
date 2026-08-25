#include "../ClientLogic/SeamlessIntroCameraPolicy.h"

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
}

int main()
{
    using namespace SeamlessIntroCameraPolicy;

    Expect(IsNativeIntroCompletionEvent(
        "Function PBGameState_Rush_BP.PBGameState_Rush_BP_C.K2_RoundHasStarted"),
        "the mode-specific GameState round-start event must be the recovery boundary");
    Expect(!IsNativeIntroCompletionEvent(
        "Function ProjectBoundary.PBPlayerController.K2_RoundHasStarted"),
        "the PlayerController sibling event must not broaden the boundary");
    Expect(!IsNativeIntroCompletionEvent(
        "Function PBGameState_BP.PBGameState_BP_C.K2_StartCountdownToStart"),
        "the GameState countdown event is still too early on OSS");
    Expect(!IsNativeIntroCompletionEvent(
        "Function PBGameState_BP.PBGameState_BP_C.K2_StartMatchIntro"),
        "the beginning of MatchIntro is too early for camera recovery");
    Expect(IsNativeCameraSettleEvent(
        "Function PlayerCameraMgr_BP.PlayerCameraMgr_BP_C.ReceiveTick"),
        "the first PlayerCameraManager tick after round start must settle the camera");
    Expect(!IsNativeCameraSettleEvent(
        "Function Engine.Actor.ReceiveTick"),
        "an unrelated actor tick must not broaden the camera boundary");
    Expect(!IsNativeCameraSettleEvent(
        "Function PBMVPCineCameraComponent_BP.PBMVPCineCameraComponent_BP_C.ReceiveTick"),
        "a cinematic component tick is not the owning camera-manager boundary");

    Expect(Decide(true, true, true, true, true, true, true) ==
            ERecoveryDecision::StopThirdPersonCamera,
        "a settled destination camera with controller ViewTarget must recover");
    Expect(Decide(true, true, true, true, true, true, false) ==
            ERecoveryDecision::Wait,
        "a remote opening cinematic must keep ownership until its POV returns to the Pawn");
    Expect(Decide(true, true, true, true, true, false, true) ==
            ERecoveryDecision::Wait,
        "death-camera proximity must never be mistaken for opening-camera completion");
    Expect(Decide(true, true, true, true, true, true, true) ==
            ERecoveryDecision::StopThirdPersonCamera,
        "Pawn ViewTarget alone must not preserve a behind-the-body camera");
    Expect(Decide(false, true, true, true, true, true, true) ==
            ERecoveryDecision::Wait,
        "the direct-connect first map must never enter seamless recovery");
    Expect(Decide(true, true, false, false, false, false, true) ==
            ERecoveryDecision::Wait,
        "camera recovery must not replace possession or spawn readiness");
    Expect(Decide(true, true, true, false, true, true, true) ==
            ERecoveryDecision::Wait,
        "an unacknowledged Pawn must not receive a camera transition");
    Expect(Decide(true, true, true, true, false, true, true) ==
            ERecoveryDecision::Wait,
        "PBCharacter must match the possessed Pawn before recovery");

    std::cout << "seamless intro camera policy tests passed\n";
    return 0;
}
