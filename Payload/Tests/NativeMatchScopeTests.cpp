#include "../ClientLogic/NativeMatchScope.h"

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

    NativeMatchScope MakeScope()
    {
        NativeMatchScope scope;
        scope.attemptId = "attempt-test";
        scope.authoritySessionId = "authority-session-test";
        scope.worldInstanceId = "world-authority-test";
        scope.rosterRevision = 7;
        scope.routeGeneration = 3;
        scope.playerId = "player-test";
        scope.grantJti = "grant-jti-test";
        scope.connectionGeneration = 2;
        return scope;
    }
}

int main()
{
    const NativeMatchScope source = MakeScope();
    std::string error;
    Expect(source.IsValid(&error), "complete scope is valid");

    const auto roundTrip = NativeMatchScope::FromJson(source.ToJson(), &error);
    Expect(roundTrip.has_value(), "scope JSON round trip parses");
    Expect(roundTrip->Matches(source), "scope JSON round trip preserves all fields");

    const nlohmann::json missing = {
        {"attempt_id", source.attemptId},
        {"authority_session_id", source.authoritySessionId},
        {"world_instance_id", source.worldInstanceId},
        {"roster_revision", source.rosterRevision},
        {"route_generation", source.routeGeneration},
        {"player_id", source.playerId},
        {"grant_jti", source.grantJti}
    };
    Expect(!NativeMatchScope::FromJson(missing, &error).has_value(),
        "scope without connection generation is rejected");

    NativeMatchScope changed = source;
    changed.routeGeneration++;
    Expect(!changed.Matches(source), "route changes do not match");

    NativeClientMatchConfirmation confirmation;
    confirmation.scope = source;
    confirmation.nativeConnectionNonce = "0123456789abcdef0123456789abcdef";
    confirmation.operationSequence = 12;
    Expect(confirmation.IsValid(&error), "scoped client confirmation is valid");
    const auto confirmationRoundTrip =
        NativeClientMatchConfirmation::FromJson(confirmation.ToJson(), &error);
    Expect(confirmationRoundTrip.has_value(), "confirmation JSON round trip parses");
    Expect(confirmationRoundTrip->scope.Matches(source),
        "confirmation preserves the exact scope");
    Expect(confirmationRoundTrip->nativeConnectionNonce ==
            confirmation.nativeConnectionNonce &&
            confirmationRoundTrip->operationSequence == confirmation.operationSequence,
        "confirmation preserves nonce and operation sequence");

    nlohmann::json memberConfirmation = confirmation.ToJson();
    memberConfirmation["room_role"] = "MEMBER";
    const auto memberConfirmationParsed =
        NativeClientMatchConfirmation::FromJson(memberConfirmation, &error);
    Expect(memberConfirmationParsed.has_value() && !memberConfirmationParsed->hostScope,
        "MEMBER confirmation keeps the remote-grant role");

    nlohmann::json badNonce = confirmation.ToJson();
    badNonce["native_connection_nonce"] = "short";
    Expect(!NativeClientMatchConfirmation::FromJson(badNonce, &error).has_value(),
        "short native nonce is rejected");

    nlohmann::json badOperation = confirmation.ToJson();
    badOperation["operation_sequence"] = 0;
    Expect(!NativeClientMatchConfirmation::FromJson(badOperation, &error).has_value(),
        "zero operation sequence is rejected");

    nlohmann::json whitespaceScope = source.ToJson();
    whitespaceScope["attempt_id"] = "   ";
    Expect(!NativeMatchScope::FromJson(whitespaceScope, &error).has_value(),
        "whitespace-only scope identity is rejected");

    nlohmann::json controlScope = source.ToJson();
    controlScope["grant_jti"] = "grant\ninvalid";
    Expect(!NativeMatchScope::FromJson(controlScope, &error).has_value(),
        "control characters in scope identity are rejected");

    nlohmann::json nestedConfirmation = confirmation.ToJson();
    nestedConfirmation["scope"] = nlohmann::json::object();
    Expect(!NativeClientMatchConfirmation::FromJson(nestedConfirmation, &error).has_value(),
        "nested confirmation scope is rejected in the flat wire form");

    nlohmann::json hostScope = source.ToJson();
    hostScope["grant_jti"] = "";
    hostScope["room_role"] = "HOST";
    const auto hostParsed = NativeMatchScope::FromHostJson(hostScope, &error);
    Expect(hostParsed.has_value(), "HOST scope accepts an explicitly empty grant JTI");
    NativeClientMatchConfirmation hostConfirmation;
    hostConfirmation.scope = *hostParsed;
    hostConfirmation.hostScope = true;
    hostConfirmation.nativeConnectionNonce = confirmation.nativeConnectionNonce;
    hostConfirmation.operationSequence = 13;
    const auto hostConfirmationParsed =
        NativeClientMatchConfirmation::FromJson(hostConfirmation.ToJson(), &error);
    Expect(hostConfirmationParsed.has_value() && hostConfirmationParsed->hostScope,
        "HOST confirmation preserves its explicit role");

    nlohmann::json hostWithGrant = hostScope;
    hostWithGrant["grant_jti"] = "member-jti";
    Expect(!NativeMatchScope::FromHostJson(hostWithGrant, &error).has_value(),
        "HOST scope rejects a non-empty grant JTI");

    std::cout << "NativeMatchScopeTests: PASS\n";
    return 0;
}
