#pragma once

// UObject-free policy primitives shared by LoadoutManager and its state tests.
// Keeping timing/precedence here makes the asynchronous connection lifecycle
// testable without loading the generated game SDK.

#include <chrono>
#include <cstdint>
#include <optional>
#include <string>
#include <vector>

enum class LoadoutRoleConfirmDecision
{
    Ready,
    Deferred,
    Fallback,
};

namespace LoadoutStatePolicy
{
    using Clock = std::chrono::steady_clock;
    using TimePoint = Clock::time_point;

    struct PendingRoleConfirmation
    {
        bool Active = false;
        bool Replaying = false;
        std::string RoleId;
        TimePoint Deadline{};
        LoadoutRoleConfirmDecision ReplayDecision = LoadoutRoleConfirmDecision::Fallback;
    };

    enum class EffectiveSource
    {
        RuntimeOverride,
        MetaserverBaseline,
        NativeDefault,
    };

    struct ConnectionIdentity
    {
        std::string PlayerId;
        std::uint64_t Generation = 0;
        std::uint64_t ServerEpoch = 0;

        bool operator==(const ConnectionIdentity& other) const
        {
            return PlayerId == other.PlayerId &&
                Generation == other.Generation &&
                ServerEpoch == other.ServerEpoch;
        }
    };

    struct InventoryEntry
    {
        int Slot = 0;
        std::string ItemId;
    };

    // Unreal's TMap iteration order is not stable across RPC publication and
    // later inspection. Compare the inventory as a multiset of slot/item
    // pairs so ordering alone cannot suppress the post-spawn detail overlay.
    inline bool SameInventoryEntries(
        const std::vector<InventoryEntry>& left,
        const std::vector<InventoryEntry>& right)
    {
        if (left.size() != right.size()) return false;
        std::vector<bool> matched(right.size(), false);
        for (const auto& expected : left)
        {
            bool found = false;
            for (std::size_t index = 0; index < right.size(); ++index)
            {
                if (!matched[index] && expected.Slot == right[index].Slot &&
                    expected.ItemId == right[index].ItemId)
                {
                    matched[index] = true;
                    found = true;
                    break;
                }
            }
            if (!found) return false;
        }
        return true;
    }

    inline bool IsResponseCurrent(
        const std::optional<ConnectionIdentity>& active,
        const ConnectionIdentity& response)
    {
        return active.has_value() && *active == response;
    }

    inline EffectiveSource ChooseEffectiveSource(bool hasRuntimeOverride, bool hasBaseline)
    {
        if (hasRuntimeOverride) return EffectiveSource::RuntimeOverride;
        if (hasBaseline) return EffectiveSource::MetaserverBaseline;
        return EffectiveSource::NativeDefault;
    }

    inline std::chrono::milliseconds RetryDelay(unsigned int failedAttempts)
    {
        switch (failedAttempts)
        {
        case 1: return std::chrono::milliseconds(500);
        case 2: return std::chrono::seconds(2);
        case 3: return std::chrono::seconds(10);
        default: return std::chrono::seconds(30);
        }
    }

    inline LoadoutRoleConfirmDecision BeginRoleConfirmation(
        PendingRoleConfirmation& pending,
        const std::string& roleId,
        bool hasBaseline,
        bool fetchSettled,
        TimePoint now,
        std::chrono::milliseconds grace = std::chrono::seconds(1))
    {
        if (pending.Replaying && pending.RoleId == roleId)
            return pending.ReplayDecision;
        if (hasBaseline)
            return LoadoutRoleConfirmDecision::Ready;
        if (fetchSettled)
            return LoadoutRoleConfirmDecision::Fallback;

        // Retransmission must not extend the one-second grace indefinitely.
        if (pending.Active && pending.RoleId == roleId)
            return LoadoutRoleConfirmDecision::Deferred;

        pending.Active = true;
        pending.Replaying = false;
        pending.RoleId = roleId;
        pending.Deadline = now + grace;
        return LoadoutRoleConfirmDecision::Deferred;
    }

    inline std::optional<LoadoutRoleConfirmDecision> PollRoleConfirmation(
        PendingRoleConfirmation& pending,
        bool hasBaseline,
        bool fetchSettled,
        TimePoint now)
    {
        if (!pending.Active || pending.Replaying)
            return std::nullopt;

        LoadoutRoleConfirmDecision decision = LoadoutRoleConfirmDecision::Deferred;
        if (hasBaseline)
            decision = LoadoutRoleConfirmDecision::Ready;
        else if (fetchSettled || now >= pending.Deadline)
            decision = LoadoutRoleConfirmDecision::Fallback;
        if (decision == LoadoutRoleConfirmDecision::Deferred)
            return std::nullopt;

        pending.Replaying = true;
        pending.ReplayDecision = decision;
        return decision;
    }

    inline void CompleteRoleConfirmation(PendingRoleConfirmation& pending)
    {
        pending.Active = false;
        pending.Replaying = false;
    }
}
