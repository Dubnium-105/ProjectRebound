#pragma once

namespace JoinUiSyncPolicy
{
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
}
