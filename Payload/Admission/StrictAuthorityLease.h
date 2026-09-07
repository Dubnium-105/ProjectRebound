#pragma once

#include "StrictRosterPolicy.h"

#include <string_view>
#include <atomic>
#include <limits>

namespace StrictAuthorityLease
{
    class MutationLease
    {
    public:
        explicit MutationLease(std::atomic_bool& owner) noexcept
            : owner_(owner), acquired_(!owner.exchange(true, std::memory_order_acq_rel)) {}
        ~MutationLease()
        {
            if (acquired_)
                owner_.store(false, std::memory_order_release);
        }
        explicit operator bool() const noexcept { return acquired_; }
        MutationLease(const MutationLease&) = delete;
        MutationLease& operator=(const MutationLease&) = delete;
    private:
        std::atomic_bool& owner_;
        bool acquired_;
    };

    enum class Decision
    {
        Unavailable,
        WorldUnavailable,
        SameRouteReplay,
        P2PRouteRecovery,
        RouteConflict,
    };

    inline bool OwnedWorldMatches(
        const void* currentWorld, const std::string_view currentId,
        const void* ownedWorld, const std::string_view ownedId) noexcept
    {
        return currentWorld && ownedWorld && currentWorld == ownedWorld &&
            !ownedId.empty() && currentId == ownedId;
    }

    inline bool SameIdentity(
        const StrictRoster::AllocationScope& left,
        const StrictRoster::AllocationScope& right) noexcept
    {
        return left.attemptId == right.attemptId &&
            left.authoritySessionId == right.authoritySessionId &&
            left.rosterRevision == right.rosterRevision;
    }

    // Pure scope classification used by the pipe callback.  It never inspects
    // UWorld or changes policy state: worldListening/sameWorld are the
    // game-thread snapshot supplied by the caller.
    inline Decision Classify(
        const bool leaseActive,
        const std::string_view leaseHostingKind,
        const StrictRoster::AllocationScope& leaseScope,
        const std::string_view requestedHostingKind,
        const StrictRoster::AllocationScope& requestedScope,
        const bool worldListening,
        const bool sameWorld) noexcept
    {
        if (!leaseActive || leaseHostingKind != requestedHostingKind ||
            !SameIdentity(leaseScope, requestedScope))
            return Decision::Unavailable;
        if (!worldListening || !sameWorld)
            return Decision::WorldUnavailable;
        if (leaseScope.routeGeneration == requestedScope.routeGeneration)
            return Decision::SameRouteReplay;
        if (requestedHostingKind == "P2P" &&
            leaseScope.routeGeneration < (std::numeric_limits<int>::max)() &&
            requestedScope.routeGeneration == leaseScope.routeGeneration + 1)
            return Decision::P2PRouteRecovery;
        return Decision::RouteConflict;
    }
}
