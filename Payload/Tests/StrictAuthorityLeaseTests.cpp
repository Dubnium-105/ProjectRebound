#include "../Admission/StrictAuthorityLease.h"
#include <atomic>
#include <barrier>
#include <cstdlib>
#include <iostream>
#include <limits>
#include <thread>
#include <vector>

namespace
{
    void Expect(bool condition, const char* message)
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
    using namespace StrictAuthorityLease;
    int worldA = 1, worldB = 2;
    std::cout << "Owned world fixture: distinct_addresses=" << (&worldA != &worldB)
        << " same_result=" << OwnedWorldMatches(&worldA, "world_a", &worldA, "world_a")
        << " other_result=" << OwnedWorldMatches(&worldB, "world_a", &worldA, "world_a") << '\n';
    Expect(OwnedWorldMatches(&worldA, "world_a", &worldA, "world_a"),
        "the exact observed native world may publish readiness");
    Expect(!OwnedWorldMatches(&worldB, "world_a", &worldA, "world_a"),
        "a replacement native world cannot inherit the frozen world identity");
    Expect(!OwnedWorldMatches(&worldA, "world_b", &worldA, "world_a"),
        "reused world storage cannot inherit a different world generation");
    Expect(!OwnedWorldMatches(nullptr, "world_a", &worldA, "world_a") &&
        !OwnedWorldMatches(&worldA, "", &worldA, ""),
        "missing world observations must never authorize readiness or cleanup");
    const StrictRoster::AllocationScope original{"attempt_a", "session_a", 7, 1};
    auto requested = original;
    const auto classify = [&](const char* kind, bool listening = true, bool sameWorld = true) {
        return Classify(true, kind, original, kind, requested, listening, sameWorld);
    };
    Expect(classify("P2P") == Decision::SameRouteReplay, "same P2P route must replay its existing lease");
    Expect(classify("DEDICATED") == Decision::SameRouteReplay, "same dedicated route must replay its lease");
    requested.routeGeneration = 2;
    Expect(classify("P2P") == Decision::P2PRouteRecovery, "P2P route+1 must reach the policy recovery branch");
    Expect(classify("DEDICATED") == Decision::RouteConflict, "dedicated route cannot use local HOST recovery");
    requested.routeGeneration = 3;
    Expect(classify("P2P") == Decision::RouteConflict, "skipped route generations must reject");
    requested = original;
    requested.attemptId = "attempt_b";
    Expect(classify("P2P") == Decision::Unavailable, "another Attempt must not inherit a world lease");
    requested = original;
    requested.authoritySessionId = "session_b";
    Expect(classify("P2P") == Decision::Unavailable, "another authority session must reject");
    requested = original;
    requested.rosterRevision++;
    Expect(classify("P2P") == Decision::Unavailable, "another roster must reject");
    requested = original;
    Expect(classify("P2P", false) == Decision::WorldUnavailable, "a stopped listener cannot replay readiness");
    Expect(classify("P2P", true, false) == Decision::WorldUnavailable, "another observed world cannot replay readiness");
    auto maxRoute = original;
    maxRoute.routeGeneration = (std::numeric_limits<int>::max)();
    Expect(Classify(true, "P2P", maxRoute, "P2P", original, true, true) == Decision::RouteConflict,
        "maximum route generation must reject rollback without signed overflow");

    NativeClientMatchConfirmation hostProof{
        {"attempt_a", "session_a", "world_a", 7, 1, "p_host", "", 1},
        std::string(32, 'c'), 12, true};
    const auto preservation = hostProof.ToJson();
    auto hostScope = hostProof.scope.ToJson();
    hostScope["room_role"] = "HOST";
    nlohmann::json clientStatus{
        {"state", "playable"}, {"scope_verified", true},
        {"local_pawn_ready", true}, {"native_net_ready", true},
        {"local_world_instance_id", "local_world_a"},
        {"operation_sequence", 12}, {"native_connection_nonce", std::string(32, 'c')},
        {"scope", hostScope}, {"playable_scope", hostScope}};
    auto nextAuthority = original;
    nextAuthority.routeGeneration = 2;
    Expect(PreservedHost(preservation, clientStatus, nextAuthority, "world_a", 1,
        hostProof.nativeConnectionNonce, 12).has_value(),
        "fresh exact HOST proof may preserve route 1 while authority advances to route 2");
    nextAuthority.routeGeneration = 3;
    Expect(PreservedHost(preservation, clientStatus, nextAuthority, "world_a", 2,
        hostProof.nativeConnectionNonce, 12).has_value(),
        "a second authority refresh must keep the original HOST live route unchanged");
    for (const char* field : {"scope_verified", "local_pawn_ready", "native_net_ready"})
    {
        auto stale = clientStatus;
        stale[field] = false;
        Expect(!PreservedHost(preservation, stale, nextAuthority, "world_a", 2,
            hostProof.nativeConnectionNonce, 12), "stale native readiness must reject preservation");
    }
    auto changed = preservation;
    changed["operation_sequence"] = 13;
    Expect(!PreservedHost(changed, clientStatus, nextAuthority, "world_a", 2,
        hostProof.nativeConnectionNonce, 12), "another operation cannot preserve the live HOST");
    changed = preservation;
    changed["route_generation"] = 2;
    Expect(!PreservedHost(changed, clientStatus, nextAuthority, "world_a", 2,
        hostProof.nativeConnectionNonce, 12), "a refreshed authority cannot relabel the client's live route");
    Expect(!PreservedHost(preservation, clientStatus, nextAuthority, "world_b", 2,
        hostProof.nativeConnectionNonce, 12), "another world cannot inherit a HOST proof");
    Expect(!PreservedHost(preservation, clientStatus, nextAuthority, "world_a", 2,
        std::string(32, 'd'), 12), "a nonce mismatch must reject preservation");

    std::atomic_bool owner{false};
    std::atomic<int> winners{0};
    std::barrier start{16};
    std::barrier allAttempted{16};
    std::vector<std::thread> callers;
    for (int i = 0; i < 16; ++i)
    {
        callers.emplace_back([&]() {
            start.arrive_and_wait();
            MutationLease lease(owner);
            if (lease) winners.fetch_add(1);
            allAttempted.arrive_and_wait();
        });
    }
    for (auto& caller : callers) caller.join();
    Expect(winners.load() == 1, "concurrent install/start/clear must have exactly one mutation owner");
    Expect(!owner.load(), "only the acquired lease may release mutation ownership");
    {
        MutationLease first(owner);
        Expect(static_cast<bool>(first), "the next transaction can acquire after completion");
        { MutationLease rejected(owner); Expect(!rejected, "another transaction must fail immediately"); }
        Expect(owner.load(), "a rejected transaction must not release the active owner");
    }
    Expect(!owner.load(), "completed transaction releases its own lease");
    std::cout << "strict authority lease tests passed\n";
}
