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

    nlohmann::json GrantClaims(const int generation, const std::string& jti)
    {
        return {
			{"iss", "game-control-plane"},
			{"aud", "project-rebound-match-client"}, {"kid", "adm_1"}, {"jti", jti},
            {"attempt_id", "att_1"}, {"lobby_id", "lby_1"},
			{"hosting_kind", "P2P"},
            {"authority_id", "p_host"}, {"authority_session_id", "auth_session_1"},
            {"player_id", "p_member"}, {"platform_id", "steam_member"},
            {"roster_revision", 3}, {"team_id", 2}, {"team_slot", 0},
            {"logical_slot", 32}, {"connection_generation", generation},
            {"route_generation", 1}, {"nbf", 90}, {"exp", 160}
        };
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
	const auto expiredDecision = expiring.ValidateJoinGrant(
		Token(GrantClaims(1, "grant_after_allocation_expiry")), "steam_member", 106);
	Expect(!expiredDecision.accepted && expiredDecision.code == "allocation_expired",
		"join grants must not outlive their authority allocation");

    StrictRoster::Policy policy(verifier, true);
    Expect(policy.InstallAllocation(Token(AllocationClaims()), "adm_1", publicKey, 100).accepted,
        "allocation should install");
    Expect(policy.StartAuthority("steam_host", 100).accepted,
        "the allocated host should bind locally");
	const auto hostGrant = GrantClaims(1, "host_grant_jti");
	auto hostClaims = hostGrant;
	hostClaims["player_id"] = "p_host";
	hostClaims["platform_id"] = "steam_host";
	hostClaims["team_id"] = 1;
	hostClaims["logical_slot"] = 0;
	Expect(!policy.ValidateJoinGrant(Token(hostClaims), "steam_host", 110).accepted,
		"the local P2P host must not attach through a remote grant");
	auto forgedTeam = GrantClaims(1, "forged_team_jti");
	forgedTeam["team_id"] = 1;
	Expect(!policy.ValidateJoinGrant(Token(forgedTeam), "steam_member", 110).accepted,
		"a signed identity cannot claim a team other than its frozen seat");
    const auto first = policy.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_jti_1")), "steam_member", 110);
    Expect(first.accepted && first.teamId == 2 && first.logicalSlot == 32,
        "grant should recover its frozen team and logical seat");
    Expect(!policy.ValidateJoinGrant(
        Token(GrantClaims(1, "grant_jti_1")), "steam_member", 110).accepted,
        "JTI replay must be rejected");
	Expect(!policy.ValidateJoinGrant(
		Token(GrantClaims(1, "same_generation_second_jti")), "steam_member", 110).accepted,
		"two live connections cannot occupy the same generation and seat");
    const auto replacement = policy.ValidateJoinGrant(
        Token(GrantClaims(2, "grant_jti_2")), "steam_member", 111);
    Expect(replacement.accepted && replacement.replacesConnection,
        "next generation should replace the existing seat connection");
    const auto latest = policy.ValidateJoinGrant(
        Token(GrantClaims(4, "grant_jti_4")), "steam_member", 112);
    Expect(latest.accepted && latest.replacesConnection,
        "a newer signed generation should invalidate skipped grants and replace the seat");
    Expect(!policy.ValidateJoinGrant(
        Token(GrantClaims(3, "grant_jti_3")), "someone_else", 112).accepted,
        "platform identity mismatch must be rejected");

	auto renewed = AllocationClaims();
	renewed["jti"] = "allocation_jti_renewed";
	renewed["exp"] = 260;
	renewed["roster"][1]["connection_generation"] = 4;
	Expect(policy.InstallAllocation(Token(renewed), "adm_1", publicKey, 120).accepted,
		"same-route allocation renewal should be idempotent");
	Expect(!policy.ValidateJoinGrant(
		Token(GrantClaims(1, "grant_jti_1")), "steam_member", 121).accepted,
		"allocation renewal must preserve consumed JTI replay state");

	auto recovered = AllocationClaims();
	recovered["jti"] = "allocation_jti_route_2";
	recovered["route_generation"] = 2;
	recovered["roster"][0]["connection_generation"] = 2;
	recovered["roster"][1]["connection_generation"] = 5;
	Expect(policy.InstallAllocation(Token(recovered), "adm_1", publicKey, 122).accepted,
		"one-step route recovery should refresh signed seat generations");
	auto routeTwoGrant = GrantClaims(5, "grant_jti_route_2");
	routeTwoGrant["route_generation"] = 2;
	const auto afterRecovery = policy.ValidateJoinGrant(
		Token(routeTwoGrant), "steam_member", 123);
	Expect(afterRecovery.accepted && !afterRecovery.replacesConnection,
		"route recovery should reserve the same logical seat without a stale live connection");
	Expect(!policy.InstallAllocation(Token(renewed), "adm_1", publicKey, 124).accepted,
		"a prior route allocation must be rejected after recovery");
	policy.Reset();
	Expect(!policy.ValidateJoinGrant(
		Token(routeTwoGrant), "steam_member", 125).accepted,
		"clearing a completed assignment must revoke its in-memory admission state");
	Expect(!policy.StartAuthority("steam_host", 125).accepted,
		"cleared allocation must not restart an authority");
    return 0;
}
