#include "../ClientLogic/HostReadinessPolicy.h"

#include <cstdlib>
#include <iostream>
#include <string>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAIL: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    using HostReadinessPolicy::FirstLossReason;
    using HostReadinessPolicy::LossReason;
    using HostReadinessPolicy::Observation;
    using HostReadinessPolicy::Sample;

    const Sample missingWorld{};
    Expect(!missingWorld.IsWorldReady(),
        "a missing world cannot be reported ready");
    Expect(FirstLossReason(missingWorld) == LossReason::WorldMissing,
        "the first loss reason identifies a missing world");

    const Sample ready{true, true, true, true, true, true, true};
    Expect(ready.IsWorldReady(),
        "all native HOST readiness facts are required for world readiness");
    const Sample noLocalGameContext{true, true, true, true, true, false, false};
    Expect(noLocalGameContext.IsWorldReady(),
        "game-instance diagnostics must not change the existing world gate");

    Observation observation;
    HostReadinessPolicy::Observe(observation, missingWorld);
    Expect(observation.firstNotReadyObserved && !observation.firstLostObserved,
        "an initially missing world is first-not-ready, not a lost world");
    HostReadinessPolicy::Observe(observation, ready);
    Expect(observation.observed && observation.current.IsWorldReady(),
        "the current sample follows the latest game-thread observation");
    Expect(observation.everWorldReady,
        "ever_world_ready records a complete ready sample");
    Expect(observation.ever.worldPresent && observation.ever.gameStatePresent &&
        observation.ever.netDriverPresent && observation.ever.driverWorldMatches &&
        observation.ever.authorityScopeMatches &&
        observation.ever.owningGameInstancePresent &&
        observation.ever.localPlayersPresent,
        "ever facts accumulate across a world transition");
    Expect(observation.firstNotReadyReason == LossReason::WorldMissing &&
        !observation.firstNotReady.IsWorldReady(),
        "the first-not-ready sample remains available after readiness recovers");

    Observation readyThenLost;
    HostReadinessPolicy::Observe(readyThenLost, ready);
    const Sample driverMismatch{true, true, true, false, true, true, true};
    HostReadinessPolicy::Observe(readyThenLost, driverMismatch);
    Expect(readyThenLost.firstLostObserved &&
        readyThenLost.firstLostReason == LossReason::DriverWorldMismatch &&
        !readyThenLost.firstLost.IsWorldReady(),
        "a later driver/world split is captured without changing the gate");
    Expect(readyThenLost.everWorldReady,
        "a ready-to-lost sequence records that the world was once ready");
    Expect(readyThenLost.ever.driverWorldMatches &&
        !readyThenLost.current.driverWorldMatches,
        "ever and current distinguish a recovered fact from its first loss");
    const Sample scatteredFacts{true, false, true, true, true, true, true};
    Observation scattered;
    HostReadinessPolicy::Observe(scattered, scatteredFacts);
    HostReadinessPolicy::Observe(scattered,
        Sample{true, true, false, true, true, true, true});
    Expect(scattered.ever.worldPresent && scattered.ever.gameStatePresent &&
        scattered.ever.netDriverPresent && scattered.ever.driverWorldMatches &&
        scattered.ever.authorityScopeMatches &&
        scattered.ever.owningGameInstancePresent &&
        scattered.ever.localPlayersPresent && !scattered.everWorldReady &&
        !scattered.firstLostObserved,
        "separately observed facts cannot impersonate one complete ready sample");
    Expect(std::string(HostReadinessPolicy::LossReasonName(
        LossReason::AuthorityScopeMismatch)) == "authority_scope_mismatch",
        "diagnostic reasons are fixed safe names");

    std::cout << "host readiness policy tests passed\n";
    return 0;
}
