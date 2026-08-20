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
}

int main()
{
    TestInitialMatchStateWaitsForNativeStart();
    TestRolePromptFollowsUiSync();
    std::cout << "join UI sync policy tests passed\n";
    return 0;
}
