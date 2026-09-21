#include "../ServerLogic/MatchLifecycle.h"

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

    MatchLifecycle::Scope ScopeFor(const char* suffix)
    {
        return {
            std::string("attempt_") + suffix,
            std::string("authority_") + suffix,
            std::string("world_") + suffix,
            7,
            3,
            1};
    }
}

int main()
{
    using MatchLifecycle::Outbox;
    using MatchLifecycle::Scope;

    const Scope scope = ScopeFor("a");
    Outbox outbox;
    Expect(!outbox.ArmReturn(scope), "return cannot arm before result confirmation");
    Expect(outbox.ConfirmResult(scope),
        "nested result confirmation is retained before native result freeze");
    Expect(outbox.Poll().at("events").empty(),
        "early result confirmation does not emit before native result freeze");
    Expect(outbox.MarkResultFrozen(scope), "native result freeze is recorded");
    Expect(outbox.ConfirmResult(scope), "result confirmation is accepted");

    const nlohmann::json resultPoll = outbox.Poll();
    Expect(resultPoll.at("protocol_version") == "match-lifecycle-v1",
        "poll uses the independent lifecycle capability");
    Expect(resultPoll.at("events").size() == 1, "result poll has one event");
    Expect(resultPoll.at("events").at(0).at("event_seq") == 1,
        "result event sequence starts at one per match");
    Expect(resultPoll.at("events").at(0).at("phase") == "RESULT_CONFIRMED",
        "result phase is frozen and explicit");
    Expect(resultPoll.at("events").at(0).at("match_generation") == 1,
        "result carries match generation");

    Expect(outbox.ArmReturn(scope), "normal return arms after result confirmation");
    Expect(!outbox.NetworkFlushCompleted(scope),
        "a flush before native return notification cannot complete return");
    Expect(outbox.ReturnNotificationCompleted(scope),
        "native return notification completes the notification stage");
    Expect(outbox.NetworkFlushCompleted(scope),
        "return becomes ready only after a network flush");

    const nlohmann::json bothPoll = outbox.Poll();
    Expect(bothPoll.at("events").size() == 2,
        "poll retains both phases until durable ACKs");
    Expect(bothPoll.at("events").at(1).at("event_seq") == 2,
        "return event sequence is two per match");
    Expect(bothPoll.at("events").at(1).at("phase") == "RETURN_READY",
        "return phase is explicit");

    nlohmann::json resultAck = bothPoll.at("events").at(0);
    // The wire contract identifies the capability in the response; the ACK
    // itself is allowed to carry only request_id plus the complete event.
    const nlohmann::json resultAckResult = outbox.Ack(resultAck);
    Expect(resultAckResult.at("status") == "ok", "result ACK is accepted");
    Expect(outbox.Poll().at("events").size() == 1,
        "result ACK leaves RETURN_READY pending");
    Expect(outbox.Ack(resultAck).at("status") == "ok",
        "replayed result ACK is idempotent");

    nlohmann::json returnAck = bothPoll.at("events").at(1);
    returnAck["protocol_version"] = "match-lifecycle-v1";
    Expect(outbox.Ack(returnAck).at("status") == "ok",
        "return ACK is accepted");
    Expect(outbox.Poll().at("events").empty(),
        "all events leave the outbox after ACK");

    Outbox reverseOrder;
    const Scope reverseScope = ScopeFor("reverse");
    Expect(reverseOrder.MarkResultFrozen(reverseScope),
        "freeze-first ordering records the native result boundary");
    Expect(reverseOrder.ConfirmResult(reverseScope),
        "freeze-first ordering accepts result confirmation");
    Expect(reverseOrder.Poll().at("events").size() == 1,
        "freeze-first ordering emits one result receipt");

    Outbox incomplete;
    const Scope incompleteScope = ScopeFor("incomplete");
    Expect(incomplete.MarkResultFrozen(incompleteScope),
        "incomplete-order test records result freeze");
    Expect(incomplete.Poll().at("events").empty(),
        "freeze without confirmation emits no result receipt");
    Outbox confirmationOnly;
    const Scope confirmationOnlyScope = ScopeFor("confirmation-only");
    Expect(confirmationOnly.ConfirmResult(confirmationOnlyScope),
        "confirmation-only order is retained as an observation");
    Expect(confirmationOnly.Poll().at("events").empty(),
        "confirmation-only order emits no result receipt");

    Outbox grace;
    const Scope graceScope = ScopeFor("grace");
    Expect(grace.MarkResultFrozen(graceScope), "grace test records result freeze");
    Expect(grace.ConfirmResult(graceScope), "grace test confirms result");
    Expect(grace.ArmReturn(graceScope), "grace test arms normal return");
    Expect(grace.ReturnNotificationCompleted(graceScope),
        "grace test records return notification");
    const float extendedWait = grace.FinalCleanupWait(graceScope, 5.0F);
    Expect(extendedWait >= 100.0F,
        "armed return extends cleanup wait while RETURN_READY is unacknowledged");
    Expect(grace.NetworkFlushCompleted(graceScope),
        "grace test produces RETURN_READY");
    nlohmann::json graceAck = grace.Poll().at("events").at(1);
    Expect(grace.Ack(graceAck).at("status") == "ok",
        "grace test accepts RETURN_READY ACK");
    Expect(grace.FinalCleanupWait(graceScope, 5.0F) == 5.0F,
        "ACKed return keeps the native cleanup wait unchanged");

    Outbox unarmed;
    const Scope unarmedScope = ScopeFor("unarmed");
    Expect(unarmed.MarkResultFrozen(unarmedScope),
        "unarmed grace test records result freeze");
    Expect(unarmed.ConfirmResult(unarmedScope),
        "unarmed grace test confirms result");
    Expect(unarmed.FinalCleanupWait(unarmedScope, 5.0F) == 5.0F,
        "unarmed result keeps the native cleanup wait unchanged");

    Outbox cancelledGrace;
    const Scope cancelledGraceScope = ScopeFor("cancelled-grace");
    Expect(cancelledGrace.MarkResultFrozen(cancelledGraceScope),
        "cancelled grace test records result freeze");
    Expect(cancelledGrace.ConfirmResult(cancelledGraceScope),
        "cancelled grace test confirms result");
    Expect(cancelledGrace.ArmReturn(cancelledGraceScope),
        "cancelled grace test arms return");
    cancelledGrace.CancelProductionAllocationScope(
        cancelledGraceScope.attemptId, cancelledGraceScope.authoritySessionId,
        cancelledGraceScope.worldInstanceId, cancelledGraceScope.rosterRevision,
        cancelledGraceScope.routeGeneration);
    Expect(cancelledGrace.FinalCleanupWait(cancelledGraceScope, 5.0F) == 5.0F,
        "cancelled return keeps the native cleanup wait unchanged");

    Expect(MatchLifecycle::IsNetworkFlushBindingValid(
               false, false, true, false, true),
        "null world permits the captured driver flush");
    Expect(MatchLifecycle::IsNetworkFlushBindingValid(
               true, true, true, false, true),
        "same world with cleared NetDriver permits the captured flush");
    Expect(MatchLifecycle::IsNetworkFlushBindingValid(
               true, true, true, true, true),
        "same world with the captured NetDriver permits the flush");
    Expect(!MatchLifecycle::IsNetworkFlushBindingValid(
               true, true, true, true, false),
        "same world with a replacement NetDriver rejects the old flush");
    Expect(!MatchLifecycle::IsNetworkFlushBindingValid(
               true, false, true, false, true),
        "a different world rejects the captured flush");
    Expect(!MatchLifecycle::IsNetworkFlushBindingValid(
               true, true, false, true, false),
        "a different hook driver rejects the flush");

    Outbox cancelled;
    const Scope cancelledScope = ScopeFor("cancelled");
    Expect(cancelled.MarkResultFrozen(cancelledScope),
        "cancel test can record result freeze");
    Expect(cancelled.ConfirmResult(cancelledScope),
        "cancel test can create a result receipt");
    cancelled.CancelProductionAllocationScope(
        cancelledScope.attemptId, cancelledScope.authoritySessionId,
        cancelledScope.worldInstanceId, cancelledScope.rosterRevision,
        cancelledScope.routeGeneration);
    Expect(cancelled.Poll().at("events").size() == 1,
        "clear keeps already-produced receipts until ACK");
    Expect(!cancelled.ArmReturn(cancelledScope),
        "clear prevents later natural return production");

    Outbox independentMatch;
    const Scope secondScope = ScopeFor("second");
    Expect(independentMatch.MarkResultFrozen(secondScope),
        "fresh process can record result freeze");
    Expect(independentMatch.ConfirmResult(secondScope),
        "a fresh process starts its event sequence at one");
    Expect(independentMatch.Poll().at("events").at(0).at("event_seq") == 1,
        "event sequence is scoped to the match process");

    std::cout << "Match lifecycle wire fixture tests passed\n";
    return 0;
}
