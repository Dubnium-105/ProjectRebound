#pragma once

#include <condition_variable>
#include <memory>
#include <mutex>

namespace StrictAuthorityStartDispatch
{
    enum class State
    {
        Idle,
        Pending,
        Processing,
        Finalizing,
        Completed,
        Cancelled,
    };

    struct Request
    {
        mutable std::mutex mutex;
        std::condition_variable completed;
        State state = State::Idle;
        bool cancellationRequested = false;
    };

    enum class CancelResult
    {
        NotOwned,
        CancelledPending,
        CancelledProcessing,
        AlreadyFinalizing,
        AlreadyCompleted,
    };

    // Shared ownership is part of the contract: the listener may time out
    // while the game thread still owns the request, but a new request can
    // never reuse that queue slot until the old request has published its
    // terminal state.
    class Queue
    {
    public:
        bool Enqueue(const std::shared_ptr<Request>& request) noexcept
        {
            if (!request)
                return false;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_)
                return false;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state != State::Idle)
                return false;
            request->state = State::Pending;
            request->cancellationRequested = false;
            active_ = request;
            return true;
        }

        // Claims the active request and transitions Pending -> Processing
        // under the same queue/request lock used by cancellation. A stale
        // copied pointer can therefore never claim a replacement request.
        std::shared_ptr<Request> Claim() noexcept
        {
            std::lock_guard<std::mutex> queueLock(mutex_);
            const std::shared_ptr<Request> request = active_;
            if (!request)
                return {};
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state != State::Pending)
                return {};
            request->state = State::Processing;
            return request;
        }

        // A native map transition spans multiple engine ticks. The producer
        // resumes only the request that still owns the processing slot, even
        // after its listener has requested cancellation.
        std::shared_ptr<Request> Processing() const noexcept
        {
            std::lock_guard<std::mutex> queueLock(mutex_);
            const auto request = active_;
            if (!request)
                return {};
            std::lock_guard<std::mutex> requestLock(request->mutex);
            return request->state == State::Processing ? request : nullptr;
        }

        CancelResult RequestCancel(const std::shared_ptr<Request>& request) noexcept
        {
            if (!request)
                return CancelResult::NotOwned;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_ != request)
                return CancelResult::NotOwned;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state == State::Pending)
            {
                request->state = State::Cancelled;
                active_.reset();
                request->completed.notify_all();
                return CancelResult::CancelledPending;
            }
            if (request->state == State::Processing)
            {
                request->cancellationRequested = true;
                return CancelResult::CancelledProcessing;
            }
            if (request->state == State::Finalizing)
                return CancelResult::AlreadyFinalizing;
            return CancelResult::AlreadyCompleted;
        }

        bool IsCancellationRequested(
            const std::shared_ptr<Request>& request) const noexcept
        {
            if (!request)
                return true;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_ != request)
                return true;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            return request->state == State::Cancelled ||
                request->cancellationRequested;
        }

        // Freezes the cancellation decision. After this transition the
        // listener cannot cancel a result between the producer's final scope
        // check and its terminal publication.
        bool BeginFinalize(
            const std::shared_ptr<Request>& request,
            bool& cancellationRequested) noexcept
        {
            cancellationRequested = false;
            if (!request)
                return false;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_ != request)
                return false;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state != State::Processing)
                return false;
            request->state = State::Finalizing;
            cancellationRequested = request->cancellationRequested;
            return true;
        }

        bool PublishCompleted(const std::shared_ptr<Request>& request) noexcept
        {
            if (!request)
                return false;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_ != request)
                return false;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state != State::Finalizing)
                return false;
            request->state = State::Completed;
            active_.reset();
            request->completed.notify_all();
            return true;
        }

        // Atomically snapshots cancellation, transitions through the
        // finalizing state, applies the caller's terminal-result callback, and
        // publishes the completed state while both queue and request locks are
        // held. A listener can therefore never observe a finalizing request
        // with an accepted result still waiting for an unowned publication.
        template <typename FinalizeCallback>
        bool CommitCompleted(
            const std::shared_ptr<Request>& request,
            bool& cancellationRequested,
            FinalizeCallback&& finalizeCallback) noexcept
        {
            cancellationRequested = false;
            if (!request)
                return false;
            std::lock_guard<std::mutex> queueLock(mutex_);
            if (active_ != request)
                return false;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            if (request->state != State::Processing)
                return false;
            cancellationRequested = request->cancellationRequested;
            request->state = State::Finalizing;
            try
            {
                finalizeCallback(cancellationRequested);
            }
            catch (...)
            {
                // The producer still owns the terminal transition. Keep the
                // queue reusable and wake the waiter; its preloaded result is
                // fail-closed if the callback could not adjust it.
            }
            request->state = State::Completed;
            active_.reset();
            request->completed.notify_all();
            return true;
        }

        State GetState(const std::shared_ptr<Request>& request) const noexcept
        {
            if (!request)
                return State::Idle;
            std::lock_guard<std::mutex> requestLock(request->mutex);
            return request->state;
        }

        bool HasActive() const noexcept
        {
            std::lock_guard<std::mutex> queueLock(mutex_);
            return static_cast<bool>(active_);
        }

    private:
        mutable std::mutex mutex_;
        std::shared_ptr<Request> active_;
    };
}
