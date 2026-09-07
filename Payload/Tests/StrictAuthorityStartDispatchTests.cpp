#include "../Admission/StrictAuthorityStartDispatch.h"
#include "../Admission/StrictAuthorityLease.h"

#include <atomic>
#include <condition_variable>
#include <cstdlib>
#include <iostream>
#include <memory>
#include <mutex>
#include <thread>
#include <vector>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    using StrictAuthorityStartDispatch::CancelResult;
    using StrictAuthorityStartDispatch::Queue;
    using StrictAuthorityStartDispatch::Request;
    using StrictAuthorityStartDispatch::State;

    Queue queue;
    auto first = std::make_shared<Request>();
    std::atomic<bool> start{false};
    std::atomic<int> queueWinners{0};
    std::vector<std::thread> queueThreads;
    queueThreads.reserve(16);
    for (int i = 0; i < 16; ++i)
    {
        queueThreads.emplace_back([&]() {
            while (!start.load(std::memory_order_acquire))
                std::this_thread::yield();
            if (queue.Enqueue(first))
                queueWinners.fetch_add(1, std::memory_order_relaxed);
        });
    }
    start.store(true, std::memory_order_release);
    for (auto& thread : queueThreads)
        thread.join();
    Expect(queueWinners.load(std::memory_order_relaxed) == 1,
        "exactly one concurrent pipe request may become pending");
    Expect(queue.GetState(first) == State::Pending,
        "the winning request must remain pending until the game thread claims it");

    const auto claimed = queue.Claim();
    Expect(claimed == first, "the game thread must claim the active request identity");
    Expect(queue.GetState(first) == State::Processing,
        "the claimed request must become processing");
    Expect(!queue.Claim() && queue.Processing() == first,
        "a later engine tick must resume the same request without dispatching twice");

    // This models the listener timing out while StartServer is already in
    // progress. It records cancellation without freeing the queue slot.
    Expect(queue.RequestCancel(first) == CancelResult::CancelledProcessing,
        "an in-flight request must retain cancellation ownership");
    Expect(queue.Processing() == first && queue.IsCancellationRequested(first),
        "the next engine tick must resume cancelled travel to clean its owned world");
    bool cancelled = false;
    Expect(queue.BeginFinalize(first, cancelled),
        "the game thread must freeze the cancellation decision before publish");
    Expect(cancelled, "finalization must observe the listener cancellation");
    Expect(queue.GetState(first) == State::Finalizing,
        "finalization must block a replacement request");
    Expect(!queue.Processing(), "a finalizing request must not execute native travel again");

    auto replacementBeforePublish = std::make_shared<Request>();
    Expect(!queue.Enqueue(replacementBeforePublish),
        "a stale in-flight request must retain the queue slot");
    Expect(queue.PublishCompleted(first),
        "the cancelled in-flight request must publish a terminal state");
    {
        std::lock_guard<std::mutex> lock(first->mutex);
        Expect(first->state == State::Completed,
            "the waiter must observe terminal publication");
    }

    // A stale copy of the old shared_ptr cannot claim the replacement.
    auto replacement = std::make_shared<Request>();
    Expect(queue.Enqueue(replacement), "a replacement may queue after publish");
    const auto replacementClaim = queue.Claim();
    Expect(replacementClaim == replacement && replacementClaim != first,
        "a stale request pointer must never claim a newer scope");
    bool replacementCancelled = false;
    Expect(queue.BeginFinalize(replacement, replacementCancelled),
        "replacement should finalize independently");
    Expect(!replacementCancelled, "replacement was not cancelled");
    Expect(queue.PublishCompleted(replacement), "replacement must publish normally");

    // Deadline/Publish interleaving: once Finalizing is entered, a listener
    // that reaches its deadline cannot cancel the request or return while the
    // producer still owns a possible accepted result. It waits for the same
    // terminal publication that closes the queue slot.
    auto finalizing = std::make_shared<Request>();
    Expect(queue.Enqueue(finalizing), "finalizing request should queue");
    Expect(queue.Claim() == finalizing, "finalizing request should claim");
    bool finalizingCancelled = false;
    Expect(queue.BeginFinalize(finalizing, finalizingCancelled),
        "finalizing request should enter the publication barrier");
    std::mutex waiterMutex;
    std::condition_variable waiterReady;
    bool waiterHasLock = false;
    bool waiterSawCompleted = false;
    std::thread waiter([&]() {
        std::unique_lock<std::mutex> lock(finalizing->mutex);
        {
            std::lock_guard<std::mutex> readyLock(waiterMutex);
            waiterHasLock = true;
        }
        waiterReady.notify_one();
        finalizing->completed.wait(lock, [&]() {
            return finalizing->state == State::Completed ||
                finalizing->state == State::Cancelled;
        });
        waiterSawCompleted = finalizing->state == State::Completed;
    });
    {
        std::unique_lock<std::mutex> lock(waiterMutex);
        waiterReady.wait(lock, [&]() { return waiterHasLock; });
    }
    Expect(queue.RequestCancel(finalizing) == CancelResult::AlreadyFinalizing,
        "a deadline racing publication must observe frozen finalization");
    Expect(queue.PublishCompleted(finalizing),
        "the producer must publish the finalizing request exactly once");
    waiter.join();
    Expect(waiterSawCompleted,
        "a finalizing waiter must observe terminal publication before returning");

    auto committed = std::make_shared<Request>();
    Expect(queue.Enqueue(committed), "atomic commit request should queue");
    Expect(queue.Claim() == committed, "atomic commit request should claim");
    Expect(queue.RequestCancel(committed) == CancelResult::CancelledProcessing,
        "atomic commit must see cancellation before final publication");
    bool commitCancelled = false;
    bool callbackSawCancellation = false;
    Expect(queue.CommitCompleted(
        committed,
        commitCancelled,
        [&](const bool cancelledByWaiter) {
            callbackSawCancellation = cancelledByWaiter;
        }),
        "atomic commit should publish the terminal result");
    Expect(commitCancelled && callbackSawCancellation &&
        queue.GetState(committed) == State::Completed,
        "atomic commit must publish cancellation and completion as one transition");

    // Pending timeout is terminal before dispatch and frees the slot only
    // after the queue/request lock has established ownership.
    auto pending = std::make_shared<Request>();
    Expect(queue.Enqueue(pending), "a fresh pending request should queue");
    Expect(queue.RequestCancel(pending) == CancelResult::CancelledPending,
        "pending timeout must cancel before native dispatch");
    Expect(queue.GetState(pending) == State::Cancelled,
        "pending cancellation must be terminal");
    Expect(!queue.Claim(), "cancelled pending request must never reach StartServer");
    auto afterPending = std::make_shared<Request>();
    Expect(queue.Enqueue(afterPending), "slot must be reusable after pending cancellation");
    const auto afterPendingClaim = queue.Claim();
    Expect(afterPendingClaim == afterPending, "new request must own the reusable slot");
    bool afterPendingCancelled = false;
    Expect(queue.BeginFinalize(afterPending, afterPendingCancelled),
        "new request must be finalizable");
    Expect(queue.PublishCompleted(afterPending), "new request must publish terminal state");

    StrictRoster::AllocationScope leaseScope{
        "attempt", "authority", 7, 4};
    StrictRoster::AllocationScope sameRoute = leaseScope;
    Expect(StrictAuthorityLease::Classify(
        true, "P2P", leaseScope, "P2P", sameRoute, true, true) ==
        StrictAuthorityLease::Decision::SameRouteReplay,
        "same scoped route must replay the existing native nonce");
    StrictRoster::AllocationScope nextRoute = leaseScope;
    nextRoute.routeGeneration = 5;
    Expect(StrictAuthorityLease::Classify(
        true, "P2P", leaseScope, "P2P", nextRoute, true, true) ==
        StrictAuthorityLease::Decision::P2PRouteRecovery,
        "P2P route refresh must classify as same-world recovery");
    StrictRoster::AllocationScope otherAttempt = nextRoute;
    otherAttempt.attemptId = "other";
    Expect(StrictAuthorityLease::Classify(
        true, "P2P", leaseScope, "P2P", otherAttempt, true, true) ==
        StrictAuthorityLease::Decision::Unavailable,
        "a different attempt must never reuse a live native lease");
    Expect(StrictAuthorityLease::Classify(
        true, "P2P", leaseScope, "P2P", sameRoute, false, true) ==
        StrictAuthorityLease::Decision::WorldUnavailable,
        "a stale game-thread world snapshot must fail closed");

    std::cout << "strict authority start dispatch tests passed\n";
    return 0;
}
