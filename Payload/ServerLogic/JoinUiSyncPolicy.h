#pragma once

namespace JoinUiSyncPolicy
{
    inline bool ShouldForwardReadyToMatchIntro(
        bool canStartMatch,
        bool ownsWorldTransition = false)
    {
        // In the pinned build the PB GameMode tick advances WaitingToJoin to
        // MatchIntro before AGameMode::StartMatch. Forwarding this readiness
        // callback before the role quorum would start the intro too early.
        return canStartMatch && !ownsWorldTransition;
    }

    inline bool ShouldRestoreFreshDestinationReadyToMatchIntro(
        bool canStartMatch,
        bool hasFreshSeamlessInitialPlayer,
        bool initialPlayersReady,
        bool nativeReady)
    {
        // The retained connection is absent from destination PostLogin
        // bookkeeping, so the native Ready result remains false. Restore that
        // one missing bit only after RestartPlayers produced every initial
        // Pawn. Forcing it in the role-confirmation stack starts OSS combat
        // while its longer opening camera still owns the client POV.
        return canStartMatch && hasFreshSeamlessInitialPlayer &&
            initialPlayersReady && !nativeReady;
    }

    inline bool ShouldDispatchStartMatch(
        bool canStartMatch,
        bool didProcStartMatch,
        bool initialPlayersReady,
        bool matchIntroEntered,
        bool completedNativeFlushAfterMatchIntro)
    {
        // Never let the custom quorum jump directly from WaitingToJoin to
        // InProgress. MatchIntro is the replicated native boundary which owns
        // the opening camera and its eventual movement/camera release. It must
        // also survive one complete native NetDriver flush; otherwise
        // StartMatch can overwrite the replicated sub-state in the same frame
        // and the retained destination client never observes MatchIntro.
        return canStartMatch && !didProcStartMatch && initialPlayersReady &&
            matchIntroEntered && completedNativeFlushAfterMatchIntro;
    }

    inline bool IsInitialPlayerReadyForMatchStart(
        bool isInitialJoin,
        bool isSpawned)
    {
        if (!isInitialJoin)
            return true;

        // MatchIntro enumerates already spawned/possessed characters when it
        // builds the client opening-camera sequence. A retained connection in
        // a new seamless destination must therefore obey the same boundary as
        // a direct-connect player: playable Pawn first, StartMatch second.
        return isSpawned;
    }

    inline bool ShouldDeferNativeInitialRoleSelectionPrompt(
        bool isInitialJoin,
        bool clientStartSent)
    {
        return isInitialJoin && !clientStartSent;
    }

    inline bool ShouldSendInitialMatchState(
        bool isInitialJoin,
        bool didBroadcastRoleSelection,
        bool clientStartSent,
        float elapsedSeconds,
        float delaySeconds)
    {
        return isInitialJoin && didBroadcastRoleSelection &&
            !clientStartSent && elapsedSeconds >= delaySeconds;
    }

    inline bool ShouldPromptInitialRoleSelection(
        bool isInitialJoin,
        bool didBroadcastRoleSelection,
        bool clientStartSent,
        bool initialRoleSelectionSent,
        float elapsedSeconds,
        float delaySeconds)
    {
        return isInitialJoin && didBroadcastRoleSelection &&
            clientStartSent && !initialRoleSelectionSent &&
            elapsedSeconds >= delaySeconds;
    }

    inline bool ShouldRecordInitialRoleSelectionPrompt(
        bool isInitialJoin,
        bool clientStartSent)
    {
        // The server-wide native broadcast can arrive while a direct-connect
        // client is still in the persistent frontend state. That delivery is
        // hidden by the subsequent match-state sync and must not consume the
        // one retry which follows the sync.
        return isInitialJoin && clientStartSent;
    }
}
