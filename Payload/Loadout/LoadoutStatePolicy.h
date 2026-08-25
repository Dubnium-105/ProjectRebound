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

    // Only seamless rebound players use this gate.  Their controller and
    // authoritative role survive travel, while LoadoutManager intentionally
    // starts a fresh per-match connection record.  Do not dispatch the new
    // Pawn until the remote controller has completed its native seamless
    // travel handshake and that record has either restored its baseline or
    // reached a bounded native-fallback deadline.  Callers must then rebuild
    // the destination PlayerState role through native confirmation before
    // dispatching RestartPlayers. The baseline fallback must never bypass
    // ServerNotifyLoadedWorld completion.
    inline bool CanReleaseSeamlessRoleSpawn(
        bool gateActive,
        bool fetchSettled,
        bool clientTravelCompleted,
        TimePoint now,
        TimePoint deadline)
    {
        return !gateActive ||
            (clientTravelCompleted && (fetchSettled || now >= deadline));
    }

    // Destination FieldMod configs and native indices are reconstructed from
    // the fresh baseline through the pinned ClientInitFieldMod body, but the
    // build can clear them again while completing the remote client's seamless
    // travel handshake. Never rebuild before ServerNotifyLoadedWorld completes,
    // and never do so for ordinary/P2P spawn or without a usable baseline.
    inline bool CanAttemptSeamlessFieldModRoleSeed(
        bool gateActive,
        bool fetchCompleted,
        bool clientTravelCompleted,
        bool hasBaselines)
    {
        return gateActive && fetchCompleted && clientTravelCompleted &&
            hasBaselines;
    }

    // A destination that keeps PlayerState but deliberately asks for a fresh
    // role must not enter either ServerPreOrderInventory or
    // ServerConfirmRoleSelection until ClientInitFieldMod has reconstructed
    // the retained native containers. Ordinary first joins remain untouched.
    inline bool CanDispatchFreshSeamlessRoleConfirmation(
        bool freshSelectionActive,
        bool fieldModRolesSeeded)
    {
        return !freshSelectionActive || fieldModRolesSeeded;
    }

    // The pinned build's reliable-RPC validator consults a transient hidden
    // role set that is not reconstructed by ClientInitFieldMod after seamless
    // travel. Bypass that validator only for the exact synchronous recovery
    // transaction whose canonical role and pre-order state were already
    // checked by CanReleaseRoleSpawn. The native RPC implementation still
    // performs the inventory merge, SelectedCharacterID write, and restart.
    inline bool CanBypassSeamlessRoleValidator(
        bool recoveryGuardActive,
        bool gateActive,
        bool sameController,
        bool sameCanonicalRole,
        bool nativePreOrderReady)
    {
        return recoveryGuardActive && gateActive && sameController &&
            sameCanonicalRole && nativePreOrderReady;
    }

    // The destination validator's hidden allowed-role set can remain stale
    // even after ClientInitFieldMod has rebuilt the visible FieldMod state.
    // A user-driven fresh selection may bypass it only when this exact
    // controller is still in the fresh-owned-travel transaction and the
    // requested FName is already present in the seeded native pre-order map.
    inline bool CanBypassFreshSeamlessRoleValidator(
        bool freshSelectionActive,
        bool fieldModRolesSeeded,
        bool requestedRolePresent)
    {
        return freshSelectionActive && fieldModRolesSeeded &&
            requestedRolePresent;
    }
}
