#include "../Admission/StrictRosterCleanupReceipt.h"

#include <cstdlib>
#include <iostream>

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
    using StrictRosterCleanupReceipt::Journal;
    using StrictRosterCleanupReceipt::Scope;
    Journal journal;
    const Scope first{"attempt_a", "authority_a", "world_a", 7, 3};
    Expect(!journal.CanReplay(first, false, false), "no receipt before teardown");
    Expect(!journal.RecordCompleted(first, false), "a request is not teardown evidence");
    Expect(!journal.CanReplay(first, false, false), "pending cleanup cannot create an ACK");

    Expect(journal.RecordCompleted(first, true), "observed teardown retains its exact scope");
    Expect(journal.CanReplay(first, false, false), "lost first ACK can be replayed");
    Expect(journal.CanReplay(first, false, false), "repeated ACK does not consume the receipt");
    Expect(!journal.CanReplay(first, true, false), "a current allocation takes precedence over a receipt");
    Expect(!journal.CanReplay(first, false, true), "another pending cleanup cannot be acknowledged");
    Expect(!journal.CanReplay(first, true, true), "active allocation and pending cleanup remain fenced");

    Scope changed = first;
    changed.attemptId = "attempt_b";
    Expect(!journal.CanReplay(changed, false, false), "attempt identity must match");
    changed = first;
    changed.authoritySessionId = "authority_b";
    Expect(!journal.CanReplay(changed, false, false), "authority session must match");
    changed = first;
    changed.worldInstanceId = "world_b";
    Expect(!journal.CanReplay(changed, false, false), "native world must match");
    changed = first;
    ++changed.rosterRevision;
    Expect(!journal.CanReplay(changed, false, false), "roster revision must match");
    changed = first;
    ++changed.routeGeneration;
    Expect(!journal.CanReplay(changed, false, false), "route generation must match");

    const Scope second{"attempt_b", "authority_b", "world_b", 8, 1};
    Expect(!journal.RecordCompleted(second, false), "a second unfinished round is not cleared");
    Expect(!journal.CanReplay(second, false, false), "first receipt cannot ACK second round");
    Expect(journal.RecordCompleted(second, true), "second observed teardown replaces the bounded receipt");
    Expect(!journal.CanReplay(first, false, false), "a retired receipt is not retained indefinitely");
    Expect(journal.CanReplay(second, false, false), "latest completed round remains retryable");

    Scope invalid = second;
    invalid.routeGeneration = 0;
    Expect(!journal.RecordCompleted(invalid, true), "invalid generation cannot create a receipt");
    invalid = second;
    invalid.worldInstanceId.assign(257, 'w');
    Expect(!journal.RecordCompleted(invalid, true), "receipt identity storage is bounded");
    invalid = second;
    invalid.authoritySessionId.push_back('\0');
    Expect(!journal.RecordCompleted(invalid, true), "embedded NUL cannot alias a native identity");
    Expect(journal.CanReplay(second, false, false), "invalid completion cannot destroy a valid receipt");
    std::cout << "Strict roster cleanup receipt tests passed\n";
    return 0;
}
