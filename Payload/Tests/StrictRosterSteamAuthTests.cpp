#include "../Hooks/StrictRosterSteamAuth.h"

#include <array>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <string_view>

namespace
{
    int failures = 0;

    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            ++failures;
            std::cerr << "FAIL: " << message << '\n';
        }
    }

    std::array<std::uint8_t,
        StrictRosterSteamAuth::kValidateAuthTicketResponseBytes> MakeResponse(
            const std::uint64_t steamId,
            const std::int32_t result)
    {
        std::array<std::uint8_t,
            StrictRosterSteamAuth::kValidateAuthTicketResponseBytes> payload{};
        const std::uint64_t owner = steamId;
        std::memcpy(payload.data(), &steamId, sizeof(steamId));
        std::memcpy(payload.data() + sizeof(steamId), &result, sizeof(result));
        std::memcpy(
            payload.data() + sizeof(steamId) + sizeof(result),
            &owner,
            sizeof(owner));
        return payload;
    }
}

int main()
{
    constexpr std::uint64_t steamId = 76561198123456789ULL;
    constexpr std::string_view platformId = "76561198123456789";
    constexpr std::string_view jti = "jti-1";
    constexpr std::string_view nonce =
        "0123456789abcdef0123456789abcdef";
    const auto success = MakeResponse(
        steamId, StrictRosterSteamAuth::kAuthSessionResponseOk);

    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(platformId, jti, nonce, 1'000U),
        "the exact staged platform/JTI can arm a pending native proof");
    Expect(
        StrictRosterSteamAuth::MarkExpectedGrantAuthStartedForTest(
            platformId, jti, nonce),
        "the pending test scope records its BeginAuthSession request");
    StrictRosterSteamAuth::ObserveValidateAuthTicketResponse(
        success.data(), success.size(), 1'001U);
    Expect(
        StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, jti, nonce, 1'002U),
        "matching SteamID/JTI consumes and binds a successful callback proof");
    Expect(
        StrictRosterSteamAuth::IsConnectionBindingActive(
            platformId, nonce, 1'003U),
        "the native connection binding remains active after PreLogin");
    Expect(
        StrictRosterSteamAuth::IsConnectionBindingActive(
            platformId, nonce, 1'003U + 12ULL * 60ULL * 1000ULL),
        "a live native connection binding has no wall-clock lease");
    Expect(
        StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, jti, nonce, 1'004U),
        "a retry for the same nonce/JTI is idempotent");
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, "jti-other", "fedcba9876543210", 1'005U),
        "a different JTI cannot consume an already-bound handshake");
    StrictRosterSteamAuth::RevokeConnectionBinding(nonce);
    Expect(
        !StrictRosterSteamAuth::IsConnectionBindingActive(
            platformId, nonce, 1'006U),
        "disconnect revokes the native connection binding");

    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(platformId, jti, "c", 2'000U),
        "a new handshake can arm the same platform after disconnect");
    Expect(
        StrictRosterSteamAuth::MarkExpectedGrantAuthStartedForTest(
            platformId, jti, "c"),
        "the second test scope records its BeginAuthSession request");
    StrictRosterSteamAuth::ObserveValidateAuthTicketResponse(
        success.data(), success.size(), 2'001U);
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, "jti-other", "a", 2'002U),
        "a wrong JTI is rejected even when the SteamID callback matches");
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            "76561198123456788", jti, "b", 2'003U),
        "a different platform identity cannot consume the callback");
    Expect(
        StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, jti, "c", 2'004U),
        "the exact second staged JTI consumes its own callback");

    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(
            platformId, "jti-expire", "expire", 3'000U),
        "an expiry test can arm a new JTI after consumption");
    Expect(
        StrictRosterSteamAuth::MarkExpectedGrantAuthStartedForTest(
            platformId, "jti-expire", "expire"),
        "the expiry test scope records its BeginAuthSession request");
    StrictRosterSteamAuth::ObserveValidateAuthTicketResponse(
        success.data(), success.size(), 3'001U);
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, "jti-expire", "expire", 18'100U),
        "an expired callback proof is rejected");

    const std::array<std::uint8_t, 12> shortPayload{};
    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(
            platformId, "jti-short", "short", 20'000U),
        "a short-payload test can arm a fresh expected JTI");
    StrictRosterSteamAuth::ObserveValidateAuthTicketResponse(
        shortPayload.data(), shortPayload.size(), 20'001U);
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, "jti-short", "short", 20'002U),
        "a short callback payload cannot authorize a platform");
    StrictRosterSteamAuth::ClearAllExpectedProofs();

    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(
            platformId, "jti-fail", "fail", 21'000U),
        "a failure callback test can arm a fresh expected JTI");
    Expect(
        StrictRosterSteamAuth::MarkExpectedGrantAuthStartedForTest(
            platformId, "jti-fail", "fail"),
        "the failure test scope records its BeginAuthSession request");
    const auto failure = MakeResponse(steamId, 5);
    StrictRosterSteamAuth::ObserveValidateAuthTicketResponse(
        failure.data(), failure.size(), 21'001U);
    Expect(
        !StrictRosterSteamAuth::ConsumeExpectedGrantProof(
            platformId, "jti-fail", "fail", 21'002U),
        "a non-OK callback revokes the pending platform proof");

    Expect(
        StrictRosterSteamAuth::ArmExpectedGrant(
            platformId, "jti-race-a", "race-a", 22'000U),
        "the first competing JTI is accepted");
    Expect(
        !StrictRosterSteamAuth::ArmExpectedGrant(
            platformId, "jti-race-b", "race-b", 22'001U),
        "a competing JTI cannot replace the same platform pending handshake");
    StrictRosterSteamAuth::ClearAllExpectedProofs();
    Expect(
        !StrictRosterSteamAuth::IsConnectionBindingActive(
            platformId, "c", 22'002U),
        "allocation cleanup clears all native proof bindings");

    std::cout << (failures == 0 ? "PASS" : "FAIL")
              << " strict roster Steam auth tests failures=" << failures << '\n';
    return failures == 0 ? 0 : 1;
}
