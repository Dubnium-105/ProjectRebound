#pragma once

#include "../Libs/json.hpp"

#include <algorithm>
#include <cctype>
#include <cstdint>
#include <deque>
#include <functional>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

namespace StrictRoster
{
    struct Decision
    {
        bool accepted = false;
        std::string code;
        std::string message;
        // The validated Grant JTI is carried to the native Steam proof gate;
        // it prevents a callback for one staged handshake from authorizing a
        // different handshake for the same platform identity.
        std::string grantJti;
    };

    // Immutable identity carried by a match-allocation cleanup request.  The
    // Payload must validate this scope before it clears local admission state;
    // a delayed clear from an older Attempt must never reset a newer one.
    struct AllocationScope
    {
        std::string attemptId;
        std::string authoritySessionId;
        std::int64_t rosterRevision = 0;
        int routeGeneration = 0;
    };

    struct SeatDecision : Decision
    {
        std::string playerId;
        std::string platformId;
        std::string grantJti;
        // A fresh nonce identifies one concrete native handshake.  It is
        // deliberately separate from the Grant JTI and connection generation:
        // retrying that same handshake may reuse it, while a competing native
        // socket must receive a different nonce and be rejected.
        std::string nativeConnectionNonce;
        int teamId = 0;
        int teamSlot = -1;
        int logicalSlot = -1;
        int connectionGeneration = 0;
        bool replacesConnection = false;
        // A reservation is authorization for the native handshake only. It
        // is deliberately distinct from a confirmed live connection.
        bool reserved = false;
        bool confirmed = false;
        bool hostSeat = false;
        bool backendReserved = false;
    };

    struct ConnectionEvent
    {
        std::uint64_t sequence = 0;
        std::string attemptId;
        std::string playerId;
        std::string grantJti;
        std::string nativeConnectionNonce;
        int connectionGeneration = 0;
        int routeGeneration = 0;
        std::string authoritySessionId;
        std::int64_t rosterRevision = 0;
        std::string worldInstanceId;
        bool connected = false;
        // RESERVED and NATIVE_ADMITTED are observations before the backend
        // ConfirmConnected linearization point. CONNECTED/DISCONNECTED keep
        // the legacy boolean projection for consumers that only need those
        // terminal connection facts.
        std::string state;
    };

    using SignatureVerifier = std::function<bool(
        std::span<const std::uint8_t> publicKey,
        std::string_view signedData,
        std::span<const std::uint8_t> signature)>;

    namespace Detail
    {
        inline void SecureClear(std::string& value) noexcept
        {
            volatile char* data = value.empty() ? nullptr : value.data();
            for (std::size_t index = 0; data && index < value.size(); ++index)
                data[index] = 0;
            value.clear();
        }

        inline int Base64Value(const unsigned char ch) noexcept
        {
            if (ch >= 'A' && ch <= 'Z') return ch - 'A';
            if (ch >= 'a' && ch <= 'z') return ch - 'a' + 26;
            if (ch >= '0' && ch <= '9') return ch - '0' + 52;
            if (ch == '+' || ch == '-') return 62;
            if (ch == '/' || ch == '_') return 63;
            return -1;
        }

        inline bool DecodeBase64(
            const std::string_view encoded,
            std::vector<std::uint8_t>& decoded,
            const bool urlSafe)
        {
            decoded.clear();
            if (encoded.empty() || encoded.size() > 60U * 1024U)
                return false;
            unsigned int accumulator = 0;
            unsigned int bits = 0;
            bool padding = false;
            for (const unsigned char ch : encoded)
            {
                if (ch == '=')
                {
                    padding = true;
                    continue;
                }
                if (padding || (!urlSafe && (ch == '-' || ch == '_')) ||
                    (urlSafe && (ch == '+' || ch == '/')))
                {
                    return false;
                }
                const int value = Base64Value(ch);
                if (value < 0)
                    return false;
                accumulator = (accumulator << 6U) | static_cast<unsigned int>(value);
                bits += 6U;
                if (bits >= 8U)
                {
                    bits -= 8U;
                    decoded.push_back(static_cast<std::uint8_t>(accumulator >> bits));
                    accumulator &= (1U << bits) - 1U;
                }
            }
            return bits == 0U || accumulator == 0U;
        }

        struct Jwt
        {
            nlohmann::json header;
            nlohmann::json claims;
            std::string signedData;
            std::vector<std::uint8_t> signature;
        };

        inline std::optional<Jwt> ParseJwt(const std::string_view token)
        {
            const std::size_t first = token.find('.');
            const std::size_t second = first == std::string_view::npos
                ? std::string_view::npos
                : token.find('.', first + 1U);
            if (first == std::string_view::npos || second == std::string_view::npos ||
                token.find('.', second + 1U) != std::string_view::npos)
            {
                return std::nullopt;
            }
            std::vector<std::uint8_t> headerBytes;
            std::vector<std::uint8_t> claimsBytes;
            std::vector<std::uint8_t> signature;
            if (!DecodeBase64(token.substr(0, first), headerBytes, true) ||
                !DecodeBase64(token.substr(first + 1U, second - first - 1U), claimsBytes, true) ||
                !DecodeBase64(token.substr(second + 1U), signature, true) ||
                signature.empty())
            {
                return std::nullopt;
            }
            try
            {
                Jwt jwt;
                jwt.header = nlohmann::json::parse(headerBytes.begin(), headerBytes.end());
                jwt.claims = nlohmann::json::parse(claimsBytes.begin(), claimsBytes.end());
                jwt.signedData = std::string(token.substr(0, second));
                jwt.signature = std::move(signature);
                if (!jwt.header.is_object() || !jwt.claims.is_object())
                    return std::nullopt;
                return jwt;
            }
            catch (...)
            {
                return std::nullopt;
            }
        }

        inline bool SafeIdentifier(const std::string& value, const std::size_t maximum = 128U)
        {
            return !value.empty() && value.size() <= maximum &&
                std::all_of(value.begin(), value.end(), [](const unsigned char ch) {
                    return std::isalnum(ch) != 0 || ch == '_' || ch == '-' || ch == ':';
                });
        }

        inline bool SafeNativeConnectionNonce(const std::string_view value)
        {
            return value.size() >= 16U && value.size() <= 128U &&
                std::all_of(value.begin(), value.end(), [](const unsigned char ch) {
                    return std::isalnum(ch) != 0 || ch == '_' || ch == '-' ||
                        ch == ':';
                });
        }
    }

    class Policy
    {
    public:
        explicit Policy(SignatureVerifier verifier, const bool nativeAdmissionPathReady)
            : verifier_(std::move(verifier)), nativeAdmissionPathReady_(nativeAdmissionPathReady)
        {
        }

        ~Policy()
        {
            Reset();
        }

        Policy(const Policy&) = delete;
        Policy& operator=(const Policy&) = delete;

        void SetNativeAdmissionPathReady(const bool ready)
        {
            std::lock_guard lock(mutex_);
            nativeAdmissionPathReady_ = ready;
        }

        std::optional<AllocationScope> CurrentAllocationScope() const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return std::nullopt;
            return AllocationScope{
                allocation_->attemptId,
                allocation_->authoritySession,
                allocation_->rosterRevision,
                allocation_->routeGeneration};
        }

        Decision InstallAllocation(
            const std::string_view allocation,
            const std::string_view keyId,
            const std::string_view publicKeyBase64,
            const std::int64_t now)
        {
            std::lock_guard lock(mutex_);
            if (!verifier_)
                return Reject("signature_verifier_unavailable", "Ed25519 verifier is unavailable");
            std::vector<std::uint8_t> publicKey;
            if (!Detail::DecodeBase64(publicKeyBase64, publicKey, false) || publicKey.size() != 32U)
                return Reject("invalid_public_key", "admission public key is invalid");
            const auto jwt = Detail::ParseJwt(allocation);
            if (!jwt)
                return Reject("invalid_allocation", "allocation JWT is malformed");
			if (jwt->header.value("alg", "") != "EdDSA" ||
				jwt->header.value("typ", "") != "match-allocation+jwt" ||
                jwt->header.value("kid", "") != keyId)
            {
                return Reject("allocation_key_mismatch", "allocation key metadata does not match");
            }
            if (!verifier_(publicKey, jwt->signedData, jwt->signature))
                return Reject("invalid_allocation_signature", "allocation signature is invalid");
            const auto& claims = jwt->claims;
			if (claims.value("iss", "") != "game-control-plane" ||
				claims.value("aud", "") != "project-rebound-match-authority" ||
				claims.value("kid", "") != keyId ||
                claims.value("nbf", std::int64_t{0}) > now + 5 ||
                claims.value("exp", std::int64_t{0}) <= now)
            {
                return Reject("allocation_inactive", "allocation audience or time window is invalid");
            }
            Allocation next;
            next.keyId = std::string(keyId);
            next.publicKey = std::move(publicKey);
			next.tokenId = claims.value("jti", "");
            next.attemptId = claims.value("attempt_id", "");
            next.lobbyId = claims.value("lobby_id", "");
            next.hostingKind = claims.value("hosting_kind", "");
            next.authorityId = claims.value("authority_id", "");
            next.authoritySession = claims.value("authority_session_id", "");
            next.rosterRevision = claims.value("roster_revision", std::int64_t{0});
            next.routeGeneration = claims.value("route_generation", 0);
			next.connectionWindowSeconds =
				claims.value("initial_connection_window_seconds", 0);
            next.expiresAt = claims.value("exp", std::int64_t{0});
			if (!Detail::SafeIdentifier(next.tokenId, 96U) ||
				!Detail::SafeIdentifier(next.attemptId) || !Detail::SafeIdentifier(next.lobbyId) ||
                !Detail::SafeIdentifier(next.authorityId) ||
                !Detail::SafeIdentifier(next.authoritySession) ||
                next.rosterRevision < 1 || next.routeGeneration < 1 ||
				next.connectionWindowSeconds < 1 || next.connectionWindowSeconds > 600 ||
                (next.hostingKind != "DEDICATED" && next.hostingKind != "P2P"))
            {
                return Reject("invalid_allocation_claims", "allocation identity claims are invalid");
            }
            const auto roster = claims.find("roster");
            if (roster == claims.end() || !roster->is_array() ||
                roster->empty() || roster->size() > 64U)
            {
                return Reject("invalid_allocation_roster", "allocation roster is invalid");
            }
            std::unordered_set<int> logicalSlots;
            std::unordered_set<std::string> teamSlots;
            int hostCount = 0;
            for (const auto& item : *roster)
            {
                if (!item.is_object())
                    return Reject("invalid_allocation_roster", "roster member is invalid");
                Seat seat;
                seat.playerId = item.value("player_id", "");
                seat.platformId = item.value("platform_id", "");
                seat.roomRole = item.value("room_role", "");
                seat.teamId = item.value("team_id", 0);
                seat.teamSlot = item.value("team_slot", -1);
                seat.logicalSlot = item.value("logical_slot", -1);
                seat.generation = item.value("connection_generation", 0);
                const std::string slotKey = std::to_string(seat.teamId) + ":" +
                    std::to_string(seat.teamSlot);
                if (!Detail::SafeIdentifier(seat.playerId) ||
                    !Detail::SafeIdentifier(seat.platformId) ||
                    (seat.roomRole != "HOST" && seat.roomRole != "MEMBER") ||
                    (seat.teamId != 1 && seat.teamId != 2) || seat.teamSlot < 0 ||
                    seat.logicalSlot < 0 || seat.generation < 1 ||
                    !logicalSlots.insert(seat.logicalSlot).second ||
                    !teamSlots.insert(slotKey).second ||
                    !next.seats.emplace(seat.playerId, std::move(seat)).second)
                {
                    return Reject("invalid_allocation_roster", "roster seats are not unique and valid");
                }
                if (item.value("room_role", "") == "HOST")
                    ++hostCount;
            }
            if ((next.hostingKind == "P2P" && hostCount != 1) ||
                (next.hostingKind == "DEDICATED" && hostCount != 0))
            {
                return Reject("invalid_allocation_host", "allocation host binding is invalid");
            }
			if (allocation_ && allocation_->tokenId == next.tokenId)
				return Accept();
			// Once native authority is live, a signed allocation may only refresh
			// the route/generation of this same attempt/session/roster.  Do not
			// replace a live world with a different authority scope and rely on a
			// later pipe callback to discover the mismatch.
			if (authorityStarted_ && allocation_ &&
				(next.attemptId != allocation_->attemptId ||
				 next.lobbyId != allocation_->lobbyId ||
				 next.hostingKind != allocation_->hostingKind ||
				 next.authorityId != allocation_->authorityId ||
				 next.authoritySession != allocation_->authoritySession ||
				 next.rosterRevision != allocation_->rosterRevision))
			{
				return Reject("authority_scope_active",
					"a live native authority cannot adopt another allocation scope");
			}
			if (allocation_ && allocation_->attemptId == next.attemptId &&
				allocation_->lobbyId == next.lobbyId &&
				allocation_->hostingKind == next.hostingKind &&
				allocation_->authorityId == next.authorityId &&
				allocation_->authoritySession == next.authoritySession &&
				allocation_->rosterRevision == next.rosterRevision)
			{
				if (next.routeGeneration < allocation_->routeGeneration ||
					next.routeGeneration > allocation_->routeGeneration + 1 ||
					next.seats.size() != allocation_->seats.size())
				{
					return Reject("allocation_generation_conflict",
						"allocation route generation is stale or discontinuous");
				}
				for (const auto& [playerId, nextSeat] : next.seats)
				{
					const auto current = allocation_->seats.find(playerId);
					if (current == allocation_->seats.end() ||
						current->second.platformId != nextSeat.platformId ||
						current->second.roomRole != nextSeat.roomRole ||
						current->second.teamId != nextSeat.teamId ||
						current->second.teamSlot != nextSeat.teamSlot ||
						current->second.logicalSlot != nextSeat.logicalSlot ||
						nextSeat.generation < current->second.generation)
					{
						return Reject("allocation_roster_conflict",
							"allocation changed an immutable frozen seat");
					}
				}
				// The signed allocation is the authorization lower-bound
				// snapshot. Keep the live connection generation separate: a
				// route/lease refresh must reject old grants immediately without
				// renaming an existing socket as the new generation.
				const bool routeAdvanced = next.routeGeneration > allocation_->routeGeneration;
				std::fill(allocation_->publicKey.begin(), allocation_->publicKey.end(),
					static_cast<std::uint8_t>(0));
				allocation_->keyId = std::move(next.keyId);
				allocation_->publicKey = std::move(next.publicKey);
				allocation_->tokenId = std::move(next.tokenId);
				allocation_->routeGeneration = next.routeGeneration;
				allocation_->connectionWindowSeconds = next.connectionWindowSeconds;
				allocation_->expiresAt = next.expiresAt;
				for (auto& [playerId, seat] : allocation_->seats)
				{
					const auto updated = next.seats.find(playerId);
					if (updated == next.seats.end())
						continue;
					seat.generation = updated->second.generation;
					// A new signed route invalidates an unconsumed reservation;
					// callers must obtain a grant for the new lower bound.
					seat.reserved = false;
					seat.reservedGeneration = 0;
					seat.reservedJti.clear();
					seat.reservedNativeConnectionNonce.clear();
					seat.reservedWorldInstanceId.clear();
					seat.reservedRouteGeneration = 0;
					seat.backendReserved = false;
					seat.nativeAdmitted = false;
					seat.nativeAdmissionGeneration = 0;
					seat.nativeAdmissionWorldInstanceId.clear();
					seat.nativeAdmissionRouteGeneration = 0;
					seat.nativeAdmissionNonce.clear();
					seat.nativeAdmissionJti.clear();
					seat.nativeAdmissionNonce.clear();
				}
				stagedGrants_.clear();
				(void)routeAdvanced;
				return Accept();
			}
			ResetLocked();
			allocation_ = std::move(next);
            return Accept();
        }

        Decision StartAuthority(const std::string_view localPlatformId, const std::int64_t now)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_ || allocation_->expiresAt <= now)
                return Reject("allocation_unavailable", "a live allocation is not installed");
            if (!nativeAdmissionPathReady_)
                return Reject("native_admission_unverified", "pinned PreLogin and team paths are not verified");
            if (allocation_->hostingKind == "P2P")
            {
                const auto host = std::find_if(
                    allocation_->seats.begin(), allocation_->seats.end(),
                    [localPlatformId](const auto& entry) {
                        return entry.second.roomRole == "HOST" &&
                            entry.second.platformId == localPlatformId;
                    });
                if (host == allocation_->seats.end())
                    return Reject("host_identity_mismatch", "local host is not the allocated host seat");
            }
            authorityStarted_ = true;
            return Accept();
        }

        // P2P authority startup is bound to the exact local platform identity
        // that owns the signed HOST seat. An empty or UI-only placeholder is
        // never accepted as a host binding.
        SeatDecision StartAuthorityForAllocatedHost(
            const std::string_view localPlatformId,
            const std::int64_t now,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_ || allocation_->expiresAt <= now)
                return RejectSeat("allocation_unavailable", "a live allocation is not installed");
            if (!nativeAdmissionPathReady_)
                return RejectSeat("native_admission_unverified", "pinned PreLogin and team paths are not verified");
            if (allocation_->hostingKind != "P2P")
                return RejectSeat("host_binding_not_applicable", "only a P2P allocation has a local host seat");
            if (localPlatformId.empty())
                return RejectSeat("host_identity_unavailable", "the native local host platform identity is required");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return RejectSeat("native_connection_nonce_required", "a fresh native host handshake nonce is required");
            const auto host = std::find_if(
                allocation_->seats.begin(), allocation_->seats.end(),
                [localPlatformId](const auto& entry) {
                    return entry.second.roomRole == "HOST" &&
                        entry.second.platformId == localPlatformId;
                });
            if (host == allocation_->seats.end())
                return RejectSeat("host_identity_mismatch", "local host identity is not the allocated HOST seat");
            // A repeated authority-start request is an observation of the
            // already-running native world, not a second handshake. Reuse the
            // existing live nonce for that idempotent report.
            if (authorityStarted_ && host->second.connected &&
                host->second.liveGeneration == host->second.generation)
            {
                return BuildConfirmedDecisionLocked(host->second);
            }
            if (host->second.connected)
            {
                // A route refresh advances the authorization lower bound but
                // cannot silently promote the previous native HOST socket to
                // the new generation.  The old live connection must first
                // emit its native DISCONNECTED event (with its old nonce).
                return RejectSeat("host_live_connection_pending_disconnect",
                    "the previous native HOST generation must disconnect before recovery");
            }
            if (host->second.reserved &&
                host->second.reservedGeneration == host->second.generation)
            {
                if (host->second.reservedNativeConnectionNonce == nativeConnectionNonce)
                    return BuildReservedDecisionLocked(host->second);
                return RejectSeat("native_connection_nonce_conflict",
                    "the host seat is already reserved by another native handshake");
            }
            if (host->second.connected &&
                host->second.liveGeneration == host->second.generation)
            {
                return RejectSeat("seat_already_connected", "the allocated host seat is still connected");
            }
            host->second.reserved = true;
            host->second.reservedGeneration = host->second.generation;
            host->second.reservedJti.clear();
            host->second.reservedNativeConnectionNonce = std::string(nativeConnectionNonce);
            host->second.backendReserved = true;
            authorityStarted_ = true;
            SeatDecision decision;
            decision.accepted = true;
            decision.code = "accepted";
            decision.playerId = host->second.playerId;
            decision.platformId = host->second.platformId;
            decision.nativeConnectionNonce = host->second.reservedNativeConnectionNonce;
            decision.teamId = host->second.teamId;
            decision.teamSlot = host->second.teamSlot;
            decision.logicalSlot = host->second.logicalSlot;
            decision.connectionGeneration = host->second.generation;
            decision.reserved = true;
            decision.hostSeat = true;
            decision.backendReserved = true;
            return decision;
        }

        // Preserve a real local HOST connection when only the authority's
        // admission route advances. This never changes its live scope or
        // nonce, reserves a seat, or emits another CONNECTED event.
        SeatDecision ValidatePreservedHost(
            const std::string_view playerId,
            const std::string_view localPlatformId,
            const std::string_view worldInstanceId,
            const int liveRouteGeneration,
            const int liveConnectionGeneration,
            const std::string_view nativeConnectionNonce,
            const std::int64_t now) const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_ || allocation_->hostingKind != "P2P" ||
                !authorityStarted_ || !nativeAdmissionPathReady_ || allocation_->expiresAt <= now)
                return RejectSeat("admission_closed", "the signed P2P authority is not active");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return RejectSeat("host_identity_mismatch", "the preserved HOST is not allocated");
            const Seat& host = found->second;
            if (host.roomRole != "HOST" || host.platformId != localPlatformId ||
                !host.connected || host.generation != liveConnectionGeneration ||
                host.liveGeneration != liveConnectionGeneration ||
                host.liveRouteGeneration != liveRouteGeneration ||
                liveRouteGeneration > allocation_->routeGeneration ||
                host.liveWorldInstanceId != worldInstanceId ||
                nativeWorldInstanceId_ != worldInstanceId ||
                host.liveNativeConnectionNonce != nativeConnectionNonce ||
                !Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return RejectSeat("host_connection_scope_mismatch", "the preserved HOST live connection changed");
            return BuildConfirmedDecisionLocked(host);
        }

        std::optional<std::string> CurrentHostingKind() const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return std::nullopt;
            return allocation_->hostingKind;
        }

        // The native world generation is supplied by the pinned world
        // observer. Events retain this value at append time so a later world
        // cannot relabel history that belongs to an older NetDriver.
        void SetNativeWorldInstanceId(const std::string_view worldInstanceId)
        {
            std::lock_guard lock(mutex_);
            nativeWorldInstanceId_ = std::string(worldInstanceId);
        }

        Decision StageJoinGrant(const std::string_view grant, const std::int64_t now)
        {
            std::lock_guard lock(mutex_);
            if (!nativeAdmissionPathReady_ || !authorityStarted_ || !allocation_)
                return Reject("admission_closed", "strict admission is not active");
            if (allocation_->expiresAt <= now)
                return Reject("allocation_expired", "strict admission allocation is expired");
            const auto jwt = Detail::ParseJwt(grant);
            if (!jwt || jwt->header.value("alg", "") != "EdDSA" ||
                jwt->header.value("typ", "") != "match-join+jwt" ||
                jwt->header.value("kid", "") != allocation_->keyId ||
                !verifier_(allocation_->publicKey, jwt->signedData, jwt->signature))
            {
                return Reject("invalid_grant_signature", "join grant signature is invalid");
            }
            const auto& claims = jwt->claims;
            PendingGrant pending;
            pending.playerId = claims.value("player_id", "");
            pending.platformId = claims.value("platform_id", "");
            pending.jti = claims.value("jti", "");
            pending.teamId = claims.value("team_id", 0);
            pending.teamSlot = claims.value("team_slot", -1);
            pending.logicalSlot = claims.value("logical_slot", -1);
            pending.generation = claims.value("connection_generation", 0);
            pending.expiresAt = claims.value("exp", std::int64_t{0});
            const auto worldInstanceClaim = claims.find("world_instance_id");
            if (worldInstanceClaim == claims.end() ||
                !worldInstanceClaim->is_string())
            {
                return Reject("grant_world_mismatch",
                    "join grant is missing its signed native world instance");
            }
            pending.worldInstanceId = worldInstanceClaim->get<std::string>();
            if (claims.value("iss", "") != "game-control-plane" ||
                claims.value("aud", "") != "project-rebound-match-client" ||
                claims.value("kid", "") != allocation_->keyId ||
                claims.value("attempt_id", "") != allocation_->attemptId ||
                claims.value("lobby_id", "") != allocation_->lobbyId ||
                claims.value("authority_id", "") != allocation_->authorityId ||
                claims.value("authority_session_id", "") != allocation_->authoritySession ||
                claims.value("hosting_kind", "") != allocation_->hostingKind ||
                claims.value("roster_revision", std::int64_t{0}) != allocation_->rosterRevision ||
                claims.value("route_generation", 0) != allocation_->routeGeneration ||
                claims.value("nbf", std::int64_t{0}) > now + 5 ||
                pending.expiresAt <= now ||
                !Detail::SafeIdentifier(pending.jti, 96U) ||
                !Detail::SafeIdentifier(pending.playerId) ||
                !Detail::SafeIdentifier(pending.platformId) ||
                !Detail::SafeIdentifier(pending.worldInstanceId))
            {
                return Reject("grant_claim_mismatch", "join grant claims do not match this authority");
            }
            if (nativeWorldInstanceId_.empty() ||
                pending.worldInstanceId != nativeWorldInstanceId_)
            {
                return Reject("grant_world_mismatch",
                    "join grant belongs to a different native authority world");
            }
            const auto seatIt = allocation_->seats.find(pending.playerId);
            if (seatIt == allocation_->seats.end())
                return Reject("player_not_rostered", "player is not in the frozen roster");
            const Seat& seat = seatIt->second;
            if (allocation_->hostingKind == "P2P" && seat.roomRole == "HOST")
                return Reject("host_uses_allocation", "the local P2P host cannot use a remote join grant");
            if (pending.platformId != seat.platformId || pending.teamId != seat.teamId ||
                pending.teamSlot != seat.teamSlot || pending.logicalSlot != seat.logicalSlot ||
                pending.generation < seat.generation)
            {
                return Reject("seat_claim_mismatch", "join grant does not own the frozen seat");
            }
            if (usedJtis_.contains(pending.jti))
                return Reject("grant_replayed", "join grant was already consumed");
            const auto staged = stagedGrants_.find(pending.platformId);
            if (staged != stagedGrants_.end())
            {
                if (staged->second.jti == pending.jti)
                    return Accept();
                if (pending.generation <= staged->second.generation)
                    return Reject("grant_superseded", "a newer join grant is already staged for this seat");
            }
            stagedGrants_[pending.platformId] = std::move(pending);
            return Accept();
        }

        // Verify the exact signed grant that arrived in NMT_Login against the
        // grant staged by the scoped command channel.  This is deliberately a
        // non-consuming check: reliable native retransmits must be able to
        // present the same grant until ReserveAdmission linearizes the
        // handshake.  The platform identity is still a separate native
        // possession proof at the hook boundary.
        // Validate the signed NMT grant against the native account identity
        // carried by FUniqueNetId.  That value is the backend Player.ID (the
        // roster's player_id), not the Steam platform_id.  The signed grant
        // is verified before its platform_id is used, and the returned seat
        // decision carries both identities for the subsequent Steam proof
        // and native reservation steps.
        SeatDecision ValidateNativeJoinGrantForPlayer(
            const std::string_view grant,
            const std::string_view nativePlayerId,
            const std::int64_t now)
        {
            std::lock_guard lock(mutex_);
            if (!nativeAdmissionPathReady_ || !authorityStarted_ || !allocation_)
                return RejectSeat("admission_closed", "strict admission is not active");
            if (allocation_->expiresAt <= now)
                return RejectSeat("allocation_expired", "a live allocation is expired");
            if (!Detail::SafeIdentifier(std::string(nativePlayerId)))
                return RejectSeat("native_identity_mismatch", "the native account identity is malformed");

            try
            {
                const auto jwt = Detail::ParseJwt(grant);
                if (!jwt || jwt->header.value("alg", "") != "EdDSA" ||
                    jwt->header.value("typ", "") != "match-join+jwt" ||
                    jwt->header.value("kid", "") != allocation_->keyId ||
                    !verifier_(allocation_->publicKey, jwt->signedData, jwt->signature))
                {
                    return RejectSeat("native_grant_invalid", "the NMT_Login grant signature is invalid");
                }
                const auto& claims = jwt->claims;
                const std::string playerId = claims.value("player_id", "");
                const std::string platformId = claims.value("platform_id", "");
                const std::string jti = claims.value("jti", "");
                const int teamId = claims.value("team_id", 0);
                const int teamSlot = claims.value("team_slot", -1);
                const int logicalSlot = claims.value("logical_slot", -1);
                const int generation = claims.value("connection_generation", 0);
                const std::int64_t expiresAt = claims.value("exp", std::int64_t{0});
                const auto worldInstanceClaim = claims.find("world_instance_id");
                if (worldInstanceClaim == claims.end() || !worldInstanceClaim->is_string())
                {
                    return RejectSeat("native_grant_mismatch",
                        "the NMT_Login grant has no signed native world instance");
                }
                const std::string worldInstanceId = worldInstanceClaim->get<std::string>();
                if (nativeWorldInstanceId_.empty() || worldInstanceId != nativeWorldInstanceId_)
                    return RejectSeat("grant_world_mismatch", "the signed grant belongs to another native world");
                if (claims.value("iss", "") != "game-control-plane" ||
                    claims.value("aud", "") != "project-rebound-match-client" ||
                    claims.value("kid", "") != allocation_->keyId ||
                    claims.value("attempt_id", "") != allocation_->attemptId ||
                    claims.value("lobby_id", "") != allocation_->lobbyId ||
                    claims.value("authority_id", "") != allocation_->authorityId ||
                    claims.value("authority_session_id", "") != allocation_->authoritySession ||
                    claims.value("hosting_kind", "") != allocation_->hostingKind ||
                    claims.value("roster_revision", std::int64_t{0}) != allocation_->rosterRevision ||
                    claims.value("route_generation", 0) != allocation_->routeGeneration ||
                    claims.value("nbf", std::int64_t{0}) > now + 5 ||
                    expiresAt <= now ||
                    !Detail::SafeIdentifier(jti, 96U) ||
                    !Detail::SafeIdentifier(playerId) ||
                    !Detail::SafeIdentifier(platformId) ||
                    !Detail::SafeIdentifier(worldInstanceId) ||
                    playerId != nativePlayerId ||
                    nativeWorldInstanceId_.empty() ||
                    worldInstanceId != nativeWorldInstanceId_ ||
                    usedJtis_.contains(jti))
                {
                    return RejectSeat("native_identity_mismatch",
                        "the signed grant player identity is not the native account");
                }

                const auto staged = stagedGrants_.find(platformId);
                if (staged == stagedGrants_.end())
                    return RejectSeat("grant_not_staged", "no staged grant matches the signed platform seat");
                const PendingGrant& pending = staged->second;
                const auto seatIt = allocation_->seats.find(playerId);
                if (seatIt == allocation_->seats.end())
                    return RejectSeat("player_not_rostered", "player is not in the frozen roster");
                const Seat& seat = seatIt->second;
                if (pending.expiresAt <= now ||
                    pending.playerId != playerId || pending.platformId != platformId ||
                    pending.jti != jti || pending.teamId != teamId ||
                    pending.teamSlot != teamSlot || pending.logicalSlot != logicalSlot ||
                    pending.generation != generation || pending.expiresAt != expiresAt ||
                    pending.worldInstanceId != worldInstanceId ||
                    seat.platformId != platformId || seat.teamId != teamId ||
                    seat.teamSlot != teamSlot || seat.logicalSlot != logicalSlot ||
                    generation < seat.generation)
                {
                    return RejectSeat("native_grant_mismatch",
                        "the signed grant is not the staged frozen seat");
                }

                SeatDecision result;
                result.accepted = true;
                result.code = "accepted";
                result.playerId = playerId;
                result.platformId = platformId;
                result.grantJti = jti;
                result.teamId = teamId;
                result.teamSlot = teamSlot;
                result.logicalSlot = logicalSlot;
                result.connectionGeneration = generation;
                result.hostSeat = seat.roomRole == "HOST";
                return result;
            }
            catch (...)
            {
                return RejectSeat("native_grant_invalid", "the NMT_Login grant claims are invalid");
            }
        }

        // The native account identity and Steam platform identity are both
        // required at the reservation linearization point.  This prevents a
        // second staged seat with the same Steam identity, or an identity
        // supplied by another connection, from consuming the validated
        // reservation between PreLogin checks and ReserveAdmission.
        SeatDecision ReserveAdmissionForNativePlayer(
            const std::string_view nativePlayerId,
            const std::string_view authenticatedPlatformId,
            const std::string_view expectedGrantJti,
            const std::int64_t now,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!Detail::SafeIdentifier(std::string(nativePlayerId)))
                return RejectSeat("native_identity_mismatch", "the native account identity is malformed");
            if (!nativeAdmissionPathReady_ || !authorityStarted_ || !allocation_)
                return RejectSeat("admission_closed", "strict admission is not active");
            if (allocation_->expiresAt <= now)
                return RejectSeat("allocation_expired", "strict admission allocation is expired");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return RejectSeat("native_connection_nonce_required", "a fresh native handshake nonce is required");
            if (nativeWorldInstanceId_.empty())
                return RejectSeat("native_world_unavailable", "the native authority world is not bound yet");
            const auto staged = stagedGrants_.find(std::string(authenticatedPlatformId));
            if (staged == stagedGrants_.end())
                return RejectSeat("grant_not_staged", "no staged grant matches the signed platform seat");
            const PendingGrant pending = staged->second;
            if (pending.playerId != nativePlayerId)
                return RejectSeat("native_identity_mismatch", "the staged seat is not the native account");
            if (pending.jti != expectedGrantJti || !Detail::SafeIdentifier(pending.jti, 96U))
                return RejectSeat("native_grant_changed", "the staged grant changed during platform authentication");
            if (pending.expiresAt <= now)
            {
                stagedGrants_.erase(staged);
                return RejectSeat("grant_expired", "the staged join grant has expired");
            }
            if (!Detail::SafeIdentifier(pending.worldInstanceId) ||
                pending.worldInstanceId != nativeWorldInstanceId_)
            {
                return RejectSeat("grant_world_mismatch",
                    "the staged grant belongs to a different native authority world");
            }
            const auto seatIt = allocation_->seats.find(pending.playerId);
            if (seatIt == allocation_->seats.end() ||
                seatIt->second.platformId != authenticatedPlatformId)
            {
                return RejectSeat("player_not_rostered", "the signed seat is not in the frozen roster");
            }
            Seat& seat = seatIt->second;
            if (pending.generation < seat.generation)
                return RejectSeat("grant_superseded", "the signed seat authorization advanced during platform authentication");
            if (usedJtis_.contains(pending.jti))
                return RejectSeat("grant_replayed", "join grant was already consumed");
            if (seat.connected && pending.generation <= seat.liveGeneration)
                return RejectSeat("seat_already_connected", "seat already has a live connection");
            if (seat.reserved)
            {
                if (seat.reservedJti == pending.jti &&
                    seat.reservedGeneration == pending.generation &&
                    seat.reservedNativeConnectionNonce == nativeConnectionNonce)
                {
                    return BuildReservedDecisionLocked(seat);
                }
                if (seat.reservedJti == pending.jti &&
                    seat.reservedGeneration == pending.generation)
                {
                    return RejectSeat("native_connection_nonce_conflict",
                        "the grant and generation are already reserved by another native handshake");
                }
                if (pending.generation <= seat.reservedGeneration)
                    return RejectSeat("grant_superseded", "a newer join grant is already reserved for this seat");
            }
            SeatDecision decision;
            decision.accepted = true;
            decision.code = "accepted";
            decision.playerId = pending.playerId;
            decision.platformId = pending.platformId;
            decision.grantJti = pending.jti;
            decision.nativeConnectionNonce = std::string(nativeConnectionNonce);
            decision.teamId = pending.teamId;
            decision.teamSlot = pending.teamSlot;
            decision.logicalSlot = pending.logicalSlot;
            decision.connectionGeneration = pending.generation;
            decision.replacesConnection = seat.connected && pending.generation > seat.liveGeneration;
            decision.reserved = true;
            decision.confirmed = false;
            decision.hostSeat = seat.roomRole == "HOST";
            decision.backendReserved = seat.backendReserved;
            seat.reserved = true;
            seat.reservedGeneration = pending.generation;
            seat.reservedJti = pending.jti;
            seat.reservedNativeConnectionNonce = std::string(nativeConnectionNonce);
            seat.reservedWorldInstanceId = nativeWorldInstanceId_;
            seat.reservedRouteGeneration = allocation_->routeGeneration;
            // grantJti belongs to the live connection. A replacement
            // reservation must not relabel that still-live socket.
            seat.nativeAdmitted = false;
            seat.nativeAdmissionGeneration = 0;
            seat.nativeAdmissionJti.clear();
            seat.backendReserved = false;
            stagedGrants_.erase(staged);
            AppendConnectionEventLocked(seat, "RESERVED", pending.jti, pending.generation);
            return decision;
        }

        std::optional<SeatDecision> ActiveDecision(const std::string_view platformId) const
        {
            std::lock_guard lock(mutex_);
            const auto found = activeDecisions_.find(std::string(platformId));
            return found == activeDecisions_.end()
                ? std::nullopt : std::optional<SeatDecision>(found->second);
        }

        std::optional<SeatDecision> ReservedDecision(const std::string_view platformId) const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return std::nullopt;
            const auto seat = std::find_if(
                allocation_->seats.begin(), allocation_->seats.end(),
                [platformId](const auto& entry)
                {
                    return entry.second.platformId == platformId &&
                        entry.second.reserved;
                });
            return seat == allocation_->seats.end()
                ? std::nullopt
                : std::optional<SeatDecision>(BuildReservedDecisionLocked(seat->second));
        }

        std::optional<SeatDecision> ReservedDecisionByPlayerId(
            const std::string_view playerId) const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return std::nullopt;
            const auto seat = allocation_->seats.find(std::string(playerId));
            if (seat == allocation_->seats.end() || !seat->second.reserved)
                return std::nullopt;
            return std::optional<SeatDecision>(BuildReservedDecisionLocked(seat->second));
        }

        // Toolbox calls this after the scoped backend ReserveAdmission
        // transaction succeeds. It is intentionally separate from
        // ConfirmConnected: the roster remains non-CONNECTED until the
        // backend consumes the same grant after native PostLogin.
        Decision ConfirmAdmissionReserved(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_required", "the backend reservation must carry the native handshake nonce");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return Reject("player_not_rostered", "player is not in the frozen roster");
            Seat& seat = found->second;
            if (seat.connected && seat.liveGeneration == generation &&
                (grantJti.empty() || seat.grantJti == grantJti) &&
                seat.liveNativeConnectionNonce == nativeConnectionNonce)
            {
                return !nativeWorldInstanceId_.empty() &&
                    seat.liveWorldInstanceId == nativeWorldInstanceId_
                    ? Accept()
                    : Reject("native_world_scope_mismatch", "the live connection belongs to another native world");
            }
            if (!seat.reserved || seat.reservedGeneration != generation ||
                (seat.roomRole != "HOST" && seat.reservedJti != grantJti) ||
                seat.reservedNativeConnectionNonce != nativeConnectionNonce)
            {
                return Reject("admission_reservation_mismatch",
                    "backend reservation does not match the native admission");
            }
            seat.backendReserved = true;
            return Accept();
        }

        Decision ValidateConnectionScope(
            const std::string_view attemptId,
            const std::string_view authoritySession,
            const std::int64_t rosterRevision,
            const int routeGeneration,
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce) const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_required", "the scoped receipt must carry a native handshake nonce");
            if (allocation_->attemptId != attemptId ||
                allocation_->authoritySession != authoritySession ||
                allocation_->rosterRevision != rosterRevision ||
                allocation_->routeGeneration != routeGeneration)
            {
                return Reject("admission_scope_mismatch",
                    "connection receipt belongs to another attempt, route, or roster");
            }
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return Reject("player_not_rostered", "player is not in the frozen roster");
            const Seat& seat = found->second;
            // Allocation refreshes retain the old live fields so a HOST can
            // be preserved, but a backend receipt must still bind to the
            // exact native world and route that owns this handshake.
            const bool currentWorldBound = !nativeWorldInstanceId_.empty();
            const bool reservedMatch = seat.reserved &&
                (seat.roomRole == "HOST" ? grantJti.empty() :
                    !grantJti.empty() && seat.reservedJti == grantJti) &&
                seat.reservedGeneration == generation &&
                seat.reservedRouteGeneration == routeGeneration &&
                currentWorldBound &&
                seat.reservedWorldInstanceId == nativeWorldInstanceId_ &&
                seat.reservedNativeConnectionNonce == nativeConnectionNonce;
            const bool connectedMatch = seat.connected &&
                (seat.roomRole == "HOST" ? grantJti.empty() :
                    !grantJti.empty() && seat.grantJti == grantJti) &&
                seat.liveGeneration == generation &&
                seat.liveRouteGeneration == routeGeneration &&
                currentWorldBound &&
                seat.liveWorldInstanceId == nativeWorldInstanceId_ &&
                seat.liveNativeConnectionNonce == nativeConnectionNonce;
            if (!reservedMatch && !connectedMatch)
            {
                if (seat.lastDisconnectedGeneration == generation &&
                    (seat.roomRole == "HOST" ? grantJti.empty() :
                        !grantJti.empty() && seat.lastDisconnectedGrantJti == grantJti) &&
                    seat.lastDisconnectedRouteGeneration == routeGeneration &&
                    currentWorldBound &&
                    seat.lastDisconnectedWorldInstanceId == nativeWorldInstanceId_ &&
                    seat.lastDisconnectedNativeConnectionNonce == nativeConnectionNonce)
                    return Accept();
                return Reject("connection_generation_stale",
                    "connection receipt belongs to a stale generation");
            }
            if (seat.roomRole != "HOST" && !grantJti.empty() &&
                seat.grantJti != grantJti && seat.reservedJti != grantJti)
            {
                return Reject("grant_scope_mismatch",
                    "connection receipt does not match the staged grant");
            }
            return Accept();
        }

        // Native team/camp readback is an intermediate observation. It does
        // not consume the Grant or mark the roster CONNECTED. The backend
        // receipt must later call ConfirmConnected.
        Decision MarkNativeAdmitted(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_required", "native admission requires the concrete handshake nonce");
            if (nativeWorldInstanceId_.empty())
                return Reject("native_world_unavailable", "native admission requires a current authority world");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return Reject("connection_generation_stale",
                    "native admission generation is unknown");
            Seat& seat = found->second;
            if (seat.connected && seat.liveGeneration == generation)
            {
                if (seat.liveNativeConnectionNonce != nativeConnectionNonce)
                    return Reject("native_connection_nonce_conflict", "the live connection belongs to another native handshake");
                return seat.liveWorldInstanceId == nativeWorldInstanceId_
                    ? Accept()
                    : Reject("native_world_scope_mismatch", "the live connection belongs to another native world");
            }
            if (seat.nativeAdmitted &&
                seat.nativeAdmissionGeneration == generation &&
                (seat.roomRole == "HOST" || seat.nativeAdmissionJti == grantJti) &&
                seat.nativeAdmissionNonce == nativeConnectionNonce)
            {
                if (seat.nativeAdmissionWorldInstanceId == nativeWorldInstanceId_ &&
                    seat.nativeAdmissionRouteGeneration == allocation_->routeGeneration)
                    return Accept();
                return Reject("native_world_scope_mismatch", "native admission belongs to another world or route");
            }
            if (!seat.reserved || seat.reservedGeneration != generation ||
                (seat.roomRole != "HOST" && seat.reservedJti != grantJti) ||
                seat.reservedNativeConnectionNonce != nativeConnectionNonce)
            {
                return Reject("connection_generation_stale",
                    "native admission has no matching reservation");
            }
            if (seat.reservedWorldInstanceId.empty())
            {
                seat.reservedWorldInstanceId = nativeWorldInstanceId_;
                seat.reservedRouteGeneration = allocation_->routeGeneration;
            }
            else if (seat.reservedWorldInstanceId != nativeWorldInstanceId_ ||
                seat.reservedRouteGeneration != allocation_->routeGeneration)
            {
                return Reject("native_world_scope_mismatch", "the reserved native handshake belongs to another world or route");
            }
            seat.nativeAdmitted = true;
            seat.nativeAdmissionGeneration = generation;
            seat.nativeAdmissionJti = std::string(grantJti);
            seat.nativeAdmissionNonce = std::string(nativeConnectionNonce);
            seat.nativeAdmissionWorldInstanceId = seat.reservedWorldInstanceId;
            seat.nativeAdmissionRouteGeneration = seat.reservedRouteGeneration;
            AppendConnectionEventLocked(
                seat, "NATIVE_ADMITTED", grantJti, generation,
                nativeConnectionNonce);
            return Accept();
        }

        bool AdmissionActive() const
        {
            std::lock_guard lock(mutex_);
            return nativeAdmissionPathReady_ && authorityStarted_ && allocation_.has_value();
        }

        bool IsConnected(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti = {}) const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return false;
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return false;
            const Seat& seat = found->second;
            return seat.connected && seat.liveGeneration == generation &&
                (grantJti.empty() || seat.grantJti == grantJti);
        }

        Decision ConfirmConnected(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_required", "native connection confirmation requires the concrete handshake nonce");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
            {
                return Reject("connection_generation_stale",
                    "connected generation is unknown");
            }
            Seat& seat = found->second;
            if (seat.connected && seat.liveGeneration == generation &&
                (grantJti.empty() || seat.grantJti == grantJti) &&
                seat.liveNativeConnectionNonce == nativeConnectionNonce)
            {
                const bool currentLiveWorld = !nativeWorldInstanceId_.empty() &&
                    seat.liveWorldInstanceId == nativeWorldInstanceId_;
                const bool currentLiveRoute = seat.roomRole == "HOST"
                    ? seat.liveRouteGeneration > 0 && seat.liveRouteGeneration <= allocation_->routeGeneration &&
                        seat.generation == generation
                    : seat.liveRouteGeneration == allocation_->routeGeneration;
                return currentLiveWorld && currentLiveRoute
                    ? Accept()
                    : Reject("native_world_scope_mismatch", "the live connection belongs to another native world or authorization route");
            }
            if (!seat.reserved || seat.reservedGeneration != generation ||
                (seat.roomRole != "HOST" &&
                 (!grantJti.empty() && seat.reservedJti != grantJti)) ||
                seat.reservedNativeConnectionNonce != nativeConnectionNonce)
            {
                return Reject("connection_generation_stale",
                    "native connection has no matching admission reservation");
            }
            if (!seat.nativeAdmitted ||
                 seat.nativeAdmissionGeneration != generation ||
                 (!grantJti.empty() && seat.nativeAdmissionJti != grantJti) ||
                 seat.nativeAdmissionNonce != nativeConnectionNonce ||
                 !seat.backendReserved)
            {
                return Reject("backend_confirmation_required",
                    "native admission is waiting for the scoped backend reservation and confirmation");
            }
            const std::string connectionWorldInstanceId =
                !seat.nativeAdmissionWorldInstanceId.empty()
                    ? seat.nativeAdmissionWorldInstanceId
                    : seat.reservedWorldInstanceId;
            const int connectionRouteGeneration =
                seat.nativeAdmissionRouteGeneration != 0
                    ? seat.nativeAdmissionRouteGeneration
                    : seat.reservedRouteGeneration;
            if (connectionWorldInstanceId.empty() || connectionRouteGeneration < 1)
                return Reject("native_world_unavailable", "the native connection world scope is unavailable");
            if (nativeWorldInstanceId_.empty() ||
                connectionWorldInstanceId != nativeWorldInstanceId_ ||
                connectionRouteGeneration != allocation_->routeGeneration)
                return Reject("native_world_scope_mismatch", "the native connection belongs to another world or route");
            if (seat.roomRole != "HOST" &&
                !usedJtis_.insert(seat.reservedJti).second)
            {
                return Reject("grant_replayed", "join grant was already consumed");
            }
            seat.connected = true;
            seat.liveGeneration = generation;
            seat.grantJti = seat.reservedJti;
            seat.liveNativeConnectionNonce = seat.reservedNativeConnectionNonce;
            seat.liveWorldInstanceId = connectionWorldInstanceId;
            seat.liveRouteGeneration = connectionRouteGeneration;
            seat.reserved = false;
            seat.reservedGeneration = 0;
            seat.reservedJti.clear();
            seat.reservedNativeConnectionNonce.clear();
            seat.reservedWorldInstanceId.clear();
            seat.reservedRouteGeneration = 0;
            seat.nativeAdmitted = false;
            seat.nativeAdmissionGeneration = 0;
            seat.nativeAdmissionJti.clear();
            seat.nativeAdmissionNonce.clear();
            seat.nativeAdmissionWorldInstanceId.clear();
            seat.nativeAdmissionRouteGeneration = 0;
            seat.backendReserved = false;
            seat.reportObserved = true;
            seat.reportedConnected = true;
            activeDecisions_[seat.platformId] = BuildConfirmedDecisionLocked(seat);
            AppendConnectionEventLocked(
                seat, "CONNECTED", seat.grantJti, generation,
                seat.liveNativeConnectionNonce);
            return Accept();
        }

        Decision MarkConnected(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce)
        {
            return ConfirmConnected(
                playerId, generation, grantJti, nativeConnectionNonce);
        }

        Decision MarkDisconnected(
            const std::string_view playerId,
            const int generation,
            const std::string_view nativeConnectionNonce = {})
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!nativeConnectionNonce.empty() &&
                !Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_invalid", "disconnect nonce is invalid");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return Reject("connection_generation_stale", "disconnect generation is stale");
            Seat& seat = found->second;
            if (!seat.connected || seat.liveGeneration != generation)
            {
                if (seat.reserved && seat.reservedGeneration == generation)
                {
                    if (!nativeConnectionNonce.empty() &&
                        seat.reservedNativeConnectionNonce != nativeConnectionNonce)
                    {
                        return Reject("native_connection_nonce_conflict",
                            "disconnect does not belong to the reserved native handshake");
                    }
                    seat.lastDisconnectedGeneration = generation;
                    seat.lastDisconnectedGrantJti = seat.reservedJti;
                    seat.lastDisconnectedNativeConnectionNonce =
                        seat.reservedNativeConnectionNonce;
                    seat.lastDisconnectedWorldInstanceId =
                        seat.reservedWorldInstanceId;
                    seat.lastDisconnectedRouteGeneration =
                        seat.reservedRouteGeneration;
                    seat.reserved = false;
                    seat.reservedGeneration = 0;
                    seat.reservedJti.clear();
                    seat.reservedNativeConnectionNonce.clear();
                    seat.reservedWorldInstanceId.clear();
                    seat.reservedRouteGeneration = 0;
                    seat.backendReserved = false;
                    seat.nativeAdmitted = false;
                    seat.nativeAdmissionGeneration = 0;
                    seat.nativeAdmissionJti.clear();
                    seat.nativeAdmissionNonce.clear();
                    return Accept();
                }
                return seat.lastDisconnectedGeneration == generation
                    && (nativeConnectionNonce.empty() ||
                        seat.lastDisconnectedNativeConnectionNonce == nativeConnectionNonce)
                    ? Accept()
                    : Reject("connection_generation_stale", "disconnect generation is stale");
            }
            if (!nativeConnectionNonce.empty() &&
                seat.liveNativeConnectionNonce != nativeConnectionNonce)
            {
                return Reject("native_connection_nonce_conflict",
                    "disconnect does not belong to the live native handshake");
            }
            // A newer reservation may already have passed backend Reserve and
            // native readback while the previous live generation is winding
            // down.  The disconnect owns only the live generation's cleanup;
            // it must not clear nativeAdmitted/backendReserved for that newer
            // reservation.
            const std::string disconnectedNonce = nativeConnectionNonce.empty()
                ? seat.liveNativeConnectionNonce
                : std::string(nativeConnectionNonce);
            const bool pendingScopeIsLiveScope = !seat.reserved ||
                (seat.reservedGeneration == generation &&
                 seat.reservedNativeConnectionNonce == disconnectedNonce);
            seat.connected = false;
            seat.lastDisconnectedGeneration = generation;
            seat.lastDisconnectedGrantJti = seat.grantJti;
            seat.lastDisconnectedNativeConnectionNonce =
                disconnectedNonce;
            seat.lastDisconnectedWorldInstanceId = seat.liveWorldInstanceId;
            seat.lastDisconnectedRouteGeneration = seat.liveRouteGeneration;
            seat.liveNativeConnectionNonce.clear();
            seat.liveWorldInstanceId.clear();
            seat.liveRouteGeneration = 0;
            if (pendingScopeIsLiveScope)
            {
                seat.nativeAdmitted = false;
                seat.nativeAdmissionGeneration = 0;
                seat.nativeAdmissionJti.clear();
                seat.nativeAdmissionNonce.clear();
                seat.nativeAdmissionWorldInstanceId.clear();
                seat.nativeAdmissionRouteGeneration = 0;
                seat.backendReserved = false;
            }
            seat.reportObserved = true;
            seat.reportedConnected = false;
            const auto activeDecision = activeDecisions_.find(seat.platformId);
            if (activeDecision != activeDecisions_.end() &&
                activeDecision->second.connectionGeneration == generation &&
                activeDecision->second.grantJti == seat.lastDisconnectedGrantJti &&
                activeDecision->second.nativeConnectionNonce == disconnectedNonce)
            {
                // Do not erase a decision that belongs to a newer confirmed
                // generation if an old disconnect arrives late.
                activeDecisions_.erase(activeDecision);
            }
            AppendConnectionEventLocked(
                seat, "DISCONNECTED", seat.grantJti, generation,
                seat.lastDisconnectedNativeConnectionNonce);
            return Accept();
        }

        Decision ReleaseAdmission(
            const std::string_view playerId,
            const int generation,
            const std::string_view grantJti,
            const std::string_view nativeConnectionNonce)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            if (!Detail::SafeNativeConnectionNonce(nativeConnectionNonce))
                return Reject("native_connection_nonce_required", "admission release requires the concrete handshake nonce");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end())
                return Reject("player_not_rostered", "player is not in the frozen roster");
            Seat& seat = found->second;
            if (!seat.reserved || seat.reservedGeneration != generation ||
                (seat.roomRole != "HOST" && seat.reservedJti != grantJti) ||
                seat.reservedNativeConnectionNonce != nativeConnectionNonce)
            {
                return seat.reserved ? Reject("reservation_mismatch", "admission reservation does not match")
                    : Accept();
            }
            seat.reserved = false;
            seat.reservedGeneration = 0;
            seat.reservedJti.clear();
            seat.reservedNativeConnectionNonce.clear();
            seat.nativeAdmitted = false;
            seat.nativeAdmissionGeneration = 0;
            seat.nativeAdmissionJti.clear();
            seat.nativeAdmissionNonce.clear();
            seat.backendReserved = false;
            if (!seat.connected)
                seat.grantJti.clear();
            return Accept();
        }

        bool HasLiveConnections() const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return false;
            return std::any_of(
                allocation_->seats.begin(), allocation_->seats.end(),
                [](const auto& entry) { return entry.second.connected; });
        }

        bool HasPendingAdmissions() const
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return false;
            return std::any_of(
                allocation_->seats.begin(), allocation_->seats.end(),
                [](const auto& entry) {
                    return entry.second.reserved || entry.second.nativeAdmitted;
                });
        }

        std::vector<ConnectionEvent> ConnectionEventsAfter(
            const std::uint64_t sequence,
            const std::size_t maximum = 64U) const
        {
            std::lock_guard lock(mutex_);
            std::vector<ConnectionEvent> result;
            result.reserve((std::min)(maximum, connectionEvents_.size()));
            for (const auto& event : connectionEvents_)
            {
                if (event.sequence <= sequence)
                    continue;
                result.push_back(event);
                if (result.size() >= maximum)
                    break;
            }
            return result;
        }

        void Reset() noexcept
        {
            std::lock_guard lock(mutex_);
            ResetLocked();
        }

    private:
        struct Seat
        {
            std::string playerId;
            std::string platformId;
            std::string roomRole;
            int teamId = 0;
            int teamSlot = -1;
            int logicalSlot = -1;
            // Signed allocation lower bound for a new connection.
            int generation = 0;
            // The generation that actually completed native PostLogin. It
            // may remain older than `generation` during a route refresh.
            int liveGeneration = 0;
            int lastDisconnectedGeneration = 0;
            std::string lastDisconnectedGrantJti;
            bool connected = false;
            bool reserved = false;
            int reservedGeneration = 0;
            std::string reservedJti;
            std::string reservedNativeConnectionNonce;
            std::string reservedWorldInstanceId;
            int reservedRouteGeneration = 0;
            bool backendReserved = false;
            bool nativeAdmitted = false;
            int nativeAdmissionGeneration = 0;
            std::string nativeAdmissionJti;
            std::string nativeAdmissionNonce;
            std::string nativeAdmissionWorldInstanceId;
            int nativeAdmissionRouteGeneration = 0;
            std::string liveNativeConnectionNonce;
            std::string liveWorldInstanceId;
            int liveRouteGeneration = 0;
            std::string lastDisconnectedNativeConnectionNonce;
            std::string lastDisconnectedWorldInstanceId;
            int lastDisconnectedRouteGeneration = 0;
            bool reportObserved = false;
            bool reportedConnected = false;
            std::string grantJti;
        };

        struct Allocation
        {
            std::string keyId;
            std::vector<std::uint8_t> publicKey;
			std::string tokenId;
            std::string attemptId;
            std::string lobbyId;
            std::string hostingKind;
            std::string authorityId;
            std::string authoritySession;
            std::int64_t rosterRevision = 0;
            int routeGeneration = 0;
			int connectionWindowSeconds = 0;
            std::int64_t expiresAt = 0;
            std::unordered_map<std::string, Seat> seats;
        };

        struct PendingGrant
        {
            std::string playerId;
            std::string platformId;
            std::string jti;
            std::string worldInstanceId;
            int teamId = 0;
            int teamSlot = -1;
            int logicalSlot = -1;
            int generation = 0;
            std::int64_t expiresAt = 0;
        };

        static Decision Accept()
        {
            return {true, "accepted", {}, {}};
        }

        static Decision Reject(std::string code, std::string message)
        {
            return {false, std::move(code), std::move(message), {}};
        }

        static SeatDecision RejectSeat(std::string code, std::string message)
        {
            SeatDecision result;
            result.code = std::move(code);
            result.message = std::move(message);
            return result;
        }

        static SeatDecision BuildReservedDecisionLocked(const Seat& seat)
        {
            SeatDecision decision;
            decision.accepted = true;
            decision.code = "accepted";
            decision.playerId = seat.playerId;
            decision.platformId = seat.platformId;
            decision.grantJti = seat.reservedJti;
            decision.nativeConnectionNonce = seat.reservedNativeConnectionNonce;
            decision.teamId = seat.teamId;
            decision.teamSlot = seat.teamSlot;
            decision.logicalSlot = seat.logicalSlot;
            decision.connectionGeneration = seat.reservedGeneration;
            decision.replacesConnection = seat.connected &&
                seat.reservedGeneration > seat.liveGeneration;
            decision.reserved = true;
            decision.hostSeat = seat.roomRole == "HOST";
            decision.backendReserved = seat.backendReserved;
            return decision;
        }

        static SeatDecision BuildConfirmedDecisionLocked(const Seat& seat)
        {
            SeatDecision decision;
            decision.accepted = true;
            decision.code = "accepted";
            decision.playerId = seat.playerId;
            decision.platformId = seat.platformId;
            decision.grantJti = seat.grantJti;
            decision.nativeConnectionNonce = seat.liveNativeConnectionNonce;
            decision.teamId = seat.teamId;
            decision.teamSlot = seat.teamSlot;
            decision.logicalSlot = seat.logicalSlot;
            decision.connectionGeneration = seat.liveGeneration;
            decision.confirmed = true;
            decision.hostSeat = seat.roomRole == "HOST";
            decision.backendReserved = seat.backendReserved;
            return decision;
        }

        void AppendConnectionEventLocked(
            const Seat& seat,
            const std::string_view state,
            const std::string_view grantJti = {},
            const int generation = 0,
            const std::string_view nativeConnectionNonce = {})
        {
            if (!allocation_)
                return;
            ConnectionEvent event;
            event.sequence = ++nextConnectionEventSequence_;
            event.attemptId = allocation_->attemptId;
            event.playerId = seat.playerId;
            event.grantJti = grantJti.empty()
                ? seat.grantJti : std::string(grantJti);
            event.nativeConnectionNonce = nativeConnectionNonce.empty()
                ? (seat.liveNativeConnectionNonce.empty()
                    ? seat.reservedNativeConnectionNonce
                    : seat.liveNativeConnectionNonce)
                : std::string(nativeConnectionNonce);
            event.connectionGeneration = generation != 0
                ? generation
                : (seat.liveGeneration != 0 ? seat.liveGeneration : seat.generation);
            if (state == "RESERVED")
            {
                event.routeGeneration = seat.reservedRouteGeneration;
                event.worldInstanceId = seat.reservedWorldInstanceId;
            }
            else if (state == "NATIVE_ADMITTED")
            {
                event.routeGeneration = seat.nativeAdmissionRouteGeneration;
                event.worldInstanceId = seat.nativeAdmissionWorldInstanceId;
            }
            else if (state == "CONNECTED")
            {
                event.routeGeneration = seat.liveRouteGeneration;
                event.worldInstanceId = seat.liveWorldInstanceId;
            }
            else if (state == "DISCONNECTED")
            {
                event.routeGeneration = seat.lastDisconnectedRouteGeneration;
                event.worldInstanceId = seat.lastDisconnectedWorldInstanceId;
            }
            event.authoritySessionId = allocation_->authoritySession;
            event.rosterRevision = allocation_->rosterRevision;
            event.state = state;
            event.connected = state == "CONNECTED";
            connectionEvents_.push_back(std::move(event));
            constexpr std::size_t kMaximumRetainedConnectionEvents = 256U;
            while (connectionEvents_.size() > kMaximumRetainedConnectionEvents)
                connectionEvents_.pop_front();
        }

        void ResetLocked() noexcept
        {
            if (allocation_)
            {
                Detail::SecureClear(allocation_->authoritySession);
                std::fill(
                    allocation_->publicKey.begin(), allocation_->publicKey.end(),
                    static_cast<std::uint8_t>(0));
            }
            if (allocation_)
            {
                for (auto& [playerId, seat] : allocation_->seats)
                {
                    (void)playerId;
                    Detail::SecureClear(seat.reservedJti);
                    Detail::SecureClear(seat.grantJti);
                    Detail::SecureClear(seat.nativeAdmissionJti);
                    Detail::SecureClear(seat.reservedNativeConnectionNonce);
                    Detail::SecureClear(seat.reservedWorldInstanceId);
                    Detail::SecureClear(seat.nativeAdmissionNonce);
                    Detail::SecureClear(seat.nativeAdmissionWorldInstanceId);
                    Detail::SecureClear(seat.liveNativeConnectionNonce);
                    Detail::SecureClear(seat.liveWorldInstanceId);
                    Detail::SecureClear(seat.lastDisconnectedNativeConnectionNonce);
                    Detail::SecureClear(seat.lastDisconnectedGrantJti);
                    Detail::SecureClear(seat.lastDisconnectedWorldInstanceId);
                    seat.backendReserved = false;
                    seat.nativeAdmitted = false;
                    seat.nativeAdmissionGeneration = 0;
                    seat.reservedRouteGeneration = 0;
                    seat.nativeAdmissionRouteGeneration = 0;
                    seat.liveRouteGeneration = 0;
                    seat.lastDisconnectedRouteGeneration = 0;
                }
            }
            allocation_.reset();
            usedJtis_.clear();
            stagedGrants_.clear();
            activeDecisions_.clear();
            connectionEvents_.clear();
            nextConnectionEventSequence_ = 0;
            Detail::SecureClear(nativeWorldInstanceId_);
            authorityStarted_ = false;
        }

        SignatureVerifier verifier_;
        bool nativeAdmissionPathReady_ = false;
        mutable std::mutex mutex_;
        std::optional<Allocation> allocation_;
        std::unordered_set<std::string> usedJtis_;
        std::unordered_map<std::string, PendingGrant> stagedGrants_;
        std::unordered_map<std::string, SeatDecision> activeDecisions_;
        std::deque<ConnectionEvent> connectionEvents_;
        std::uint64_t nextConnectionEventSequence_ = 0;
        std::string nativeWorldInstanceId_;
        bool authorityStarted_ = false;
    };
}
