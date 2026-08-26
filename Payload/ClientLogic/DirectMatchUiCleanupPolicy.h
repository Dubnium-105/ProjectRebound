#pragma once

#include <cstddef>
#include <string_view>

namespace DirectMatchUiCleanupPolicy
{
    inline constexpr std::size_t MaxFrontendWidgets = 8;
    inline constexpr std::size_t MaxRetainedMatchWidgets = 24;

    inline bool IsRetainedSourceMatchWidget(std::string_view fullName)
    {
        // These top-level UMG roots were observed still attached to the
        // viewport after the retained PlayerController had already reached a
        // no-Pawn destination role-selection state. Match by the generated
        // class token, not a broad HUD/result substring: role selection and
        // in-game menus share the same persistent LocalPlayer viewport.
        return fullName.starts_with("HelmetHUDContainer_C ") ||
            fullName.starts_with("UMG_InGameHUD_Mother_C ") ||
            fullName.starts_with("UMG_InGameHUD_TopScoreBar_TDM_C ") ||
            fullName.starts_with("UMG_InGameTopScore_TDM_C ") ||
            fullName.starts_with("UMG_MatchState_C ") ||
            fullName.starts_with("Effect_WinBoard_C ") ||
            fullName.starts_with("UMG_EndGameScoreboardPage_C ");
    }

    inline bool IsOwnedSeamlessDestinationStartEvent(
        std::string_view fullFunctionName)
    {
        // These are the first reliable client RPCs sent after the destination
        // world, retained controller and retained HUD have all become usable.
        // Keep the list narrow: source-match result callbacks must remain
        // visible for their native duration and must never trigger cleanup.
        return fullFunctionName.find(
                   "PBPlayerController.ClientStartOnlineGame") !=
                   std::string_view::npos ||
            fullFunctionName.find(
                   "PBPlayerController.ClientMatchHasStarted") !=
                   std::string_view::npos ||
            fullFunctionName.find(
                   "PBPlayerController.ClientRoundHasStarted") !=
                   std::string_view::npos ||
            fullFunctionName.find(
                   "PBPlayerController.NotifyGameStarted") !=
                   std::string_view::npos;
    }

    inline bool IsOwnedSeamlessTravelEvent(
        std::string_view fullFunctionName)
    {
        // The pinned client receives the server's ClientTravel wrapper as the
        // reflected ClientTravelInternal RPC. Do not use a broad ClientTravel
        // substring: it would also admit sibling/internal call paths.
        constexpr std::string_view suffix =
            "PlayerController.ClientTravelInternal";
        return fullFunctionName.size() >= suffix.size() &&
            fullFunctionName.ends_with(suffix);
    }

    inline bool IsNativeDeathEvent(std::string_view fullFunctionName)
    {
        constexpr std::string_view suffix =
            "PBPlayerController.ClientBeKilled";
        return fullFunctionName.size() >= suffix.size() &&
            fullFunctionName.ends_with(suffix);
    }

    inline bool IsNativePlayableRestartEvent(
        std::string_view fullFunctionName,
        const bool hasNewPawn)
    {
        constexpr std::string_view suffix =
            "PlayerController.ClientRestart";
        return hasNewPawn && fullFunctionName.size() >= suffix.size() &&
            fullFunctionName.ends_with(suffix);
    }

    inline bool IsDirectMatchFrontendWidget(std::string_view fullName)
    {
        // These are the three persistent frontend layers observed on the
        // fixed Boundary build before a successful main-menu login. Do not
        // broaden this list to prompts or in-game menus: they live in sibling
        // CommonUI stacks owned by the same LocalPlayer subsystem.
        return fullName.find("UMG_MainMenuBase_C") != std::string_view::npos ||
            fullName.find("UMG_LoginGate_C") != std::string_view::npos ||
            fullName.find("UMG_Login_C") != std::string_view::npos ||
            fullName.find("UMG_EnterGame_C") != std::string_view::npos;
    }
}
