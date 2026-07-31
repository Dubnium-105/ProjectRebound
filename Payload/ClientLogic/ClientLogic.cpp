#include "ClientLogic.h"

#include "../Communication/CommandProtocol.h"
#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"

#include <chrono>
#include <mutex>
#include <optional>
#include <utility>

using namespace SDK;

std::atomic<bool> LoginCompleted{false};

namespace
{
    enum class ConnectStage
    {
        Idle,
        Queued,
        WaitingAfterLogin,
        WaitingAfterRange
    };

    std::mutex connectMutex;
    std::optional<std::string> pendingTarget;
    std::string currentTarget;
    ConnectStage connectStage = ConnectStage::Idle;
    std::chrono::steady_clock::time_point nextActionAt{};

    constexpr auto LoginSettleDelay = std::chrono::seconds(2);
    constexpr auto RangeSettleDelay = std::chrono::seconds(1);
}

bool QueueConnectToMatch(const std::string& target)
{
    std::string validationError;
    if (!CommandProtocol::ValidateMatchTarget(target, &validationError))
    {
        ClientLog("[CLIENT] Rejected match target: " + validationError);
        return false;
    }

    std::lock_guard<std::mutex> lock(connectMutex);
    if (pendingTarget.has_value())
        return false;

    pendingTarget = target;
    connectStage = ConnectStage::Queued;
    ClientLog("[CLIENT] Match transition queued: " + target);
    return true;
}

void ConnectToMatch()
{
    std::string target;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        target = currentTarget;
    }

    if (target.empty())
    {
        ClientLog("[CLIENT] Reconnect requested without a current match target.");
        return;
    }
    if (!QueueConnectToMatch(target))
        ClientLog("[CLIENT] Reconnect ignored because another transition is pending.");
}

void AutoConnectToMatchFromCmdline()
{
    if (!MatchIP.empty() && !QueueConnectToMatch(MatchIP))
        ClientLog("[CLIENT] Initial match target could not be queued.");
}

void PumpPendingClientCommands()
{
    static thread_local bool pumping = false;
    if (pumping || !LoginCompleted.load())
        return;

    UWorld* const world = UWorld::GetWorld();
    if (world == nullptr || world->OwningGameInstance == nullptr ||
        world->OwningGameInstance->LocalPlayers.Num() == 0)
    {
        return;
    }

    const auto now = std::chrono::steady_clock::now();
    bool enterRange = false;
    std::optional<std::string> connectTarget;

    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (!pendingTarget.has_value())
            return;

        if (connectStage == ConnectStage::Queued)
        {
            connectStage = ConnectStage::WaitingAfterLogin;
            nextActionAt = now + LoginSettleDelay;
            return;
        }
        if (now < nextActionAt)
            return;

        if (connectStage == ConnectStage::WaitingAfterLogin)
        {
            connectStage = ConnectStage::WaitingAfterRange;
            nextActionAt = now + RangeSettleDelay;
            enterRange = true;
        }
        else if (connectStage == ConnectStage::WaitingAfterRange)
        {
            connectTarget = std::move(pendingTarget);
            currentTarget = *connectTarget;
            pendingTarget.reset();
            connectStage = ConnectStage::Idle;
        }
    }

    pumping = true;
    try
    {
        auto* const localPlayer = static_cast<UPBLocalPlayer*>(
            world->OwningGameInstance->LocalPlayers[0]);

        if (enterRange)
        {
            if (localPlayer != nullptr)
            {
                ClientLog("[CLIENT] Entering Shooting Range before match transition...");
                localPlayer->GoToRange(0.0f);
            }
        }
        else if (connectTarget.has_value())
        {
            const std::wstring command = L"open " +
                std::wstring(connectTarget->begin(), connectTarget->end());
            ClientLog("[CLIENT] Connecting to match: " + *connectTarget);
            UKismetSystemLibrary::ExecuteConsoleCommand(world, command.c_str(), nullptr);
        }
    }
    catch (...)
    {
        ClientLog("[CLIENT] Match transition failed on the game thread.");
    }
    pumping = false;
}
