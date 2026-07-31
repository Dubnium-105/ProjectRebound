#pragma once

#include <string>

namespace SDK
{
    class UObject;
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
}
