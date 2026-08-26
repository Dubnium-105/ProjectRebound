#include "../ClientLogic/DirectMatchUiCleanupPolicy.h"

#include <cstdlib>
#include <iostream>
#include <string_view>

namespace
{
    void Expect(bool condition, std::string_view message)
    {
        if (!condition)
        {
            std::cerr << "FAIL: " << message << '\n';
            std::exit(EXIT_FAILURE);
        }
    }
}

int main()
{
    using DirectMatchUiCleanupPolicy::IsDirectMatchFrontendWidget;
    using DirectMatchUiCleanupPolicy::IsNativeDeathEvent;
    using DirectMatchUiCleanupPolicy::IsNativePlayableRestartEvent;
    using DirectMatchUiCleanupPolicy::IsOwnedSeamlessDestinationStartEvent;
    using DirectMatchUiCleanupPolicy::IsOwnedSeamlessTravelEvent;
    using DirectMatchUiCleanupPolicy::IsRetainedSourceMatchWidget;

    Expect(IsOwnedSeamlessTravelEvent(
        "Function Engine.PlayerController.ClientTravelInternal"),
        "the pinned reflected seamless travel RPC must arm destination cleanup");
    Expect(!IsOwnedSeamlessTravelEvent(
        "Function Engine.PlayerController.ClientTravel"),
        "the wrapper sibling must not broaden the exact travel hook gate");
    Expect(IsNativeDeathEvent(
        "Function ProjectBoundary.PBPlayerController.ClientBeKilled"),
        "native client death must arm a death-HUD cleanup generation");
    Expect(IsNativePlayableRestartEvent(
        "Function Engine.PlayerController.ClientRestart", true),
        "a native restart with a Pawn must complete death-HUD cleanup");
    Expect(!IsNativePlayableRestartEvent(
        "Function Engine.PlayerController.ClientRestart", false),
        "the native null-Pawn restart must not consume death-HUD cleanup");
    Expect(!IsNativePlayableRestartEvent(
        "Function Engine.PlayerController.ClientRetryClientRestart", true),
        "retry RPCs must not broaden the successful restart boundary");

    Expect(IsOwnedSeamlessDestinationStartEvent(
        "Function ProjectBoundary.PBPlayerController.ClientStartOnlineGame"),
        "the first destination start RPC must be an eligible cleanup boundary");
    Expect(IsOwnedSeamlessDestinationStartEvent(
        "Function ProjectBoundary.PBPlayerController.ClientMatchHasStarted"),
        "match-start must retry cleanup when the HUD was initially unavailable");
    Expect(IsOwnedSeamlessDestinationStartEvent(
        "Function ProjectBoundary.PBPlayerController.ClientRoundHasStarted"),
        "round-start must remain an eligible bounded retry");
    Expect(IsOwnedSeamlessDestinationStartEvent(
        "Function ProjectBoundary.PBPlayerController.NotifyGameStarted"),
        "notify-game-started must be the final eligible retry");
    Expect(!IsOwnedSeamlessDestinationStartEvent(
        "Function ProjectBoundary.PBGameState.K2_StartShowingMatchResult"),
        "source result presentation must not be hidden early");
    Expect(!IsOwnedSeamlessDestinationStartEvent(
        "Function Engine.PlayerController.ClientTravelInternal"),
        "travel dispatch must arm cleanup rather than consume it");

    Expect(IsRetainedSourceMatchWidget(
        "HelmetHUDContainer_C Transient.PBCharacter_BP_C_1.HelmetHUDContainer_C_0"),
        "the source Pawn's helmet HUD root must be retired at destination start");
    Expect(!IsRetainedSourceMatchWidget(
        "HUD_QuickRespawnTips_C Transient.PBGameInstance_C_1.HUD_QuickRespawnTips_C_0"),
        "the reusable quick-respawn root must remain owned by PlayerHUD_BP");
    Expect(IsRetainedSourceMatchWidget(
        "UMG_InGameHUD_TopScoreBar_TDM_C Transient.UMG_InGameHUD_TopScoreBar_TDM_C_0"),
        "the source match score root must not survive seamless travel");
    Expect(IsRetainedSourceMatchWidget(
        "UMG_MatchState_C Transient.UMG_MatchState_C_0"),
        "the source match-state root must not survive seamless travel");
    Expect(IsRetainedSourceMatchWidget(
        "Effect_WinBoard_C Transient.Effect_WinBoard_C_0"),
        "the source win-board result root must not survive seamless travel");
    Expect(IsRetainedSourceMatchWidget(
        "UMG_EndGameScoreboardPage_C Transient.UMG_EndGameScoreboardPage_C_0"),
        "the source end-game scoreboard root must not survive seamless travel");
    Expect(!IsRetainedSourceMatchWidget(
        "UMG_InGameSelectRole_C Transient.UMG_InGameSelectRole_C_0"),
        "the destination role-selection layer must be preserved");
    Expect(!IsRetainedSourceMatchWidget(
        "UMG_InGameOption_V2_C Transient.UMG_InGameOption_V2_C_0"),
        "the in-game menu must not be detached by match-layer cleanup");
    Expect(!IsRetainedSourceMatchWidget(
        "UMG_MatchMessage_C Transient.UMG_MatchMessage_C_0"),
        "the retained APBHUD message widget is handled by its owner");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_MainMenuBase_C /Engine/Transient.GameEngine_1.UMG_MainMenuBase_C_0"),
        "the main-menu layer must be hidden before direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_LoginGate_C /Engine/Transient.GameEngine_1.UMG_LoginGate_C_0"),
        "the platform-login gate must not survive direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_EnterGame_C /Engine/Transient.GameEngine_1.UMG_EnterGame_C_0"),
        "the press-to-start layer must not survive direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_Login_C /Engine/Transient.GameEngine_1.UMG_Login_C_0"),
        "the platform-login layer must not survive direct travel");

    Expect(!IsDirectMatchFrontendWidget(
        "UMG_InGameOption_V2_C /Engine/Transient.GameEngine_1.UMG_InGameOption_V2_C_0"),
        "the in-game ESC menu must be preserved");
    Expect(!IsDirectMatchFrontendWidget(
        "ConfirmPage_C /Engine/Transient.GameEngine_1.ConfirmPage_C_0"),
        "prompt widgets must be preserved");
    Expect(!IsDirectMatchFrontendWidget("PBMainMenuManager_BP_C /Engine/Transient"),
        "the manager itself is not a cleanup target");
    Expect(!IsDirectMatchFrontendWidget(""),
        "an unknown widget must stop targeted cleanup");

    std::cout << "Direct-match UI cleanup policy tests passed\n";
    return EXIT_SUCCESS;
}
