#include "../ServerLogic/JoinUiSyncPolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }

    void TestFreshSeamlessDestinationRestoresNativeIntroBoundary()
    {
        Expect(!JoinUiSyncPolicy::ShouldForwardReadyToMatchIntro(false),
            "native MatchIntro must remain behind destination role confirmation");
        Expect(JoinUiSyncPolicy::ShouldForwardReadyToMatchIntro(true),
            "role-confirmed StartMatch may enter native MatchIntro");
        Expect(!JoinUiSyncPolicy::ShouldForwardReadyToMatchIntro(true, true),
            "a source generation gate must not advance the partial destination world");

        Expect(!JoinUiSyncPolicy::
                ShouldRestoreFreshDestinationReadyToMatchIntro(
                    true, true, false, false),
            "role confirmation must not start OSS MatchIntro before its Pawn exists");
        Expect(JoinUiSyncPolicy::
                ShouldRestoreFreshDestinationReadyToMatchIntro(
                    true, true, true, false),
            "a spawned fresh destination may restore retained PostLogin readiness");
        Expect(!JoinUiSyncPolicy::
                ShouldRestoreFreshDestinationReadyToMatchIntro(
                    true, true, true, true),
            "an already-ready destination must preserve its native result");
        Expect(!JoinUiSyncPolicy::
                ShouldRestoreFreshDestinationReadyToMatchIntro(
                    true, false, true, false),
            "a direct-connect generation must never receive the seamless override");

        Expect(!JoinUiSyncPolicy::IsInitialPlayerReadyForMatchStart(
                true, false),
            "every initial-flow player must have a Pawn before native MatchIntro enumerates it");
        Expect(JoinUiSyncPolicy::IsInitialPlayerReadyForMatchStart(
                true, true),
            "the initial flow may enter MatchIntro only after spawn completes");
        Expect(JoinUiSyncPolicy::IsInitialPlayerReadyForMatchStart(
                false, false),
            "late joins do not participate in the initial MatchIntro quorum");

        Expect(!JoinUiSyncPolicy::ShouldDispatchStartMatch(
                true, false, true, false, true),
            "StartMatch must not skip the replicated native MatchIntro boundary");
        Expect(!JoinUiSyncPolicy::ShouldDispatchStartMatch(
                true, false, true, true, false),
            "StartMatch must preserve a complete native flush for MatchIntro replication");
        Expect(JoinUiSyncPolicy::ShouldDispatchStartMatch(
                true, false, true, true, true),
            "StartMatch may run after native MatchIntro was flushed with a playable Pawn");
        Expect(!JoinUiSyncPolicy::ShouldDispatchStartMatch(
                true, false, false, true, true),
            "MatchIntro alone must not start before the playable Pawn exists");
    }

    void TestInitialMatchStateWaitsForNativeStart()
    {
        Expect(!JoinUiSyncPolicy::ShouldSendInitialMatchState(
            true, false, false, 10.0f, 1.0f),
            "initial UI sync must not announce a match before StartMatch");
        Expect(!JoinUiSyncPolicy::ShouldSendInitialMatchState(
            true, true, false, 0.5f, 1.0f),
            "initial UI sync must wait for the connection settle delay");
        Expect(JoinUiSyncPolicy::ShouldSendInitialMatchState(
            true, true, false, 1.0f, 1.0f),
            "initial UI sync should run after the native role broadcast");
        Expect(!JoinUiSyncPolicy::ShouldSendInitialMatchState(
            true, true, true, 2.0f, 1.0f),
            "initial UI sync must be connection-idempotent");
        Expect(!JoinUiSyncPolicy::ShouldSendInitialMatchState(
            false, true, false, 2.0f, 1.0f),
            "late joins use their existing client-start branch");
    }

    void TestNativeRolePromptIsDeferredUntilUiSync()
    {
        Expect(JoinUiSyncPolicy::ShouldDeferNativeInitialRoleSelectionPrompt(
            true, false),
            "the initial native prompt must wait for direct-connect UI sync");
        Expect(!JoinUiSyncPolicy::ShouldDeferNativeInitialRoleSelectionPrompt(
            true, true),
            "the initial native prompt may run after direct-connect UI sync");
        Expect(!JoinUiSyncPolicy::ShouldDeferNativeInitialRoleSelectionPrompt(
            false, false),
            "late joins use their own role-selection lifecycle");
    }

    void TestRolePromptFollowsUiSync()
    {
        Expect(!JoinUiSyncPolicy::ShouldPromptInitialRoleSelection(
            true, true, false, false, 2.0f, 1.0f),
            "role selection must not be retried under the frontend menu");
        Expect(!JoinUiSyncPolicy::ShouldPromptInitialRoleSelection(
            true, true, true, false, 0.5f, 1.0f),
            "role selection should wait for UI sync to settle");
        Expect(JoinUiSyncPolicy::ShouldPromptInitialRoleSelection(
            true, true, true, false, 1.0f, 1.0f),
            "role selection should follow the completed UI sync");
        Expect(!JoinUiSyncPolicy::ShouldPromptInitialRoleSelection(
            true, true, true, true, 2.0f, 1.0f),
            "role selection retry must be one-shot");
    }

    void TestRolePromptDeliveryIsRecordedOnlyAfterUiSync()
    {
        Expect(!JoinUiSyncPolicy::ShouldRecordInitialRoleSelectionPrompt(
            true, false),
            "a prompt hidden under the frontend state must remain retryable");
        Expect(JoinUiSyncPolicy::ShouldRecordInitialRoleSelectionPrompt(
            true, true),
            "the post-sync native prompt should consume the one-shot retry");
        Expect(!JoinUiSyncPolicy::ShouldRecordInitialRoleSelectionPrompt(
            false, true),
            "late joins do not use the initial prompt-delivery flag");
    }
}

int main()
{
    TestFreshSeamlessDestinationRestoresNativeIntroBoundary();
    TestInitialMatchStateWaitsForNativeStart();
    TestNativeRolePromptIsDeferredUntilUiSync();
    TestRolePromptFollowsUiSync();
    TestRolePromptDeliveryIsRecordedOnlyAfterUiSync();
    std::cout << "join UI sync policy tests passed\n";
    return 0;
}
