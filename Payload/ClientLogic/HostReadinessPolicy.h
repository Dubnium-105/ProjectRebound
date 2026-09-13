#pragma once

namespace HostReadinessPolicy
{
    // These are the only native facts exposed by the HOST readiness
    // diagnostic.  It deliberately carries no UObject pointer, world
    // identity, nonce, token, or other match credential.
    struct Sample
    {
        bool worldPresent = false;
        bool gameStatePresent = false;
        bool netDriverPresent = false;
        bool driverWorldMatches = false;
        bool authorityScopeMatches = false;
        bool owningGameInstancePresent = false;
        bool localPlayersPresent = false;

        [[nodiscard]] constexpr bool IsWorldReady() const noexcept
        {
            return worldPresent && gameStatePresent && netDriverPresent &&
                driverWorldMatches && authorityScopeMatches;
        }
    };

    enum class LossReason
    {
        WorldMissing,
        GameStateMissing,
        NetDriverMissing,
        DriverWorldMismatch,
        AuthorityScopeMismatch,
        WorldNotReady
    };

    inline constexpr LossReason FirstLossReason(const Sample& sample) noexcept
    {
        if (!sample.worldPresent)
            return LossReason::WorldMissing;
        if (!sample.gameStatePresent)
            return LossReason::GameStateMissing;
        if (!sample.netDriverPresent)
            return LossReason::NetDriverMissing;
        if (!sample.driverWorldMatches)
            return LossReason::DriverWorldMismatch;
        if (!sample.authorityScopeMatches)
            return LossReason::AuthorityScopeMismatch;
        return LossReason::WorldNotReady;
    }

    inline constexpr const char* LossReasonName(const LossReason reason) noexcept
    {
        switch (reason)
        {
        case LossReason::WorldMissing: return "world_missing";
        case LossReason::GameStateMissing: return "game_state_missing";
        case LossReason::NetDriverMissing: return "net_driver_missing";
        case LossReason::DriverWorldMismatch: return "driver_world_mismatch";
        case LossReason::AuthorityScopeMismatch:
            return "authority_scope_mismatch";
        case LossReason::WorldNotReady: return "world_not_ready";
        }
        return "world_not_ready";
    }

    struct Observation
    {
        bool observed = false;
        Sample current{};
        Sample ever{};
        bool everWorldReady = false;
        bool firstNotReadyObserved = false;
        Sample firstNotReady{};
        LossReason firstNotReadyReason = LossReason::WorldNotReady;
        bool firstLostObserved = false;
        Sample firstLost{};
        LossReason firstLostReason = LossReason::WorldNotReady;
    };

    inline void Observe(Observation& observation, const Sample& sample) noexcept
    {
        observation.observed = true;
        observation.current = sample;
        observation.ever.worldPresent |= sample.worldPresent;
        observation.ever.gameStatePresent |= sample.gameStatePresent;
        observation.ever.netDriverPresent |= sample.netDriverPresent;
        observation.ever.driverWorldMatches |= sample.driverWorldMatches;
        observation.ever.authorityScopeMatches |= sample.authorityScopeMatches;
        observation.ever.owningGameInstancePresent |= sample.owningGameInstancePresent;
        observation.ever.localPlayersPresent |= sample.localPlayersPresent;
        if (sample.IsWorldReady())
        {
            observation.everWorldReady = true;
            return;
        }
        if (!observation.firstNotReadyObserved)
        {
            observation.firstNotReadyObserved = true;
            observation.firstNotReady = sample;
            observation.firstNotReadyReason = FirstLossReason(sample);
        }
        if (observation.everWorldReady && !observation.firstLostObserved)
        {
            observation.firstLostObserved = true;
            observation.firstLost = sample;
            observation.firstLostReason = FirstLossReason(sample);
        }
    }
}
