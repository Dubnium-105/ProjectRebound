#pragma once

#include <string>

namespace SDK
{
    class UObject;
    class UWorld;
}

namespace BattleLog
{
    enum class ProcessSide
    {
        Server,
        Client,
    };

    // Must be called on the game thread after the original ProcessEvent returns.
    // The extractor reads UObject state synchronously and only writes detached JSON.
    void OnProcessEventPost(
        ProcessSide side,
        SDK::UObject* object,
        const std::string& functionName,
        void* parms);

    // Explicit generation reset for seamless travel. Pointer comparison alone
    // is insufficient because a new match can reuse a UWorld address.
    void ResetForMatchGeneration(SDK::UWorld* world);
}
