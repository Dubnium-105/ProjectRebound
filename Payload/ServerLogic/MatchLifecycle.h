#pragma once

#include "../Libs/json.hpp"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <deque>
#include <limits>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <utility>

namespace MatchLifecycle
{
    inline constexpr std::string_view kProtocolVersion = "match-lifecycle-v1";
    inline constexpr std::string_view kResultConfirmedPhase = "RESULT_CONFIRMED";
    inline constexpr std::string_view kReturnReadyPhase = "RETURN_READY";
    inline constexpr float kReturnReadyGraceSeconds = 120.0F;

    // Pure binding policy used by the native bridge and policy tests. A null
    // world-side NetDriver is allowed during teardown, but a live replacement
    // driver on the captured World invalidates the old flush callback.
    [[nodiscard]] inline bool IsNetworkFlushBindingValid(
        const bool worldProvided,
        const bool worldMatchesBinding,
        const bool hookDriverMatchesBinding,
        const bool worldDriverPresent,
        const bool worldDriverMatchesBinding) noexcept
    {
        if (!hookDriverMatchesBinding)
            return false;
        if (worldProvided && !worldMatchesBinding)
            return false;
        if (worldProvided && worldDriverPresent && !worldDriverMatchesBinding)
            return false;
        return true;
    }

    struct Scope
    {
        std::string attemptId;
        std::string authoritySessionId;
        std::string worldInstanceId;
        std::int64_t rosterRevision = 0;
        int routeGeneration = 0;
        std::uint64_t matchGeneration = 0;

        [[nodiscard]] bool operator==(const Scope& other) const noexcept
        {
            return attemptId == other.attemptId &&
                authoritySessionId == other.authoritySessionId &&
                worldInstanceId == other.worldInstanceId &&
                rosterRevision == other.rosterRevision &&
                routeGeneration == other.routeGeneration &&
                matchGeneration == other.matchGeneration;
        }

        [[nodiscard]] bool HasSameAllocation(const Scope& other) const noexcept
        {
            return attemptId == other.attemptId &&
                authoritySessionId == other.authoritySessionId &&
                worldInstanceId == other.worldInstanceId &&
                rosterRevision == other.rosterRevision &&
                routeGeneration == other.routeGeneration;
        }

        [[nodiscard]] bool IsValid() const noexcept
        {
            const auto validIdentity = [](const std::string& value) {
                return !value.empty() && value.size() <= 256U &&
                    value.find('\0') == std::string::npos;
            };
            return validIdentity(attemptId) && validIdentity(authoritySessionId) &&
                validIdentity(worldInstanceId) &&
                rosterRevision > 0 && routeGeneration > 0 && matchGeneration > 0;
        }
    };

    struct Receipt
    {
        Scope scope;
        std::uint64_t eventSeq = 0;
        std::string phase;

        [[nodiscard]] bool operator==(const Receipt& other) const noexcept
        {
            return scope == other.scope && eventSeq == other.eventSeq &&
                phase == other.phase;
        }

        [[nodiscard]] nlohmann::json ToJson() const
        {
            return nlohmann::json{
                {"attempt_id", scope.attemptId},
                {"authority_session_id", scope.authoritySessionId},
                {"world_instance_id", scope.worldInstanceId},
                {"roster_revision", scope.rosterRevision},
                {"route_generation", scope.routeGeneration},
                {"match_generation", scope.matchGeneration},
                {"event_seq", eventSeq},
                {"phase", phase}
            };
        }
    };

    // The native side owns only a small in-memory outbox.  The Toolbox is the
    // durable consumer: it saves an event before sending its ACK.  All methods
    // take short mutexes and never touch UObjects, so pipe callbacks and the
    // game-thread hooks can use the same object without blocking gameplay on
    // transport or disk I/O.
    class Outbox
    {
    public:
        [[nodiscard]] bool MarkResultFrozen(const Scope& scope)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!PrepareScopeLocked(scope) || productionCancelled_)
                return false;
            resultFrozen_ = true;
            if (resultConfirmationPending_ && !resultConfirmed_)
            {
                resultConfirmationPending_ = false;
                resultConfirmed_ = true;
                pending_.push_back(
                    Receipt{scope, 1, std::string(kResultConfirmedPhase)});
            }
            return true;
        }

        [[nodiscard]] bool ConfirmResult(const Scope& scope)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!PrepareScopeLocked(scope) || productionCancelled_)
                return false;
            if (resultConfirmed_)
                return true;

            // EndMatch in the pinned build can synchronously enter
            // StartShowingMatchResult. Remember that observation until the
            // EndMatch hook records the result freeze boundary.
            if (!resultFrozen_)
            {
                resultConfirmationPending_ = true;
                return true;
            }

            resultConfirmed_ = true;
            pending_.push_back(Receipt{scope, 1, std::string(kResultConfirmedPhase)});
            return true;
        }

        [[nodiscard]] bool ArmReturn(const Scope& scope)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!PrepareScopeLocked(scope) || !resultConfirmed_ || productionCancelled_)
                return false;
            if (returnArmed_ || returnReadyIssued_)
                return true;

            returnArmed_ = true;
            returnGraceDeadline_ = std::chrono::steady_clock::now() +
                std::chrono::seconds(static_cast<int>(kReturnReadyGraceSeconds));
            return true;
        }

        [[nodiscard]] bool ReturnNotificationCompleted(const Scope& scope)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!activeScope_ || *activeScope_ != scope ||
                !returnArmed_ || productionCancelled_)
                return false;

            returnNotificationCompleted_ = true;
            return true;
        }

        [[nodiscard]] bool NetworkFlushCompleted(const Scope& scope)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!activeScope_ || *activeScope_ != scope ||
                !returnArmed_ || !returnNotificationCompleted_ || productionCancelled_)
            {
                return false;
            }
            if (returnReadyIssued_)
                return true;

            returnReadyIssued_ = true;
            pending_.push_back(Receipt{scope, 2, std::string(kReturnReadyPhase)});
            return true;
        }

        [[nodiscard]] float FinalCleanupWait(
            const Scope& scope,
            const float requestedSeconds) const
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!activeScope_ || *activeScope_ != scope || !returnArmed_ ||
                productionCancelled_ || returnReadyAcknowledged_)
            {
                return requestedSeconds;
            }

            const auto now = std::chrono::steady_clock::now();
            if (returnGraceDeadline_ <= now)
                return requestedSeconds;

            const float remaining = std::chrono::duration<float>(
                returnGraceDeadline_ - now).count();
            return (std::max)(requestedSeconds, remaining);
        }

        void CancelProductionAllocationScope(
            const std::string& attemptId,
            const std::string& authoritySessionId,
            const std::string& worldInstanceId,
            const std::int64_t rosterRevision,
            const int routeGeneration)
        {
            std::lock_guard<std::mutex> lock(mutex_);
            if (!activeScope_ || activeScope_->attemptId != attemptId ||
                activeScope_->authoritySessionId != authoritySessionId ||
                activeScope_->worldInstanceId != worldInstanceId ||
                activeScope_->rosterRevision != rosterRevision ||
                activeScope_->routeGeneration != routeGeneration)
            {
                return;
            }
            productionCancelled_ = true;
            returnArmed_ = false;
            returnNotificationCompleted_ = false;
        }

        void CancelProductionAllocationScope(const nlohmann::json& arguments)
        {
            try
            {
                const auto attempt = arguments.find("attempt_id");
                const auto authority = arguments.find("authority_session_id");
                const auto world = arguments.find("world_instance_id");
                const auto revision = arguments.find("roster_revision");
                const auto route = arguments.find("route_generation");
                if (attempt == arguments.end() || !attempt->is_string() ||
                    authority == arguments.end() || !authority->is_string() ||
                    world == arguments.end() || !world->is_string() ||
                    revision == arguments.end() || route == arguments.end())
                {
                    return;
                }
                std::int64_t revisionValue = 0;
                std::int64_t routeValue = 0;
                if (!ReadPositiveInt64(*revision, revisionValue) ||
                    !ReadPositiveInt64(*route, routeValue) ||
                    routeValue > (std::numeric_limits<int>::max)())
                {
                    return;
                }
                CancelProductionAllocationScope(
                    attempt->get<std::string>(), authority->get<std::string>(),
                    world->get<std::string>(), revisionValue,
                    static_cast<int>(routeValue));
            }
            catch (...)
            {
                // A malformed clear request is rejected by strict-roster
                // validation; it must not affect the lifecycle outbox.
            }
        }

        [[nodiscard]] nlohmann::json Poll() const
        {
            std::deque<Receipt> snapshot;
            {
                std::lock_guard<std::mutex> lock(mutex_);
                snapshot = pending_;
            }
            nlohmann::json events = nlohmann::json::array();
            for (const Receipt& receipt : snapshot)
                events.push_back(receipt.ToJson());

            return nlohmann::json{
                {"protocol_version", std::string(kProtocolVersion)},
                {"events", std::move(events)}
            };
        }

        [[nodiscard]] nlohmann::json Ack(const nlohmann::json& arguments)
        {
            Receipt requested;
            std::string error;
            if (!ParseReceipt(arguments, requested, error))
                return Rejected("invalid_receipt", error);

            std::lock_guard<std::mutex> lock(mutex_);
            for (auto it = pending_.begin(); it != pending_.end(); ++it)
            {
                if (it->scope != requested.scope ||
                    it->eventSeq != requested.eventSeq ||
                    it->phase != requested.phase)
                {
                    continue;
                }

                // A higher event sequence implicitly confirms earlier events
                // from this exact match, which lets the durable consumer ACK a
                // batch after its atomic save.
                auto erase = pending_.begin();
                while (erase != pending_.end())
                {
                    if (erase->scope == requested.scope &&
                        erase->eventSeq <= requested.eventSeq)
                    {
                        RememberAckedLocked(*erase);
                        erase = pending_.erase(erase);
                    }
                    else
                    {
                        ++erase;
                    }
                }
                if (requested.eventSeq >= 2)
                    returnReadyAcknowledged_ = true;
                return AcceptedAck();
            }

            for (const Receipt& receipt : acknowledged_)
            {
                if (receipt == requested)
                {
                    if (requested.eventSeq >= 2)
                        returnReadyAcknowledged_ = true;
                    return AcceptedAck();
                }
            }

            return Rejected(
                "receipt_not_pending",
                "the lifecycle receipt is unknown or belongs to another match");
        }

    private:
        static bool ReadPositiveInt64(
            const nlohmann::json& value,
            std::int64_t& output) noexcept
        {
            try
            {
                if (value.is_number_integer())
                {
                    output = value.get<std::int64_t>();
                    return output > 0;
                }
                if (value.is_number_unsigned())
                {
                    const std::uint64_t unsignedValue = value.get<std::uint64_t>();
                    if (unsignedValue == 0 ||
                        unsignedValue > static_cast<std::uint64_t>(
                            (std::numeric_limits<std::int64_t>::max)()))
                    {
                        return false;
                    }
                    output = static_cast<std::int64_t>(unsignedValue);
                    return true;
                }
            }
            catch (...)
            {
                return false;
            }
            return false;
        }

        static bool ReadPositiveUInt64(
            const nlohmann::json& value,
            std::uint64_t& output) noexcept
        {
            try
            {
                if (value.is_number_unsigned())
                {
                    output = value.get<std::uint64_t>();
                    return output > 0;
                }
                if (value.is_number_integer())
                {
                    const std::int64_t signedValue = value.get<std::int64_t>();
                    if (signedValue <= 0)
                        return false;
                    output = static_cast<std::uint64_t>(signedValue);
                    return true;
                }
            }
            catch (...)
            {
                return false;
            }
            return false;
        }

        static bool ParseReceipt(
            const nlohmann::json& arguments,
            Receipt& output,
            std::string& error) noexcept
        {
            try
            {
                const auto protocol = arguments.find("protocol_version");
                if (protocol != arguments.end() &&
                    (!protocol->is_string() ||
                        protocol->get<std::string>() != std::string(kProtocolVersion)))
                {
                    error = "protocol_version must be match-lifecycle-v1 when present";
                    return false;
                }

                const auto attempt = arguments.find("attempt_id");
                const auto authority = arguments.find("authority_session_id");
                const auto world = arguments.find("world_instance_id");
                const auto revision = arguments.find("roster_revision");
                const auto route = arguments.find("route_generation");
                const auto generation = arguments.find("match_generation");
                const auto sequence = arguments.find("event_seq");
                const auto phase = arguments.find("phase");
                if (attempt == arguments.end() || !attempt->is_string() ||
                    authority == arguments.end() || !authority->is_string() ||
                    world == arguments.end() || !world->is_string() ||
                    revision == arguments.end() || route == arguments.end() ||
                    generation == arguments.end() || sequence == arguments.end() ||
                    phase == arguments.end() || !phase->is_string())
                {
                    error = "ACK requires the complete lifecycle receipt";
                    return false;
                }

                output.scope.attemptId = attempt->get<std::string>();
                output.scope.authoritySessionId = authority->get<std::string>();
                output.scope.worldInstanceId = world->get<std::string>();
                std::int64_t routeValue = 0;
                if (!ReadPositiveInt64(*revision, output.scope.rosterRevision) ||
                    !ReadPositiveInt64(*route, routeValue))
                {
                    error = "roster_revision and route_generation must be positive integers";
                    return false;
                }
                if (routeValue > (std::numeric_limits<int>::max)())
                {
                    error = "route_generation is out of range";
                    return false;
                }
                output.scope.routeGeneration = static_cast<int>(routeValue);
                if (!ReadPositiveUInt64(*generation, output.scope.matchGeneration) ||
                    !ReadPositiveUInt64(*sequence, output.eventSeq))
                {
                    error = "match_generation and event_seq must be positive integers";
                    return false;
                }
                output.phase = phase->get<std::string>();
                if (output.phase != kResultConfirmedPhase &&
                    output.phase != kReturnReadyPhase)
                {
                    error = "phase is not supported by match-lifecycle-v1";
                    return false;
                }
                if (!output.scope.IsValid() || output.eventSeq > 2)
                {
                    error = "lifecycle receipt scope is invalid";
                    return false;
                }
                if ((output.eventSeq == 1 && output.phase != kResultConfirmedPhase) ||
                    (output.eventSeq == 2 && output.phase != kReturnReadyPhase))
                {
                    error = "event_seq does not match phase";
                    return false;
                }
                return true;
            }
            catch (...)
            {
                error = "lifecycle receipt has invalid field types";
                return false;
            }
        }

        [[nodiscard]] static nlohmann::json Rejected(
            const char* code,
            const std::string& message)
        {
            return nlohmann::json{
                {"accepted", false},
                {"status", "rejected"},
                {"protocol_version", std::string(kProtocolVersion)},
                {"code", code},
                {"message", message}
            };
        }

        [[nodiscard]] static nlohmann::json AcceptedAck()
        {
            return nlohmann::json{
                {"status", "ok"}
            };
        }

        [[nodiscard]] bool PrepareScopeLocked(const Scope& scope)
        {
            if (!scope.IsValid())
                return false;
            if (!activeScope_)
            {
                activeScope_ = scope;
                resultFrozen_ = false;
                resultConfirmed_ = false;
                resultConfirmationPending_ = false;
                productionCancelled_ = false;
                returnArmed_ = false;
                returnNotificationCompleted_ = false;
                returnReadyIssued_ = false;
                returnReadyAcknowledged_ = false;
                returnGraceDeadline_ = {};
                pending_.clear();
                acknowledged_.clear();
                return true;
            }
            if (*activeScope_ == scope)
                return true;
            if (!pending_.empty() || returnArmed_ || resultFrozen_ ||
                resultConfirmed_ || productionCancelled_)
                return false;

            activeScope_ = scope;
            resultFrozen_ = false;
            resultConfirmed_ = false;
            resultConfirmationPending_ = false;
            productionCancelled_ = false;
            returnNotificationCompleted_ = false;
            returnReadyIssued_ = false;
            returnReadyAcknowledged_ = false;
            returnGraceDeadline_ = {};
            acknowledged_.clear();
            return true;
        }

        void RememberAckedLocked(const Receipt& receipt)
        {
            acknowledged_.push_back(receipt);
            while (acknowledged_.size() > 8U)
                acknowledged_.pop_front();
        }

        mutable std::mutex mutex_;
        std::optional<Scope> activeScope_;
        bool resultFrozen_ = false;
        bool resultConfirmed_ = false;
        bool resultConfirmationPending_ = false;
        bool productionCancelled_ = false;
        bool returnArmed_ = false;
        bool returnNotificationCompleted_ = false;
        bool returnReadyIssued_ = false;
        bool returnReadyAcknowledged_ = false;
        std::chrono::steady_clock::time_point returnGraceDeadline_{};
        std::deque<Receipt> pending_;
        std::deque<Receipt> acknowledged_;
    };

    inline Outbox& NativeOutbox()
    {
        static Outbox outbox;
        return outbox;
    }
}
