#pragma once

#include <algorithm>
#include <array>
#include <cctype>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <unordered_set>
#include <vector>

namespace DedicatedMultiMatchPolicy
{
    enum class LifecycleState
    {
        Disabled,
        Running,
        ShowingResult,
        Voting,
        WaitingToTravel,
        Traveling,
        LoadingNext,
        FallbackExit,
    };

    struct MapInfo
    {
        std::string_view Alias;
        bool PveCompatible;
        std::string_view TravelPackage;
    };

    inline constexpr std::array<MapInfo, 10> KnownMaps{{
        {"OSS", true, "/Game/Maps/EA/Purge/OSS/OSS"},
        {"MiniFarm", true, "/Game/Maps/EA/Purge/MiniFarm/MiniFarm"},
        {"Warehouse", true, "/Game/Maps/EA/Domination/Warehouse/Warehouse"},
        {"Dusty", false, "Dusty"},
        {"DataCenter", true, "/Game/Maps/EA/Domination/DataCenter/DataCenter"},
        {"CircularX", true, "/Game/Maps/EA/Domination/CircularX/CircularX"},
        {"Museum_art", false, "Museum_art"},
        {"RelayStation", false, "RelayStation"},
        {"Oriolus", false, "Oriolus"},
        {"GangesRiver", false, "GangesRiver"},
    }};

    inline std::string NormalizeAscii(std::string_view value)
    {
        std::string normalized;
        normalized.reserve(value.size());
        for (const unsigned char character : value)
            normalized.push_back(static_cast<char>(std::tolower(character)));
        return normalized;
    }

    inline const MapInfo* FindKnownMap(std::string_view alias)
    {
        const std::string normalized = NormalizeAscii(alias);
        const auto result = std::find_if(
            KnownMaps.begin(), KnownMaps.end(), [&](const MapInfo& map) {
                return NormalizeAscii(map.Alias) == normalized;
            });
        return result == KnownMaps.end() ? nullptr : &*result;
    }

    inline std::string_view TravelPackageForMap(std::string_view alias)
    {
        const MapInfo* const map = FindKnownMap(alias);
        return map ? map->TravelPackage : std::string_view{};
    }

    inline constexpr std::string_view TravelMarker =
        "?ProjectReboundMultiMatch=1";

    inline bool IsOwnedTravelUrl(std::string_view url)
    {
        if (url.find(TravelMarker) == std::string_view::npos)
            return false;

        return std::any_of(KnownMaps.begin(), KnownMaps.end(),
            [url](const MapInfo& map) {
                if (map.TravelPackage.empty() ||
                    !url.starts_with(map.TravelPackage))
                {
                    return false;
                }
                return url.size() > map.TravelPackage.size() &&
                    url[map.TravelPackage.size()] == '?';
            });
    }

    // The pinned 0.4.3 image leaves one travel-owned tickable alive for the
    // first frames after UWorld::SeamlessTravel. Its adjacent delegate wrappers
    // at RVAs 0x164AB10, 0x164AB30, 0x164AB40, 0x164AB50 and 0x164AB90 add
    // +0x3C0/+0x300/+0x310/+0x3B0/+0x2C0 to null owners before entering the
    // script-multicast dispatcher, producing deterministic reads at
    // 0x3C8/0x308/0x318/0x3B8/0x2C8. The +0x2C0 case is the destination
    // RoundState multicast reached when the player deploys from role selection;
    // +0x3B0/+0x3C0 are first reached when the destination Pawn is recreated.
    // Keep this allow-list exact so unrelated delegate dispatch remains native
    // and P2P/unmarked travel is never affected.
    inline constexpr std::array<std::uintptr_t, 5>
        InvalidTravelDelegateThisValues{{0x2C0, 0x300, 0x310, 0x3B0, 0x3C0}};

    inline bool ShouldSuppressInvalidTravelDelegate(
        const std::uintptr_t delegateThis,
        const bool ownedTravelWindow)
    {
        return ownedTravelWindow &&
            std::find(InvalidTravelDelegateThisValues.begin(),
                InvalidTravelDelegateThisValues.end(), delegateThis) !=
                InvalidTravelDelegateThisValues.end();
    }

    inline bool ShouldArmClientTravelDelegateGuard(
        const bool ownedTravelUrl,
        const bool seamlessTravel)
    {
        return ownedTravelUrl && seamlessTravel;
    }

    // During the pinned client's owned seamless transition, replication first
    // can temporarily replicate a null or a non-PB AController::PlayerState
    // before the destination APBPlayerState is installed.  The override at RVA
    // 0x015BACC0 calls the Engine base implementation, then unconditionally
    // dispatches through the failed PB cast at +0x740.
    // Preserve the Engine notification and skip only that impossible PB tail,
    // only inside the already-proven owned-travel compatibility window.
    inline bool ShouldUseInvalidPlayerStateTravelFallback(
        const bool ownedTravelWindow,
        const bool playerStateUsable)
    {
        return ownedTravelWindow && !playerStateUsable;
    }

    // APBPlayerController's pinned input/observer eligibility helper at
    // 0x15A60D0 obtains the current PBGameInstance, unconditionally adds
    // 0x380, then reads +0x48. A second seamless hop can briefly make the
    // native resolver return null, producing the proven 0x3C8 read. Treat the
    // eligibility query as false only for that exact owned-travel null phase.
    inline bool ShouldUseUnavailableGameInstanceTravelFallback(
        const bool ownedTravelWindow,
        const bool gameInstanceAvailable)
    {
        return ownedTravelWindow && !gameInstanceAvailable;
    }

    // A source PBGameMode may receive its delayed EndMatch callback again
    // after the owned result path has already queued/committed travel. Reject
    // only an identified source/retired instance; the current authority must
    // retain its first native EndMatch.
    inline bool ShouldSuppressRetiredGameModeEndMatch(
        const bool multiMatchEnabled,
        const bool fallbackStarted,
        const bool travelInProgress,
        const bool gameModeIsTravelSource,
        const bool gameModeWasRetired,
        const bool gameModeIsCurrentAuthority)
    {
        if (!multiMatchEnabled || fallbackStarted)
            return false;
        if (travelInProgress && gameModeIsTravelSource)
            return true;
        return gameModeWasRetired && !gameModeIsCurrentAuthority;
    }

    inline bool ShouldBypassNullResultMvp(
        const bool multiMatchEnabled,
        const bool fallbackStarted,
        const bool gameModeIsCurrentAuthority,
        const bool mvpPlayerAvailable)
    {
        return multiMatchEnabled && !fallbackStarted &&
            gameModeIsCurrentAuthority && !mvpPlayerAvailable;
    }

    // The pinned dedicated bootstrap creates and owns the listening driver,
    // while native seamless travel temporarily clears both sides of its
    // World binding.  Repair is permitted only for the already-proven source
    // driver, after the owned travel request reached the destination World,
    // with the original connection set still intact and no competing owner.
    inline bool ShouldRepairDestinationNetDriverBinding(
        const bool ownedWorldTransition,
        const bool seamlessTravelDispatched,
        const bool destinationWorldReady,
        const bool destinationWorldHasNoDriver,
        const bool sourceDriverHasNoWorld,
        const bool connectionSetMatches)
    {
        return ownedWorldTransition && seamlessTravelDispatched &&
            destinationWorldReady && destinationWorldHasNoDriver &&
            sourceDriverHasNoWorld && connectionSetMatches;
    }

    // The pinned MVP selector uses AGameMode::NumPlayers plus
    // NumTravellingPlayers before it inspects PlayerArray. Seamless rebound
    // can leave both counters at zero even though validated human connections
    // were carried into the destination. Repair only that impossible zero
    // total, during the owned transition, from the exact preserved set.
    inline bool ShouldRepairSeamlessGameModePlayerCounts(
        const bool ownedWorldTransition,
        const int preservedHumanConnections,
        const int nativePlayers,
        const int nativeTravellingPlayers)
    {
        return ownedWorldTransition && preservedHumanConnections > 0 &&
            nativePlayers + nativeTravellingPlayers <= 0;
    }

    // WaitingToEndGame is observed from inside the native lifecycle callback.
    // Starting travel again from the first post-flush observation still leaves
    // result/UI cleanup on the same engine frame. Require one completed
    // TickFlush boundary before the existing seamless-travel path may run.
    inline bool ShouldBeginTravelAfterWaitingToEnd(
        const bool waitingToEndReceived,
        const bool waitingToTravel,
        const std::uint32_t completedPostWaitingTicks)
    {
        return waitingToEndReceived && waitingToTravel &&
            completedPostWaitingTicks >= 1;
    }

    // A controller that still owns a playable Pawn can remain on the native
    // seamless-carry path. A retained PlayerState role is not sufficient: if
    // travel destroyed the Pawn, treating that stale role as already confirmed
    // skips destination role selection, match intro, and the start-spot equip
    // gate. That exact shape must keep its connection but re-enter the existing
    // initial role-selection flow for the destination generation.
    inline bool ShouldPreserveSeamlessReboundPlayer(
        const bool ownedWorldTransition,
        const bool sourceConnectionProven,
        const bool controllerAlive,
        const bool hasPlayablePawn,
        const bool hasAuthoritativeRole)
    {
        (void)hasAuthoritativeRole;
        return ownedWorldTransition && sourceConnectionProven &&
            controllerAlive && hasPlayablePawn;
    }

    // A destination client can auto-submit its local default role after the
    // new Pawn is already playable.  Re-entering native confirmation then can
    // traverse the pinned controller's retired pre-ordering container.  Before
    // spawn, however, native confirmation is required to rebuild the new
    // world's PlayerState role and pre-order inventory, even when the requested
    // role matches the source match. Suppress only post-spawn confirmations for
    // a player explicitly registered by the owned transition. A real
    // post-death role selection is not Spawned and therefore remains native.
    inline bool ShouldSuppressSeamlessDuplicateRoleConfirmation(
        const bool isSeamlessRebound,
        const bool requestedRoleIsConcrete,
        const bool playerAlreadySpawned)
    {
        return isSeamlessRebound && requestedRoleIsConcrete &&
            playerAlreadySpawned;
    }

    inline bool IsSafeMapAlias(std::string_view alias)
    {
        return !alias.empty() && alias.size() <= 64 &&
            std::all_of(alias.begin(), alias.end(), [](const unsigned char character) {
                return std::isalnum(character) != 0 || character == '_';
            });
    }

    struct PlaylistValidation
    {
        bool Valid = false;
        std::vector<std::string> Playlist;
        std::string Detail;
    };

    inline PlaylistValidation ValidatePlaylist(
        const std::vector<std::string>& requested,
        const bool isPve)
    {
        PlaylistValidation result;
        if (requested.empty())
        {
            result.Detail = "playlist is empty";
            return result;
        }

        std::unordered_set<std::string> seen;
        result.Playlist.reserve(requested.size());
        for (const std::string& requestedAlias : requested)
        {
            if (!IsSafeMapAlias(requestedAlias))
            {
                result.Detail = "playlist contains an unsafe map alias";
                return result;
            }

            const MapInfo* const map = FindKnownMap(requestedAlias);
            if (!map)
            {
                result.Detail = "playlist contains an unknown map alias";
                return result;
            }
            if (isPve && !map->PveCompatible)
            {
                result.Detail = "playlist contains a map that is incompatible with PVE";
                return result;
            }

            const std::string key = NormalizeAscii(map->Alias);
            if (!seen.insert(key).second)
            {
                result.Detail = "playlist contains a duplicate map alias";
                return result;
            }
            result.Playlist.emplace_back(map->Alias);
        }

        if (result.Playlist.empty())
        {
            result.Detail = "playlist has no eligible maps";
            return result;
        }

        result.Valid = true;
        result.Detail = "ok";
        return result;
    }

    inline std::vector<std::string> BuildCandidates(
        const std::vector<std::string>& playlist,
        std::string_view currentMap,
        std::size_t requestedCount)
    {
        std::vector<std::string> result;
        if (playlist.empty() || requestedCount == 0)
            return result;

        std::size_t currentIndex = playlist.size() - 1;
        const std::string currentKey = NormalizeAscii(currentMap);
        for (std::size_t index = 0; index < playlist.size(); ++index)
        {
            if (NormalizeAscii(playlist[index]) == currentKey)
            {
                currentIndex = index;
                break;
            }
        }

        const std::size_t maximum = (std::min)(requestedCount, playlist.size());
        result.reserve(maximum);
        for (std::size_t offset = 1; offset <= playlist.size() && result.size() < maximum; ++offset)
        {
            const std::string& candidate = playlist[(currentIndex + offset) % playlist.size()];
            if (playlist.size() > 1 && NormalizeAscii(candidate) == currentKey)
                continue;
            result.push_back(candidate);
        }

        if (result.empty() && playlist.size() == 1)
            result.push_back(playlist.front());
        return result;
    }

    inline std::size_t ResolveWinner(
        const std::vector<std::string>& candidates,
        const std::vector<std::size_t>& voteCounts)
    {
        if (candidates.empty())
            return 0;

        std::size_t winner = 0;
        std::size_t highest = voteCounts.empty() ? 0 : voteCounts.front();
        for (std::size_t index = 1; index < candidates.size(); ++index)
        {
            const std::size_t count = index < voteCounts.size() ? voteCounts[index] : 0;
            if (count > highest)
            {
                highest = count;
                winner = index;
            }
        }
        return winner;
    }

    // The pinned game build bootstraps a command-line dedicated server through
    // a temporary client/standalone world before Payload creates the
    // authoritative listening NetDriver.  Some resulting authoritative worlds
    // therefore continue to report NM_Client/NM_Standalone even though they
    // have no ServerConnection. Accept that pinned-build shape only when the
    // exact dedicated bootstrap switch is present. NM_ListenServer remains
    // excluded so P2P/listen-host lifecycle is unchanged.
    inline bool IsDedicatedMatchHost(
        const int nativeNetMode,
        const bool hasExactServerSwitch,
        const bool authorityGameModeMatches,
        const bool hasNetDriver,
        const bool netDriverWorldMatches,
        const bool hasServerConnection)
    {
        if (!authorityGameModeMatches)
            return false;
        if (nativeNetMode == 1)
            return true;
        if (nativeNetMode != 0 && nativeNetMode != 3)
            return false;
        return hasExactServerSwitch && hasNetDriver && netDriverWorldMatches &&
            !hasServerConnection;
    }

    inline bool WorldNameMatchesMap(
        std::string_view worldName,
        std::string_view mapAlias)
    {
        const std::string normalizedWorld = NormalizeAscii(worldName);
        const std::string normalizedMap = NormalizeAscii(mapAlias);
        if (normalizedWorld.empty() || normalizedMap.empty())
            return false;
        if (normalizedWorld == normalizedMap)
            return true;
        return normalizedWorld.size() > normalizedMap.size() &&
            normalizedWorld.ends_with(normalizedMap) &&
            normalizedWorld[normalizedWorld.size() - normalizedMap.size() - 1] == '_';
    }

    inline std::optional<std::size_t> ParseVoteCommand(
        std::string_view message,
        const std::size_t candidateCount)
    {
        while (!message.empty() && std::isspace(static_cast<unsigned char>(message.front())) != 0)
            message.remove_prefix(1);
        while (!message.empty() && std::isspace(static_cast<unsigned char>(message.back())) != 0)
            message.remove_suffix(1);

        constexpr std::string_view prefix = "/vote";
        if (message.size() <= prefix.size() ||
            NormalizeAscii(message.substr(0, prefix.size())) != prefix)
        {
            return std::nullopt;
        }

        message.remove_prefix(prefix.size());
        while (!message.empty() && std::isspace(static_cast<unsigned char>(message.front())) != 0)
            message.remove_prefix(1);
        if (message.empty() || message.size() > 2 ||
            !std::all_of(message.begin(), message.end(), [](const unsigned char character) {
                return std::isdigit(character) != 0;
            }))
        {
            return std::nullopt;
        }

        std::size_t oneBased = 0;
        for (const char character : message)
            oneBased = oneBased * 10 + static_cast<std::size_t>(character - '0');
        if (oneBased == 0 || oneBased > candidateCount)
            return std::nullopt;
        return oneBased - 1;
    }

    inline const char* ToString(const LifecycleState state)
    {
        switch (state)
        {
        case LifecycleState::Running: return "Running";
        case LifecycleState::ShowingResult: return "ShowingResult";
        case LifecycleState::Voting: return "Voting";
        case LifecycleState::WaitingToTravel: return "WaitingToTravel";
        case LifecycleState::Traveling: return "Traveling";
        case LifecycleState::LoadingNext: return "LoadingNext";
        case LifecycleState::FallbackExit: return "FallbackExit";
        default: return "Disabled";
        }
    }
}
