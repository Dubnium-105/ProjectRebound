#pragma once

#include <cstdint>
#include <optional>
#include <string>

namespace StrictRosterCleanupReceipt
{
    struct Scope
    {
        std::string attemptId;
        std::string authoritySessionId;
        std::string worldInstanceId;
        std::int64_t rosterRevision = 0;
        int routeGeneration = 0;

        bool operator==(const Scope&) const = default;

        bool IsValid() const
        {
            const auto validId = [](const std::string& value) {
                return !value.empty() && value.size() <= 256 &&
                    value.find('\0') == std::string::npos;
            };
            return validId(attemptId) && validId(authoritySessionId) &&
                validId(worldInstanceId) && rosterRevision > 0 && routeGeneration > 0;
        }
    };

    // The native owner serializes this journal with its cleanup state. Keep
    // one completed scope only, without world pointers, grants or credentials.
    // Replaying the receipt is a read: it must not rerun any cleanup side effects.
    class Journal final
    {
    public:
        bool RecordCompleted(const Scope& scope, const bool teardownObserved)
        {
            if (!teardownObserved || !scope.IsValid())
                return false;
            lastCompleted_ = scope;
            return true;
        }

        bool CanReplay(
            const Scope& scope,
            const bool allocationPresent,
            const bool cleanupPending) const
        {
            return !allocationPresent && !cleanupPending && scope.IsValid() &&
                lastCompleted_.has_value() && *lastCompleted_ == scope;
        }

    private:
        std::optional<Scope> lastCompleted_;
    };
}
