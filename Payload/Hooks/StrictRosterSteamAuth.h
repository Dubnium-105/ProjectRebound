#pragma once

#include <cstddef>
#include <cstdint>
#include <limits>
#include <string>
#include <string_view>

namespace StrictRosterSteamAuth
{
    // The Steamworks ValidateAuthTicketResponse_t callback is the only native
    // platform proof accepted by the online PreLogin gate.  This module never
    // accepts a self-reported UniqueNetId and never exposes ticket bytes.
    constexpr std::uint32_t kValidateAuthTicketResponseCallback = 143U;
    constexpr std::size_t kValidateAuthTicketResponseBytes = 20U;
    constexpr std::int32_t kAuthSessionResponseOk = 0;
    constexpr std::uint8_t kCallbackFlagsGameServer = 0x02U;
    constexpr std::uint64_t kProofLifetimeMilliseconds = 15'000ULL;
    // A verified native connection binding has connection scope, not a
    // wall-clock lease.  Disconnect, Steam revocation, and allocation cleanup
    // are the only release authorities; UINT64_MAX is the in-memory sentinel
    // used to make accidental timer-based reuse impossible.
    constexpr std::uint64_t kConnectionBindingLifetimeMilliseconds =
        (std::numeric_limits<std::uint64_t>::max)();
    constexpr std::uint64_t kValidateAuthTicketWaitMilliseconds = 3'000ULL;

    enum class ClientTicketState : std::uint8_t
    {
        Unavailable,
        Pending,
        Ready,
    };

    // Register the callback against the already initialized Steam API. A
    // false result is fail-closed: PreLogin must reject online connections.
    // The gameserver flag is selected only for a real -server authority;
    // client and gameserver callback queues are distinct in Steamworks.
    bool Initialize(bool gameserver) noexcept;
    void Shutdown() noexcept;

    // Client-side carrier.  The ticket is requested through the fixed
    // SteamUser interface and is only exposed as base64url for the exact
    // NMT_Login URL carrier; raw bytes never leave this module or enter logs.
    ClientTicketState InitializeClientTicket() noexcept;
    ClientTicketState RequestClientAuthTicket() noexcept;
    ClientTicketState GetClientTicketState() noexcept;
    bool CopyClientAuthTicket(std::string& encodedTicket) noexcept;
    void CancelClientAuthTicket() noexcept;

    // Read the authenticated local Steam user from the initialized
    // SteamUser interface.  This is used only for the signed P2P HOST seat;
    // the game's FUniqueNetId string is the separate backend player_id.
    bool TryGetLocalPlatformId(std::string& platformId) noexcept;

    // Dedicated authority side.  BeginAuthSession is issued only after the
    // exact Grant/JTI has been armed for this nonce and ticket carrier.  The
    // successful Steam callback can therefore wake only this pending scope.
    bool BeginServerAuthSession(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce,
        const std::uint8_t* ticketBytes,
        std::size_t ticketByteCount,
        std::uint64_t nowMilliseconds) noexcept;
    void EndServerAuthSession(std::string_view nativeConnectionNonce) noexcept;
    void CancelExpectedGrant(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce) noexcept;

#if defined(STRICT_ROSTER_STEAM_AUTH_TEST)
    bool MarkExpectedGrantAuthStartedForTest(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce) noexcept;
#endif

    // Arm one exact staged Grant before waiting for the callback. A callback
    // for an identity with no expected Grant is ignored, so this is not a
    // global SteamID proof pool.
    bool ArmExpectedGrant(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce,
        std::uint64_t nowMilliseconds) noexcept;

    // This is called by the registered official callback ABI. It is public so
    // the bounded regression harness can feed the exact 20-byte callback
    // shape without loading the game SDK.
    void ObserveValidateAuthTicketResponse(
        const void* payload,
        std::size_t payloadBytes,
        std::uint64_t nowMilliseconds) noexcept;

    // Consume one callback proof for the exact platform/Grant and bind it to
    // the native handshake nonce. Different Grant JTIs cannot steal it.
    bool ConsumeExpectedGrantProof(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce,
        std::uint64_t nowMilliseconds) noexcept;

    // Wait only on the dedicated gameserver callback pump.  The caller must
    // use a bounded timeout; this never pumps or re-enters the UE client
    // dispatcher from the game thread.
    bool WaitForExpectedGrantProof(
        std::string_view platformId,
        std::string_view grantJti,
        std::string_view nativeConnectionNonce,
        std::uint64_t nowMilliseconds,
        std::uint64_t timeoutMilliseconds) noexcept;

    bool IsConnectionBindingActive(
        std::string_view platformId,
        std::string_view nativeConnectionNonce,
        std::uint64_t nowMilliseconds) noexcept;

    void RevokeConnectionBinding(std::string_view nativeConnectionNonce) noexcept;

    // A non-OK Steam response queues the exact active native nonce for
    // game-thread recovery.  The queue carries no Steam identity or ticket.
    bool TryConsumeRevokedConnectionNonce(std::string& nativeConnectionNonce) noexcept;

    // Allocation/attempt cleanup revokes any expected callback or binding;
    // it is intentionally scoped by the current native owner in the caller.
    void ClearAllExpectedProofs() noexcept;

    // Used by status/debug diagnostics without exposing any identity value.
    bool CallbackRegistered() noexcept;
}
