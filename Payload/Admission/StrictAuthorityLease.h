#pragma once

#include "StrictRosterPolicy.h"
#include "../ClientLogic/NativeMatchScope.h"

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

    // A new authority route does not create a new local HOST connection.
    // Correlate an explicit preservation request with the fresh game-thread
    // client snapshot; the caller must separately verify the signed live seat.
    inline std::optional<NativeClientMatchConfirmation> PreservedHost(
        const nlohmann::json& request,
        const nlohmann::json& clientStatus,
        const StrictRoster::AllocationScope& allocation,
        const std::string_view worldInstanceId,
        const int previousAuthorityRoute,
        const std::string_view publishedNonce,
        const std::uint64_t publishedOperation) noexcept
    {
        try
        {
            const auto proof = NativeClientMatchConfirmation::FromJson(request);
            if (!proof || !proof->hostScope ||
                proof->scope.attemptId != allocation.attemptId ||
                proof->scope.authoritySessionId != allocation.authoritySessionId ||
                proof->scope.worldInstanceId != worldInstanceId ||
                proof->scope.rosterRevision != allocation.rosterRevision ||
                proof->scope.routeGeneration > previousAuthorityRoute ||
                previousAuthorityRoute > allocation.routeGeneration ||
                proof->nativeConnectionNonce != publishedNonce ||
                proof->operationSequence != publishedOperation ||
                clientStatus.value("state", "") != "playable" ||
                !clientStatus.value("scope_verified", false) ||
                !clientStatus.value("local_pawn_ready", false) ||
                !clientStatus.value("native_net_ready", false) ||
                clientStatus.value("local_world_instance_id", "").empty() ||
                clientStatus.value("operation_sequence", 0ULL) != proof->operationSequence ||
                clientStatus.value("native_connection_nonce", "") != proof->nativeConnectionNonce)
                return std::nullopt;
            const auto staged = NativeMatchScope::FromHostJson(clientStatus.at("scope"));
            const auto confirmed = NativeMatchScope::FromHostJson(clientStatus.at("playable_scope"));
            if (!staged || !confirmed || !proof->scope.Matches(*staged) ||
                !proof->scope.Matches(*confirmed))
                return std::nullopt;
            return proof;
        }
        catch (...)
        {
            return std::nullopt;
        }
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
