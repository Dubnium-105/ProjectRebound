#include "../Admission/StrictRosterPolicy.h"

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

    std::string Base64Url(const std::string& input)
    {
        static constexpr char alphabet[] =
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        std::string result;
        unsigned int accumulator = 0;
        unsigned int bits = 0;
        for (const unsigned char byte : input)
        {
            accumulator = (accumulator << 8U) | byte;
            bits += 8U;
            while (bits >= 6U)
            {
                bits -= 6U;
                result.push_back(alphabet[(accumulator >> bits) & 63U]);
                accumulator &= (1U << bits) - 1U;
            }
        }
        if (bits > 0U)
            result.push_back(alphabet[(accumulator << (6U - bits)) & 63U]);
        return result;
    }

    std::string Token(const nlohmann::json& claims)
    {
		const std::string type = claims.contains("roster")
			? "match-allocation+jwt" : "match-join+jwt";
		const nlohmann::json header{
			{"alg", "EdDSA"}, {"typ", type}, {"kid", "adm_1"}};
        return Base64Url(header.dump()) + "." + Base64Url(claims.dump()) + ".AA";
    }

    nlohmann::json AllocationClaims()
    {
        return {
			{"iss", "game-control-plane"},
			{"aud", "project-rebound-match-authority"}, {"kid", "adm_1"},
			{"jti", "allocation_jti_1"},
            {"attempt_id", "att_1"}, {"lobby_id", "lby_1"},
            {"hosting_kind", "P2P"}, {"authority_id", "p_host"},
            {"authority_session_id", "auth_session_1"},
			{"roster_revision", 3}, {"route_generation", 1},
			{"initial_connection_window_seconds", 120},
            {"nbf", 90}, {"exp", 200},
            {"roster", nlohmann::json::array({
                {{"player_id", "p_host"}, {"platform_id", "steam_host"},
                 {"room_role", "HOST"}, {"team_id", 1}, {"team_slot", 0},
                 {"logical_slot", 0}, {"connection_generation", 1}},
                {{"player_id", "p_member"}, {"platform_id", "steam_member"},
                 {"room_role", "MEMBER"}, {"team_id", 2}, {"team_slot", 0},
                 {"logical_slot", 32}, {"connection_generation", 1}}
            })}
        };
    }

    nlohmann::json GrantClaims(
        const int generation,
        const std::string& jti,
        const std::string& worldInstanceId = "world_test_a")
    {
        return {
			{"iss", "game-control-plane"},
			{"aud", "project-rebound-match-client"}, {"kid", "adm_1"}, {"jti", jti},
            {"attempt_id", "att_1"}, {"lobby_id", "lby_1"},
            {"hosting_kind", "P2P"},
            {"authority_id", "p_host"}, {"authority_session_id", "auth_session_1"},
            {"world_instance_id", worldInstanceId},
            {"player_id", "p_member"}, {"platform_id", "steam_member"},
            {"roster_revision", 3}, {"team_id", 2}, {"team_slot", 0},
            {"logical_slot", 32}, {"connection_generation", generation},
            {"route_generation", 1}, {"nbf", 90}, {"exp", 160}
        };
    }

    std::string NativeNonce(const char* suffix)
    {
        return std::string("native_nonce_0123456789_") + suffix;
    }
}

int main()
{
    const auto verifier = [](std::span<const std::uint8_t> key,
                             std::string_view data,
                             std::span<const std::uint8_t> signature) {
        return key.size() == 32U && !data.empty() && !signature.empty();
    };
    constexpr char publicKey[] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

    StrictRoster::Policy disabled(verifier, false);
    Expect(disabled.InstallAllocation(Token(AllocationClaims()), "adm_1", publicKey, 100).accepted,
        "signed allocation should install before native activation");
    Expect(!disabled.StartAuthority("steam_host", 100).accepted,
        "unverified native path must fail closed");

	StrictRoster::Policy expiring(verifier, true);
	auto shortAllocation = AllocationClaims();
	shortAllocation["exp"] = 105;
	Expect(expiring.InstallAllocation(Token(shortAllocation), "adm_1", publicKey, 100).accepted,
		"short allocation should install");
	Expect(expiring.StartAuthority("steam_host", 100).accepted,
		"short allocation should start while live");
    expiring.SetNativeWorldInstanceId("world_test_a");
    const auto expiredDecision = expiring.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_after_allocation_expiry")), "steam_member", 106,
        NativeNonce("expiry"));
	Expect(!expiredDecision.accepted && expiredDecision.code == "allocation_expired",
		"join grants must not outlive their authority allocation");

    StrictRoster::Policy policy(verifier, true);
    Expect(policy.InstallAllocation(Token(AllocationClaims()), "adm_1", publicKey, 100).accepted,
        "allocation should install");
    Expect(policy.StartAuthority("steam_host", 100).accepted,
        "the allocated host should bind locally");
    policy.SetNativeWorldInstanceId("world_test_a");
	Expect(!policy.StartAuthorityForAllocatedHost("", 100, NativeNonce("host")).accepted,
		"P2P host startup rejects an empty local platform identity");
	Expect(!policy.StartAuthorityForAllocatedHost("steam_other", 100, NativeNonce("host")).accepted,
		"P2P host startup rejects a non-rostered local platform identity");
	Expect(policy.StartAuthorityForAllocatedHost("steam_host", 100, NativeNonce("host")).accepted,
		"P2P host startup binds the signed HOST seat to the local identity");
	Expect(!policy.StartAuthorityForAllocatedHost("steam_host", 100, NativeNonce("host-race")).accepted,
		"a competing native host handshake nonce cannot replace the reserved HOST seat");
	const auto hostGrant = GrantClaims(1, "host_grant_jti");
	auto hostClaims = hostGrant;
	hostClaims["player_id"] = "p_host";
	hostClaims["platform_id"] = "steam_host";
	hostClaims["team_id"] = 1;
	hostClaims["logical_slot"] = 0;
	Expect(!policy.ValidateJoinGrant(Token(hostClaims), "steam_host", 110, NativeNonce("forged-host")).accepted,
		"the local P2P host must not attach through a remote grant");
	auto forgedTeam = GrantClaims(1, "forged_team_jti");
	forgedTeam["team_id"] = 1;
	Expect(!policy.ValidateJoinGrant(Token(forgedTeam), "steam_member", 110, NativeNonce("forged-team")).accepted,
		"a signed identity cannot claim a team other than its frozen seat");
    const auto first = policy.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_jti_1")), "steam_member", 110,
        NativeNonce("one"));
    Expect(first.accepted && first.teamId == 2 && first.logicalSlot == 32,
        "grant should recover its frozen team and logical seat");
    Expect(first.nativeConnectionNonce == NativeNonce("one"),
        "native reservation must retain the concrete handshake nonce");
    const auto competingNonce = policy.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_jti_1")), "steam_member", 110,
        NativeNonce("competing"));
    Expect(!competingNonce.accepted &&
        competingNonce.code == "native_connection_nonce_conflict",
        "a different nonce cannot reserve the same grant and generation");
    Expect(!policy.MarkConnected(
        first.playerId, first.connectionGeneration, first.grantJti,
        first.nativeConnectionNonce).accepted,
        "native reservation cannot mark CONNECTED before backend confirmation");
    Expect(policy.ConfirmAdmissionReserved(
        first.playerId, first.connectionGeneration, first.grantJti,
        first.nativeConnectionNonce).accepted,
        "backend ReserveAdmission should bind the same grant reservation");
    Expect(!policy.ConfirmAdmissionReserved(
        first.playerId, first.connectionGeneration, first.grantJti,
        NativeNonce("wrong-reservation")).accepted,
        "backend reservation with another native nonce must be rejected");
    Expect(policy.MarkNativeAdmitted(
        first.playerId, first.connectionGeneration, first.grantJti,
        first.nativeConnectionNonce).accepted,
        "native team/camp readback should remain a pre-confirmation state");
    Expect(policy.MarkConnected(
        first.playerId, first.connectionGeneration, first.grantJti,
        first.nativeConnectionNonce).accepted,
        "native PostLogin should confirm the connected generation");
    Expect(policy.MarkConnected(
        first.playerId, first.connectionGeneration, first.grantJti,
        first.nativeConnectionNonce).accepted,
        "connected reporting should be idempotent");
    auto connectionEvents = policy.ConnectionEventsAfter(0);
    Expect(connectionEvents.size() == 3 &&
        connectionEvents[0].state == "RESERVED" &&
        connectionEvents[1].state == "NATIVE_ADMITTED" &&
        connectionEvents[2].state == "CONNECTED" &&
        connectionEvents.back().connected &&
        connectionEvents.back().connectionGeneration == 1 &&
        connectionEvents.back().grantJti == "grant_jti_1" &&
        connectionEvents[0].worldInstanceId == "world_test_a" &&
        connectionEvents[1].worldInstanceId == "world_test_a" &&
        connectionEvents[2].worldInstanceId == "world_test_a" &&
        connectionEvents[0].routeGeneration == 1 &&
        connectionEvents[1].routeGeneration == 1 &&
        connectionEvents[2].routeGeneration == 1 &&
        connectionEvents[0].nativeConnectionNonce == NativeNonce("one") &&
        connectionEvents[1].nativeConnectionNonce == NativeNonce("one") &&
        connectionEvents[2].nativeConnectionNonce == NativeNonce("one"),
        "native reservation, readback, and backend confirmation should be correlated");
    Expect(!policy.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_jti_1")), "steam_member", 110,
        NativeNonce("replay")).accepted,
        "JTI replay must be rejected");
	Expect(!policy.ValidateJoinGrant(
		Token(GrantClaims(1, "same_generation_second_jti")), "steam_member", 110,
        NativeNonce("second")).accepted,
		"two live connections cannot occupy the same generation and seat");
    policy.SetNativeWorldInstanceId("world_test_b");
    Expect(policy.MarkDisconnected(first.playerId, first.connectionGeneration,
        first.nativeConnectionNonce).accepted,
        "authority logout should release the connected generation");
    Expect(policy.MarkDisconnected(first.playerId, first.connectionGeneration,
        first.nativeConnectionNonce).accepted,
        "disconnect reporting should be idempotent");
    Expect(!policy.MarkDisconnected(first.playerId, first.connectionGeneration,
        NativeNonce("wrong-disconnect")).accepted,
        "a stale disconnect nonce cannot mutate the already released generation");
    connectionEvents = policy.ConnectionEventsAfter(connectionEvents.back().sequence);
    Expect(connectionEvents.size() == 1 && !connectionEvents[0].connected &&
        connectionEvents[0].connectionGeneration == 1 &&
        connectionEvents[0].worldInstanceId == "world_test_a" &&
        connectionEvents[0].routeGeneration == 1,
        "native logout must retain the connection's admission world and route");
    const auto replacement = policy.ValidateJoinGrant(
        Token(GrantClaims(2, "grant_jti_2", "world_test_b")), "steam_member", 111,
        NativeNonce("two"));
    Expect(replacement.accepted && !replacement.replacesConnection,
        "next generation should reclaim the disconnected frozen seat");
    Expect(policy.ConfirmAdmissionReserved(
        replacement.playerId, replacement.connectionGeneration, replacement.grantJti,
        replacement.nativeConnectionNonce).accepted,
        "replacement generation should require a fresh backend reservation");
    Expect(policy.MarkNativeAdmitted(
        replacement.playerId, replacement.connectionGeneration, replacement.grantJti,
        replacement.nativeConnectionNonce).accepted,
        "replacement native admission should remain quarantined");
    Expect(policy.MarkConnected(
        replacement.playerId, replacement.connectionGeneration, replacement.grantJti,
        replacement.nativeConnectionNonce).accepted,
        "the reconnected generation should be reportable after native seat application");
    const auto latest = policy.ValidateJoinGrant(
        Token(GrantClaims(4, "grant_jti_4", "world_test_b")), "steam_member", 112,
        NativeNonce("four"));
    Expect(latest.accepted && latest.replacesConnection,
        "a newer signed generation should invalidate skipped grants and replace the seat");
	Expect(!policy.ValidateJoinGrant(
		Token(GrantClaims(3, "grant_jti_3", "world_test_b")), "someone_else", 112,
        NativeNonce("wrong-player")).accepted,
        "platform identity mismatch must be rejected");

	auto renewed = AllocationClaims();
	renewed["jti"] = "allocation_jti_renewed";
	renewed["exp"] = 260;
	renewed["roster"][1]["connection_generation"] = 4;
	Expect(policy.InstallAllocation(Token(renewed), "adm_1", publicKey, 120).accepted,
		"same-route allocation renewal should be idempotent");
	Expect(!policy.ValidateJoinGrant(
		Token(GrantClaims(1, "grant_jti_1", "world_test_b")), "steam_member", 121,
        NativeNonce("old-route")).accepted,
		"allocation renewal must preserve consumed JTI replay state");

	auto recovered = AllocationClaims();
	recovered["jti"] = "allocation_jti_route_2";
	recovered["route_generation"] = 2;
	recovered["roster"][0]["connection_generation"] = 2;
	recovered["roster"][1]["connection_generation"] = 5;
	Expect(policy.InstallAllocation(Token(recovered), "adm_1", publicKey, 122).accepted,
		"one-step route recovery should refresh signed seat generations");
	auto routeTwoGrant = GrantClaims(5, "grant_jti_route_2", "world_test_b");
	routeTwoGrant["route_generation"] = 2;
	const auto beforeRecoveryReservationEvents =
		policy.ConnectionEventsAfter(0).size();
	const auto afterRecovery = policy.ValidateJoinGrant(
		Token(routeTwoGrant), "steam_member", 123, NativeNonce("route-two"));
	Expect(afterRecovery.accepted && afterRecovery.replacesConnection,
		"route recovery should reserve a new generation while retaining the old live connection");
    const auto recoveryEvents = policy.ConnectionEventsAfter(0);
    Expect(recoveryEvents.size() == beforeRecoveryReservationEvents + 1U &&
        recoveryEvents.back().state == "RESERVED" &&
        recoveryEvents.back().connected == false &&
        recoveryEvents.back().worldInstanceId == "world_test_b" &&
        recoveryEvents.back().routeGeneration == 2,
        "route refresh reservation must not emit a connected event before native confirmation");
	Expect(!policy.InstallAllocation(Token(renewed), "adm_1", publicKey, 124).accepted,
		"a prior route allocation must be rejected after recovery");

    StrictRoster::Policy hostRecovery(verifier, true);
    auto hostRecoveryAllocation = AllocationClaims();
    hostRecoveryAllocation["jti"] = "allocation_jti_host_recovery";
    Expect(hostRecovery.InstallAllocation(
        Token(hostRecoveryAllocation), "adm_1", publicKey, 100).accepted,
        "host recovery allocation should install");
    const auto hostReservation = hostRecovery.StartAuthorityForAllocatedHost(
        "steam_host", 100, NativeNonce("host-live-one"));
    Expect(hostReservation.accepted && hostReservation.connectionGeneration == 1,
        "host recovery should reserve the first native generation");
    Expect(hostRecovery.ConfirmAdmissionReserved(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.grantJti, hostReservation.nativeConnectionNonce).accepted,
        "host recovery should confirm its first backend reservation");
    Expect(!hostRecovery.ConfirmConnected(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.grantJti, hostReservation.nativeConnectionNonce).accepted,
        "a local HOST cannot become connected before native seat readback");
    Expect(!hostRecovery.MarkNativeAdmitted(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.grantJti, hostReservation.nativeConnectionNonce).accepted,
        "a cold HOST cannot emit admission before its world has been observed");
    Expect(hostRecovery.ConnectionEventsAfter(0).empty(),
        "pending HOST world observation must not manufacture worldless events");
    hostRecovery.SetNativeWorldInstanceId("world_host");
    Expect(hostRecovery.MarkNativeAdmitted(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.grantJti, hostReservation.nativeConnectionNonce).accepted,
        "observed HOST world and native seat readback admit the reserved generation");
    Expect(hostRecovery.ConfirmConnected(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.grantJti, hostReservation.nativeConnectionNonce).accepted,
        "host recovery should mark the first native generation live");
    auto hostRecoveryRefresh = AllocationClaims();
    hostRecoveryRefresh["jti"] = "allocation_jti_host_recovery_2";
    hostRecoveryRefresh["route_generation"] = 2;
    hostRecoveryRefresh["roster"][0]["connection_generation"] = 2;
    hostRecoveryRefresh["roster"][1]["connection_generation"] = 2;
    Expect(hostRecovery.InstallAllocation(
        Token(hostRecoveryRefresh), "adm_1", publicKey, 101).accepted,
        "host recovery should accept a one-step route refresh");
    Expect(!hostRecovery.StartAuthorityForAllocatedHost(
        "steam_host", 101, NativeNonce("host-live-two")).accepted,
        "host recovery must not promote an old live socket to the new generation");
    Expect(hostRecovery.MarkDisconnected(
        hostReservation.playerId, hostReservation.connectionGeneration,
        hostReservation.nativeConnectionNonce).accepted,
        "host recovery should clear the old live generation with its nonce");
    const auto hostEvents = hostRecovery.ConnectionEventsAfter(0);
    Expect(hostEvents.size() == 3 && hostEvents.back().state == "DISCONNECTED" &&
        hostEvents.back().worldInstanceId == "world_host" && hostEvents.back().routeGeneration == 1,
        "old HOST disconnect must preserve the admitted route after allocation refresh");
    const auto hostReplacement = hostRecovery.StartAuthorityForAllocatedHost(
        "steam_host", 101, NativeNonce("host-live-two"));
    Expect(hostReplacement.accepted && hostReplacement.connectionGeneration == 2 &&
        hostReplacement.nativeConnectionNonce == NativeNonce("host-live-two"),
        "host recovery should require a fresh nonce for the new generation");

	policy.Reset();
	Expect(!policy.ValidateJoinGrant(
		Token(routeTwoGrant), "steam_member", 125, NativeNonce("route-two-late")).accepted,
		"clearing a completed assignment must revoke its in-memory admission state");
	Expect(!policy.StartAuthority("steam_host", 125).accepted,
		"cleared allocation must not restart an authority");

    StrictRoster::Policy nativeGrantValidation(verifier, true);
    Expect(nativeGrantValidation.InstallAllocation(
        Token(AllocationClaims()), "adm_1", publicKey, 100).accepted,
        "native grant validation allocation should install");
    Expect(nativeGrantValidation.StartAuthority("steam_host", 100).accepted,
        "native grant validation authority should start");
    nativeGrantValidation.SetNativeWorldInstanceId("world_test_a");
    const std::string nativeGrant = Token(GrantClaims(1, "native_grant_jti"));
    Expect(nativeGrantValidation.StageJoinGrant(nativeGrant, 110).accepted,
        "the backend-staged grant should be retained for native login");
    Expect(nativeGrantValidation.ValidateNativeJoinGrant(
        nativeGrant, "steam_member", 110).accepted,
        "NMT_Login must validate the exact staged signed grant without consuming it");
    Expect(nativeGrantValidation.ValidateNativeJoinGrant(
        nativeGrant, "steam_member", 110).accepted,
        "reliable NMT_Login retransmit validation must remain idempotent");
    auto mismatchedNativeGrant = GrantClaims(1, "native_grant_jti");
    mismatchedNativeGrant["logical_slot"] = 33;
    Expect(!nativeGrantValidation.ValidateNativeJoinGrant(
        Token(mismatchedNativeGrant), "steam_member", 110).accepted,
        "a signed grant with the staged JTI but different seat claims must be rejected");

    StrictRoster::Policy worldBinding(verifier, true);
    Expect(worldBinding.InstallAllocation(
        Token(AllocationClaims()), "adm_1", publicKey, 100).accepted,
        "world binding allocation should install");
    Expect(worldBinding.StartAuthority("steam_host", 100).accepted,
        "world binding authority should start");
    worldBinding.SetNativeWorldInstanceId("world_test_a");
    auto missingWorldGrant = GrantClaims(1, "world_missing_jti");
    missingWorldGrant.erase("world_instance_id");
    const auto missingWorldDecision = worldBinding.StageJoinGrant(
        Token(missingWorldGrant), 110);
    Expect(!missingWorldDecision.accepted &&
        missingWorldDecision.code == "grant_world_mismatch",
        "a Join Grant without the signed native world must be rejected at staging");
    auto wrongWorldGrant = GrantClaims(1, "world_wrong_jti", "world_test_b");
    const auto wrongWorldDecision = worldBinding.StageJoinGrant(
        Token(wrongWorldGrant), 110);
    Expect(!wrongWorldDecision.accepted &&
        wrongWorldDecision.code == "grant_world_mismatch",
        "a Join Grant for another native world must be rejected at staging");
    const std::string worldBoundGrant = Token(
        GrantClaims(1, "world_bound_jti", "world_test_a"));
    Expect(worldBinding.StageJoinGrant(worldBoundGrant, 110).accepted,
        "a Join Grant for the observed native world should stage");
    Expect(worldBinding.StageJoinGrant(worldBoundGrant, 110).accepted,
        "retransmitting the same world-bound Join Grant should remain idempotent");
    Expect(worldBinding.ValidateNativeJoinGrant(
        worldBoundGrant, "steam_member", 110).accepted,
        "the staged Join Grant should validate while its world is current");
    worldBinding.SetNativeWorldInstanceId("world_test_b");
    const auto staleWorldValidation = worldBinding.ValidateNativeJoinGrant(
        worldBoundGrant, "steam_member", 110);
    Expect(!staleWorldValidation.accepted &&
        staleWorldValidation.code == "grant_world_mismatch",
        "NMT_Login validation must reject a staged grant after the authority world changes");
    const auto staleWorldReservation = worldBinding.ReserveAdmission(
        "steam_member", 110, NativeNonce("world-binding"));
    Expect(!staleWorldReservation.accepted &&
        staleWorldReservation.code == "grant_world_mismatch",
        "native reservation must reject a staged grant after the authority world changes");
    worldBinding.SetNativeWorldInstanceId("world_test_a");
    Expect(worldBinding.ValidateNativeJoinGrant(
        worldBoundGrant, "steam_member", 110).accepted,
        "the exact staged grant should remain retryable when its world returns");
    const auto worldReservation = worldBinding.ReserveAdmission(
        "steam_member", 110, NativeNonce("world-binding"));
    Expect(worldReservation.accepted,
        "native reservation should bind the exact signed world after it is current again");
    return 0;
}
