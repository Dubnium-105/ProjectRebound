#pragma once

#include "../Libs/json.hpp"

#include <algorithm>
#include <cctype>
#include <cstdint>
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
    };

    struct SeatDecision : Decision
    {
        int teamId = 0;
        int teamSlot = -1;
        int logicalSlot = -1;
        int connectionGeneration = 0;
        bool replacesConnection = false;
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
				if (next.routeGeneration == allocation_->routeGeneration)
				{
					std::fill(allocation_->publicKey.begin(), allocation_->publicKey.end(),
						static_cast<std::uint8_t>(0));
					allocation_->keyId = std::move(next.keyId);
					allocation_->publicKey = std::move(next.publicKey);
					allocation_->tokenId = std::move(next.tokenId);
					allocation_->connectionWindowSeconds = next.connectionWindowSeconds;
					allocation_->expiresAt = next.expiresAt;
					return Accept();
				}

				// A resumed P2P authority is the same process/world and authority
				// session, but Meta advances the route and every seat generation.
				// Old connections are no longer live; preserve consumed JTIs while
				// replacing the signed generation snapshot.
				const bool wasStarted = authorityStarted_;
				Detail::SecureClear(allocation_->authoritySession);
				std::fill(allocation_->publicKey.begin(), allocation_->publicKey.end(),
					static_cast<std::uint8_t>(0));
				allocation_ = std::move(next);
				if (wasStarted && allocation_->hostingKind == "P2P")
				{
					for (auto& [playerId, seat] : allocation_->seats)
					{
						(void)playerId;
						seat.connected = seat.roomRole == "HOST";
					}
				}
				authorityStarted_ = wasStarted;
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
				host->second.connected = true;
            }
            authorityStarted_ = true;
            return Accept();
        }

        SeatDecision ValidateJoinGrant(
            const std::string_view grant,
            const std::string_view authenticatedPlatformId,
            const std::int64_t now)
        {
            std::lock_guard lock(mutex_);
            if (!nativeAdmissionPathReady_ || !authorityStarted_ || !allocation_)
                return RejectSeat("admission_closed", "strict admission is not active");
            if (allocation_->expiresAt <= now)
                return RejectSeat("allocation_expired", "strict admission allocation is expired");
            const auto jwt = Detail::ParseJwt(grant);
			if (!jwt || jwt->header.value("alg", "") != "EdDSA" ||
				jwt->header.value("typ", "") != "match-join+jwt" ||
                jwt->header.value("kid", "") != allocation_->keyId ||
                !verifier_(allocation_->publicKey, jwt->signedData, jwt->signature))
            {
                return RejectSeat("invalid_grant_signature", "join grant signature is invalid");
            }
            const auto& claims = jwt->claims;
            const std::string playerId = claims.value("player_id", "");
            const std::string platformId = claims.value("platform_id", "");
            const std::string jti = claims.value("jti", "");
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
                claims.value("exp", std::int64_t{0}) <= now ||
                platformId != authenticatedPlatformId || !Detail::SafeIdentifier(jti, 96U))
            {
                return RejectSeat("grant_claim_mismatch", "join grant claims do not match this authority");
            }
            const auto seatIt = allocation_->seats.find(playerId);
            if (seatIt == allocation_->seats.end())
                return RejectSeat("player_not_rostered", "player is not in the frozen roster");
            Seat& seat = seatIt->second;
			if (allocation_->hostingKind == "P2P" && seat.roomRole == "HOST")
				return RejectSeat("host_uses_allocation", "the local P2P host cannot use a remote join grant");
            const int generation = claims.value("connection_generation", 0);
            if (platformId != seat.platformId || claims.value("team_id", 0) != seat.teamId ||
                claims.value("team_slot", -1) != seat.teamSlot ||
                claims.value("logical_slot", -1) != seat.logicalSlot ||
                generation < seat.generation)
            {
                return RejectSeat("seat_claim_mismatch", "join grant does not own the frozen seat");
            }
            if (!usedJtis_.insert(jti).second)
                return RejectSeat("grant_replayed", "join grant was already consumed");
            if (seat.connected && generation == seat.generation)
                return RejectSeat("seat_already_connected", "seat already has a live connection");
            SeatDecision decision;
            decision.accepted = true;
            decision.code = "accepted";
            decision.teamId = seat.teamId;
            decision.teamSlot = seat.teamSlot;
            decision.logicalSlot = seat.logicalSlot;
            decision.connectionGeneration = generation;
            decision.replacesConnection = seat.connected && generation > seat.generation;
            seat.generation = generation;
            seat.connected = true;
            return decision;
        }

        Decision MarkDisconnected(const std::string_view playerId, const int generation)
        {
            std::lock_guard lock(mutex_);
            if (!allocation_)
                return Reject("allocation_unavailable", "allocation is unavailable");
            const auto found = allocation_->seats.find(std::string(playerId));
            if (found == allocation_->seats.end() || found->second.generation != generation)
                return Reject("connection_generation_stale", "disconnect generation is stale");
            found->second.connected = false;
            return Accept();
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
            int generation = 0;
            bool connected = false;
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

        static Decision Accept()
        {
            return {true, "accepted", {}};
        }

        static Decision Reject(std::string code, std::string message)
        {
            return {false, std::move(code), std::move(message)};
        }

        static SeatDecision RejectSeat(std::string code, std::string message)
        {
            SeatDecision result;
            result.code = std::move(code);
            result.message = std::move(message);
            return result;
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
            allocation_.reset();
            usedJtis_.clear();
            authorityStarted_ = false;
        }

        SignatureVerifier verifier_;
        bool nativeAdmissionPathReady_ = false;
        std::mutex mutex_;
        std::optional<Allocation> allocation_;
        std::unordered_set<std::string> usedJtis_;
        bool authorityStarted_ = false;
    };
}
