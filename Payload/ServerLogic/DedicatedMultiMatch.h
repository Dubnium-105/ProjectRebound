#pragma once

#include <string>

#include "../Libs/json.hpp"

namespace SDK
{
    class APBGameMode;
    class APBPlayerController;
    class UNetDriver;
}

namespace DedicatedMultiMatch
{
    enum class EngineBrowseInterceptResult
    {
        PassThrough,
        HandledSuccess,
        HandledFailure,
    };

    // Load and validate the opt-in configuration after Config has been loaded.
    // Invalid or incomplete configuration leaves the native process-per-match
    // lifecycle active.
    void Initialize();

    bool IsEnabled();
    bool OwnsWorldTransition();
    // Called only from the pinned UGameEngine::Browse detour.  The opaque
    // context is an FWorldContext whose World member is validated against the
    // exact source World captured when native ServerTravel was queued.
    EngineBrowseInterceptResult InterceptEngineBrowse(void* worldContext);
    // UGameEngine keeps ticking while the listening driver is temporarily
    // detached during seamless travel.  This post-tick boundary performs the
    // narrowly validated destination rebind before network flushing resumes.
    void OnGameEnginePostTick();
    void Tick(float deltaSeconds, SDK::UNetDriver* tickNetDriver);
    void OnShowingMatchResult(SDK::APBGameMode* gameMode);
    bool HandleWaitingToEndGame(SDK::APBGameMode* gameMode);
    bool ShouldSuppressRetiredEndMatch(SDK::APBGameMode* gameMode);
    // Continue through the pinned result freezer's ordinary no-MVP path only
    // for the current authority of an owned multi-match session.
    bool ShouldBypassNullResultMvp(SDK::APBGameMode* gameMode);
    // Seamless travel destroys the retired source GameMode after the
    // destination has committed.  The pinned PB GameMode EndPlay path can
    // re-enter its dedicated final-cleanup routine directly, bypassing
    // WaitingToEndGame and requesting process exit.  Suppress that terminal
    // cleanup only while multi-match still owns the process lifecycle.
    bool ShouldSuppressNativeFinalCleanup(SDK::APBGameMode* gameMode);
    bool HandleServerSay(
        SDK::APBPlayerController* playerController,
        const std::string& message);
    void OnPlayerDisconnected(SDK::APBPlayerController* playerController);

    nlohmann::json BuildStatusPayload();
}
