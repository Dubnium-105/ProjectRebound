#include "DedicatedMultiMatch.h"

#include <Windows.h>

#include <algorithm>
#include <chrono>
#include <fstream>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <vector>

#include "DedicatedMultiMatchPolicy.h"
#include "ServerLogic.h"
#include "../Config/CommandLinePolicy.h"
#include "../Config/Config.h"
#include "../Network/NetDriverAccess.h"
#include "../SDK.hpp"

extern uintptr_t BaseAddress;

namespace DedicatedMultiMatch
{
namespace
{
    using namespace SDK;
    using DedicatedMultiMatchPolicy::LifecycleState;

    constexpr uintptr_t kWorldServerTravelRva = 0x36D61B0;
    constexpr uintptr_t kWorldSeamlessTravelRva = 0x36D3D10;
    constexpr uintptr_t kNetDriverSetWorldRva = 0x33DF330;
    constexpr uintptr_t kWorldContextWorldOffset = 0x280;
    constexpr uintptr_t kWorldNextUrlOffset = 0x5F0;

    struct RuntimeConfig
    {
        bool Enabled = false;
        std::vector<std::string> Playlist;
        float TravelTimeoutSeconds = 45.0f;
        bool VoteEnabled = true;
        float VoteDurationSeconds = 15.0f;
        std::size_t VoteCandidateCount = 3;
    };

    struct RuntimeState
    {
        RuntimeConfig Config;
        LifecycleState Lifecycle = LifecycleState::Disabled;
        std::string ActiveMap;
        std::string NextMap;
        std::vector<std::string> Candidates;
        std::unordered_map<APBPlayerController*, std::size_t> Votes;
        float VoteRemainingSeconds = 0.0f;
        float TravelElapsedSeconds = 0.0f;
        std::uint64_t MatchGeneration = 0;
        std::uint64_t TravelGeneration = 0;
        bool WaitingToEndReceived = false;
        std::uint32_t CompletedPostWaitingTicks = 0;
        bool FallbackStarted = false;
        bool NativeTravelQueued = false;
        bool SeamlessTravelDispatched = false;
        bool EngineBrowseDispatchFailed = false;
        std::string EngineBrowseFailureReason;
        std::wstring PendingTravelUrl;
        UWorld* SourceWorld = nullptr;
        AGameModeBase* SourceGameMode = nullptr;
        AGameStateBase* SourceGameState = nullptr;
        UNetDriver* SourceNetDriver = nullptr;
        std::vector<UNetConnection*> SourceConnections;
        std::unordered_set<AGameModeBase*> RetiredGameModes;
    };

    RuntimeState gState;
    std::recursive_mutex gMutex;

    std::size_t DestroySourceCharactersBeforeTravel(UWorld* world)
    {
        if (!world)
            return 0;

        // Seamless travel can retain client replicas whose authoritative Pawn
        // was discarded with the source World. Close those actor channels
        // while the source NetDriver still has two normal flush boundaries;
        // never destroy controllers, PlayerStates, or connection objects.
        std::vector<APBCharacter*> sourceCharacters;
        for (int32 levelIndex = 0; levelIndex < world->Levels.Num(); ++levelIndex)
        {
            ULevel* const level = world->Levels[levelIndex];
            if (!level)
                continue;
            for (int32 actorIndex = 0; actorIndex < level->Actors.Num(); ++actorIndex)
            {
                AActor* const actor = level->Actors[actorIndex];
                if (!actor || actor->bActorIsBeingDestroyed ||
                    !actor->IsA(APBCharacter::StaticClass()))
                {
                    continue;
                }
                sourceCharacters.push_back(static_cast<APBCharacter*>(actor));
            }
        }

        std::size_t destroyed = 0;
        for (APBCharacter* const character : sourceCharacters)
        {
            if (!character || character->bActorIsBeingDestroyed)
                continue;
            character->K2_DestroyActor();
            ++destroyed;
        }
        return destroyed;
    }

    bool IsDedicatedWorld(APBGameMode* gameMode, const char* context)
    {
        UWorld* const world = UWorld::GetWorld();
        if (!gameMode || !world || BaseAddress == 0)
        {
            std::cout << "[MULTIMATCH_GATE] context="
                << (context ? context : "unknown")
                << " game_mode=" << (gameMode ? 1 : 0)
                << " world=" << (world ? 1 : 0)
                << " image_base=" << (BaseAddress ? 1 : 0)
                << " result=reject" << std::endl;
            return false;
        }

        using GetNetModeFn = int(__fastcall*)(const UWorld*);
        const auto getNetMode = reinterpret_cast<GetNetModeFn>(
            BaseAddress + 0x36CC300);
        try
        {
            const int nativeNetMode = getNetMode(world);
            // EndMatch can transiently clear UWorld::NetDriver before the
            // post-match callbacks run even though the listening driver still
            // owns this World. Recover only the previously validated binding;
            // Resolve(false) never object-scans or replaces another owner.
            UNetDriver* const netDriver = world->NetDriver
                ? world->NetDriver
                : NetDriverAccess::Resolve(false, false);
            const bool exactServerSwitch = CommandLinePolicy::HasExactSwitch(
                GetCommandLineA(), "-server");
            const bool authorityMatches = world->AuthorityGameMode == gameMode;
            const bool driverWorldMatches =
                netDriver && netDriver->World == world;
            const bool hasServerConnection =
                netDriver && netDriver->ServerConnection != nullptr;
            const bool accepted = DedicatedMultiMatchPolicy::IsDedicatedMatchHost(
                nativeNetMode,
                exactServerSwitch,
                authorityMatches,
                netDriver != nullptr,
                driverWorldMatches,
                hasServerConnection);
            std::cout << "[MULTIMATCH_GATE] context="
                << (context ? context : "unknown")
                << " native_net_mode=" << nativeNetMode
                << " exact_server=" << (exactServerSwitch ? 1 : 0)
                << " authority_matches=" << (authorityMatches ? 1 : 0)
                << " net_driver=" << (netDriver ? 1 : 0)
                << " driver_world_matches=" << (driverWorldMatches ? 1 : 0)
                << " server_connection=" << (hasServerConnection ? 1 : 0)
                << " result=" << (accepted ? "accept" : "reject")
                << std::endl;
            return accepted;
        }
        catch (...)
        {
            std::cout << "[MULTIMATCH_GATE] context="
                << (context ? context : "unknown")
                << " result=exception" << std::endl;
            return false;
        }
    }

    std::wstring WidenAscii(const std::string& value)
    {
        return std::wstring(value.begin(), value.end());
    }

    std::string TrimAscii(std::string_view value)
    {
        while (!value.empty() && std::isspace(static_cast<unsigned char>(value.front())) != 0)
            value.remove_prefix(1);
        while (!value.empty() && std::isspace(static_cast<unsigned char>(value.back())) != 0)
            value.remove_suffix(1);
        return std::string(value);
    }

    bool IsVoteCommand(std::string_view message)
    {
        const std::string trimmed = TrimAscii(message);
        constexpr std::string_view prefix = "/vote";
        if (trimmed.size() < prefix.size() ||
            DedicatedMultiMatchPolicy::NormalizeAscii(
                std::string_view(trimmed).substr(0, prefix.size())) != prefix)
        {
            return false;
        }
        return trimmed.size() == prefix.size() ||
            std::isspace(static_cast<unsigned char>(trimmed[prefix.size()])) != 0;
    }

    std::vector<std::size_t> BuildVoteCountsLocked()
    {
        std::vector<std::size_t> counts(gState.Candidates.size(), 0);
        for (const auto& vote : gState.Votes)
        {
            if (vote.second < counts.size())
                ++counts[vote.second];
        }
        return counts;
    }

    nlohmann::json BuildStatusLocked()
    {
        const std::vector<std::size_t> counts = BuildVoteCountsLocked();
        nlohmann::json vote = nlohmann::json::object();
        vote["enabled"] = gState.Config.VoteEnabled;
        vote["remainingSeconds"] =
            (std::max)(0.0f, gState.VoteRemainingSeconds);
        vote["candidates"] = nlohmann::json::array();
        for (std::size_t index = 0; index < gState.Candidates.size(); ++index)
        {
            nlohmann::json candidate = nlohmann::json::object();
            candidate["index"] = static_cast<std::uint64_t>(index + 1);
            candidate["map"] = gState.Candidates[index];
            candidate["votes"] = static_cast<std::uint64_t>(
                index < counts.size() ? counts[index] : 0);
            vote["candidates"].push_back(std::move(candidate));
        }

        nlohmann::json status = nlohmann::json::object();
        status["enabled"] = gState.Config.Enabled;
        status["lifecycleState"] =
            DedicatedMultiMatchPolicy::ToString(gState.Lifecycle);
        status["activeMap"] = gState.ActiveMap;
        status["nextMap"] = gState.NextMap;
        status["matchGeneration"] = gState.MatchGeneration;
        status["travelGeneration"] = gState.TravelGeneration;
        status["vote"] = std::move(vote);
        return status;
    }

    void PublishStatusLocked()
    {
        std::cout << "[MULTIMATCH_STATUS] " << BuildStatusLocked().dump() << std::endl;
    }

    void SetLifecycleLocked(const LifecycleState state)
    {
        if (gState.Lifecycle == state)
            return;
        gState.Lifecycle = state;
        PublishStatusLocked();
    }

    void SendMessageToPlayerLocked(APBPlayerController* playerController, const std::string& message)
    {
        if (!playerController || playerController->bActorIsBeingDestroyed)
            return;
        try
        {
            const std::wstring wide = WidenAscii(message);
            playerController->ClientMessage(FString(wide.c_str()), FName{}, 12.0f);
        }
        catch (...)
        {
            std::cout << "[MULTIMATCH] Failed to send a native client message." << std::endl;
        }
    }

    std::vector<APBPlayerController*> RemotePlayerControllersLocked()
    {
        std::vector<APBPlayerController*> players;
        UWorld* const world = UWorld::GetWorld();
        UNetDriver* const netDriver = world
            ? (world->NetDriver
                ? world->NetDriver
                : NetDriverAccess::Resolve(false, false))
            : nullptr;
        if (!netDriver || netDriver->World != world || netDriver->ServerConnection)
            return players;

        players.reserve(static_cast<std::size_t>((std::max)(
            0, netDriver->ClientConnections.Num())));
        for (UNetConnection* connection : netDriver->ClientConnections)
        {
            APlayerController* const playerController = connection
                ? connection->PlayerController
                : nullptr;
            if (!playerController ||
                !playerController->IsA(APBPlayerController::StaticClass()))
            {
                continue;
            }

            auto* const boundaryPlayerController =
                static_cast<APBPlayerController*>(playerController);
            if (boundaryPlayerController->bActorIsBeingDestroyed ||
                !ConnectedPlayerControllers.contains(boundaryPlayerController) ||
                DisconnectedPlayerControllers.contains(boundaryPlayerController))
            {
                continue;
            }
            players.push_back(boundaryPlayerController);
        }
        return players;
    }

    bool IsRemotePlayerControllerLocked(APBPlayerController* playerController)
    {
        if (!playerController)
            return false;
        const std::vector<APBPlayerController*> players =
            RemotePlayerControllersLocked();
        return std::find(players.begin(), players.end(), playerController) !=
            players.end();
    }

    void BroadcastLocked(const std::string& message)
    {
        const std::vector<APBPlayerController*> players =
            RemotePlayerControllersLocked();
        for (APBPlayerController* playerController : players)
            SendMessageToPlayerLocked(playerController, message);
    }

    void BroadcastCandidatesLocked()
    {
        if (gState.Candidates.empty())
            return;

        std::ostringstream message;
        message << "[VOTE] Next map";
        for (std::size_t index = 0; index < gState.Candidates.size(); ++index)
            message << "  " << (index + 1) << "=" << gState.Candidates[index];
        if (gState.Config.VoteEnabled)
            message << "  Type /vote <number>";
        BroadcastLocked(message.str());
    }

    void BroadcastVoteCountsLocked()
    {
        const std::vector<std::size_t> counts = BuildVoteCountsLocked();
        std::ostringstream message;
        message << "[VOTE]";
        for (std::size_t index = 0; index < gState.Candidates.size(); ++index)
        {
            message << " " << (index + 1) << "="
                << (index < counts.size() ? counts[index] : 0);
        }
        BroadcastLocked(message.str());
    }

    bool LoadRuntimeConfig(RuntimeConfig& outConfig, std::string& outDetail)
    {
        const std::string commandLine = GetCommandLineA();
        if (!CommandLinePolicy::HasExactSwitch(commandLine, "-server"))
        {
            outDetail = "exact -server bootstrap switch is absent";
            return false;
        }
        if (!CommandLinePolicy::HasExactSwitch(commandLine, "-DedicatedMultiMatch"))
        {
            outDetail = "opt-in switch is absent";
            return false;
        }

        const std::string configPath = GetCmdValue("-multimatchconfig=");
        if (configPath.empty())
        {
            outDetail = "-multimatchconfig is missing";
            return false;
        }

        std::ifstream input(configPath);
        if (!input.is_open())
        {
            outDetail = "multi-match config cannot be opened";
            return false;
        }

        nlohmann::json root;
        try
        {
            input >> root;
        }
        catch (...)
        {
            outDetail = "multi-match config is invalid JSON";
            return false;
        }

        if (!root.contains("multiMatch") || !root["multiMatch"].is_object())
        {
            outDetail = "multiMatch object is missing";
            return false;
        }
        const nlohmann::json& multiMatch = root["multiMatch"];
        if (!multiMatch.contains("enabled") || !multiMatch["enabled"].is_boolean())
        {
            outDetail = "multiMatch.enabled must be a boolean";
            return false;
        }
        if (!multiMatch["enabled"].get<bool>())
        {
            outDetail = "multiMatch.enabled is false";
            return false;
        }
        if (!multiMatch.contains("playlist") || !multiMatch["playlist"].is_array())
        {
            outDetail = "multiMatch.playlist is missing";
            return false;
        }

        std::vector<std::string> requested;
        for (const nlohmann::json& entry : multiMatch["playlist"])
        {
            if (!entry.is_string())
            {
                outDetail = "multiMatch.playlist contains a non-string entry";
                return false;
            }
            requested.push_back(entry.get<std::string>());
        }

        const auto validation = DedicatedMultiMatchPolicy::ValidatePlaylist(
            requested, ::Config.IsPvE);
        if (!validation.Valid)
        {
            outDetail = validation.Detail;
            return false;
        }

        float travelTimeout = 45.0f;
        if (multiMatch.contains("travelTimeoutSeconds"))
        {
            if (!multiMatch["travelTimeoutSeconds"].is_number_integer())
            {
                outDetail = "travelTimeoutSeconds must be an integer";
                return false;
            }
            travelTimeout = multiMatch["travelTimeoutSeconds"].get<float>();
        }
        if (travelTimeout < 10.0f || travelTimeout > 180.0f)
        {
            outDetail = "travelTimeoutSeconds must be between 10 and 180";
            return false;
        }

        RuntimeConfig config;
        config.Enabled = true;
        config.Playlist = validation.Playlist;
        config.TravelTimeoutSeconds = travelTimeout;

        if (multiMatch.contains("vote"))
        {
            if (!multiMatch["vote"].is_object())
            {
                outDetail = "multiMatch.vote must be an object";
                return false;
            }
            const nlohmann::json& vote = multiMatch["vote"];
            if (vote.contains("enabled") && !vote["enabled"].is_boolean())
            {
                outDetail = "multiMatch.vote.enabled must be a boolean";
                return false;
            }
            if (vote.contains("durationSeconds") &&
                !vote["durationSeconds"].is_number_integer())
            {
                outDetail = "multiMatch.vote.durationSeconds must be an integer";
                return false;
            }
            if (vote.contains("candidateCount") &&
                !vote["candidateCount"].is_number_integer())
            {
                outDetail = "multiMatch.vote.candidateCount must be an integer";
                return false;
            }
            config.VoteEnabled = vote.contains("enabled")
                ? vote["enabled"].get<bool>()
                : true;
            config.VoteDurationSeconds = vote.contains("durationSeconds")
                ? vote["durationSeconds"].get<float>()
                : 15.0f;
            const int candidateCount = vote.contains("candidateCount")
                ? vote["candidateCount"].get<int>()
                : 3;
            if (config.VoteDurationSeconds < 0.0f || config.VoteDurationSeconds > 60.0f ||
                candidateCount < 1 || candidateCount > 3)
            {
                outDetail = "vote duration/candidateCount is outside the supported range";
                return false;
            }
            config.VoteCandidateCount = static_cast<std::size_t>(candidateCount);
        }

        outConfig = std::move(config);
        outDetail = "ok";
        return true;
    }

    void PrepareResultLocked()
    {
        std::cout << "[MULTIMATCH_TRACE] prepare-result begin" << std::endl;
        gState.Candidates = DedicatedMultiMatchPolicy::BuildCandidates(
            gState.Config.Playlist,
            gState.ActiveMap,
            gState.Config.VoteCandidateCount);
        std::cout << "[MULTIMATCH_TRACE] prepare-result candidates="
            << gState.Candidates.size() << std::endl;
        gState.Votes.clear();
        gState.NextMap.clear();
        gState.WaitingToEndReceived = false;
        gState.CompletedPostWaitingTicks = 0;
        gState.VoteRemainingSeconds = gState.Config.VoteEnabled
            ? gState.Config.VoteDurationSeconds
            : 0.0f;
        std::cout << "[MULTIMATCH_TRACE] prepare-result publish-showing" << std::endl;
        SetLifecycleLocked(LifecycleState::ShowingResult);
        std::cout << "[MULTIMATCH_TRACE] prepare-result broadcast-candidates" << std::endl;
        BroadcastCandidatesLocked();
        if (gState.Config.VoteEnabled && gState.VoteRemainingSeconds > 0.0f)
            SetLifecycleLocked(LifecycleState::Voting);
        else
            SetLifecycleLocked(LifecycleState::WaitingToTravel);
        std::cout << "[MULTIMATCH_TRACE] prepare-result complete" << std::endl;
    }

    bool ConnectionSetMatchesLocked(UNetDriver* netDriver)
    {
        if (!netDriver || netDriver->ClientConnections.Num() !=
            static_cast<int32>(gState.SourceConnections.size()))
        {
            return false;
        }

        std::unordered_set<UNetConnection*> expected(
            gState.SourceConnections.begin(), gState.SourceConnections.end());
        for (UNetConnection* connection : netDriver->ClientConnections)
        {
            if (!connection || !expected.erase(connection))
                return false;
        }
        return expected.empty();
    }

    bool IsDestinationWorldLocked(UWorld* world)
    {
        if (!world || gState.NextMap.empty())
            return false;
        try
        {
            return DedicatedMultiMatchPolicy::WorldNameMatchesMap(
                world->GetName(), gState.NextMap);
        }
        catch (...)
        {
            return false;
        }
    }

    void StartFallbackLocked(const char* reason)
    {
        if (gState.FallbackStarted)
            return;
        gState.FallbackStarted = true;
        NetDriverAccess::SetHookArgumentRebindEnabled(true);
        SetLifecycleLocked(LifecycleState::FallbackExit);
        std::cout << "[MULTIMATCH] Falling back to native process cleanup: "
            << (reason ? reason : "unknown") << std::endl;

        UWorld* const world = UWorld::GetWorld();
        APBGameMode* gameMode = nullptr;
        if (world && world->AuthorityGameMode &&
            world->AuthorityGameMode->IsA(APBGameMode::StaticClass()))
        {
            gameMode = static_cast<APBGameMode*>(world->AuthorityGameMode);
        }
        BeginGracefulDedicatedExit(gameMode, reason);
    }

    void BeginTravelLocked()
    {
        if (gState.Lifecycle == LifecycleState::Traveling ||
            gState.Lifecycle == LifecycleState::LoadingNext ||
            gState.FallbackStarted)
        {
            return;
        }
        if (gState.Candidates.empty())
        {
            StartFallbackLocked("no eligible next map");
            return;
        }

        const std::vector<std::size_t> counts = BuildVoteCountsLocked();
        const std::size_t winner = DedicatedMultiMatchPolicy::ResolveWinner(
            gState.Candidates, counts);
        if (winner >= gState.Candidates.size())
        {
            StartFallbackLocked("vote winner is invalid");
            return;
        }

        UWorld* const world = UWorld::GetWorld();
        if (!world || !world->AuthorityGameMode || !world->GameState ||
            !world->AuthorityGameMode->IsA(APBGameMode::StaticClass()) ||
            !IsDedicatedWorld(
                static_cast<APBGameMode*>(world->AuthorityGameMode), "begin-travel"))
        {
            StartFallbackLocked("authoritative world is unavailable before travel");
            return;
        }

        UNetDriver* const netDriver = world->NetDriver
            ? world->NetDriver
            : NetDriverAccess::Resolve(false, false);
        if (!netDriver || netDriver->World != world)
        {
            StartFallbackLocked("active NetDriver does not belong to the source world");
            return;
        }

        if (!world->NetDriver &&
            !NetDriverAccess::RestoreValidatedBinding(world, netDriver))
        {
            StartFallbackLocked("validated NetDriver could not be rebound for travel");
            return;
        }
        NetDriverAccess::SetHookArgumentRebindEnabled(true);
        std::cout << "[MULTIMATCH_TRACE] begin-travel binding-ready" << std::endl;

        auto* const gameMode = static_cast<APBGameMode*>(world->AuthorityGameMode);
        gameMode->bUseSeamlessTravel = true;

        gState.NextMap = gState.Candidates[winner];
        gState.SourceWorld = world;
        gState.SourceGameMode = world->AuthorityGameMode;
        gState.SourceGameState = world->GameState;
        gState.SourceNetDriver = netDriver;
        gState.SourceConnections.clear();
        for (UNetConnection* connection : netDriver->ClientConnections)
        {
            if (connection)
                gState.SourceConnections.push_back(connection);
        }
        gState.TravelElapsedSeconds = 0.0f;
        ++gState.TravelGeneration;

        BroadcastLocked("[VOTE] Winner: " + gState.NextMap + ". Loading next match...");
        SetLifecycleLocked(LifecycleState::Traveling);

        const std::string_view travelPackage =
            DedicatedMultiMatchPolicy::TravelPackageForMap(gState.NextMap);
        if (travelPackage.empty())
        {
            StartFallbackLocked("next map has no pinned travel package");
            return;
        }
        const std::wstring urlText = WidenAscii(std::string(travelPackage)) +
            L"?game=" + ::Config.FullModePath +
            WidenAscii(std::string(DedicatedMultiMatchPolicy::TravelMarker));
        std::cout << "[MULTIMATCH_TRACE] seamless-travel package="
            << travelPackage << std::endl;
        FString url(urlText.c_str());

        // Queue through UWorld::ServerTravel so UGameEngine::TickWorldTravel
        // reaches its native world-travel boundary on the following engine
        // tick.  Starting FSeamlessTravelHandler directly from TickFlush left
        // old-world ticker callbacks active and crashed both peers.  The
        // pinned ProcessServerTravel virtual is a no-op, so the narrow Browse
        // detour completes only this owned transition at the safe boundary.
        gState.PendingTravelUrl = urlText;
        gState.NativeTravelQueued = true;
        gState.SeamlessTravelDispatched = false;
        gState.EngineBrowseDispatchFailed = false;
        gState.EngineBrowseFailureReason.clear();
        using ServerTravelFn = bool(__fastcall*)(
            UWorld*, const FString*, bool, bool);
        const auto serverTravel = reinterpret_cast<ServerTravelFn>(
            BaseAddress + kWorldServerTravelRva);
        const bool queued = serverTravel(world, &url, false, false);
        const std::string queuedUrl = url.ToString();
        FString* const nextUrl = reinterpret_cast<FString*>(
            reinterpret_cast<uintptr_t>(world) + kWorldNextUrlOffset);
        if (!queued || !nextUrl || nextUrl->Num() <= 1 ||
            nextUrl->ToString() != queuedUrl)
        {
            gState.NativeTravelQueued = false;
            StartFallbackLocked("native ServerTravel did not queue the pinned URL");
            return;
        }
        std::cout << "[MULTIMATCH_TRACE] native-server-travel queued=1"
            << std::endl;
    }

    void CompleteTravelIfReadyLocked(UNetDriver* tickNetDriver)
    {
        UWorld* const world = UWorld::GetWorld();
        if (!world || !world->AuthorityGameMode || !world->GameState)
            return;

        const bool newMatchObjects = world != gState.SourceWorld ||
            world->AuthorityGameMode != gState.SourceGameMode ||
            world->GameState != gState.SourceGameState;
        if (!newMatchObjects || !IsDestinationWorldLocked(world))
            return;

        if (!world->AuthorityGameMode->IsA(APBGameMode::StaticClass()) ||
            !IsDedicatedWorld(
                static_cast<APBGameMode*>(world->AuthorityGameMode), "complete-travel"))
        {
            return;
        }

        SetLifecycleLocked(LifecycleState::LoadingNext);
        UNetDriver* const assignedNetDriver = world->NetDriver;
        if ((assignedNetDriver && assignedNetDriver != gState.SourceNetDriver) ||
            (tickNetDriver && tickNetDriver != gState.SourceNetDriver))
        {
            StartFallbackLocked("seamless travel replaced the NetDriver");
            return;
        }
        if (assignedNetDriver != gState.SourceNetDriver ||
            !gState.SourceNetDriver || gState.SourceNetDriver->World != world)
        {
            return;
        }
        UNetDriver* const netDriver = gState.SourceNetDriver;
        if (!ConnectionSetMatchesLocked(netDriver))
            return;

        world->AuthorityGameMode->bUseSeamlessTravel = true;
        if (!BeginServerMatchGeneration(world, netDriver))
        {
            StartFallbackLocked("destination generation reset was refused");
            return;
        }

        gState.ActiveMap = gState.NextMap;
        gState.NextMap.clear();
        gState.Candidates.clear();
        gState.Votes.clear();
        gState.VoteRemainingSeconds = 0.0f;
        gState.WaitingToEndReceived = false;
        gState.CompletedPostWaitingTicks = 0;
        gState.SourceWorld = nullptr;
        if (gState.SourceGameMode)
            gState.RetiredGameModes.insert(gState.SourceGameMode);
        gState.SourceGameMode = nullptr;
        gState.SourceGameState = nullptr;
        gState.SourceNetDriver = nullptr;
        gState.SourceConnections.clear();
        gState.NativeTravelQueued = false;
        gState.SeamlessTravelDispatched = false;
        gState.EngineBrowseDispatchFailed = false;
        gState.EngineBrowseFailureReason.clear();
        gState.PendingTravelUrl.clear();
        ++gState.MatchGeneration;

        NetDriverAccess::SetHookArgumentRebindEnabled(true);
        SetLifecycleLocked(LifecycleState::Running);
        BroadcastLocked("[MATCH] Connected to " + gState.ActiveMap + ". Prepare for role selection.");
    }

    bool RepairDestinationNetDriverBindingLocked()
    {
        UWorld* const world = UWorld::GetWorld();
        UNetDriver* const netDriver = gState.SourceNetDriver;
        const bool destinationWorldReady = world && world != gState.SourceWorld &&
            world->AuthorityGameMode && world->GameState &&
            IsDestinationWorldLocked(world);
        const bool connectionSetMatches = netDriver &&
            ConnectionSetMatchesLocked(netDriver);
        if (!DedicatedMultiMatchPolicy::ShouldRepairDestinationNetDriverBinding(
                gState.Lifecycle == LifecycleState::Traveling ||
                    gState.Lifecycle == LifecycleState::LoadingNext,
                gState.SeamlessTravelDispatched,
                destinationWorldReady,
                world && world->NetDriver == nullptr,
                netDriver && netDriver->World == nullptr,
                connectionSetMatches))
        {
            return false;
        }

        // Use the pinned native UNetDriver::SetWorld implementation so its
        // internal WorldPackage/replication state follows the destination;
        // UWorld's public pointer is then restored only if SetWorld accepted
        // the exact already-listening driver.
        using NetDriverSetWorldFn = void(__fastcall*)(UNetDriver*, UWorld*);
        const auto setWorld = reinterpret_cast<NetDriverSetWorldFn>(
            BaseAddress + kNetDriverSetWorldRva);
        setWorld(netDriver, world);
        if (netDriver->World != world)
        {
            std::cout << "[MULTIMATCH_TRACE] destination-net-driver-rebind="
                         "rejected reason=native-set-world"
                      << std::endl;
            return false;
        }

        world->NetDriver = netDriver;
        NetDriverAccess::Observe(netDriver, world, NetDriverAccess::Source::Cached);
        if (world->NetDriver != netDriver || netDriver->World != world)
        {
            std::cout << "[MULTIMATCH_TRACE] destination-net-driver-rebind="
                         "rejected reason=postcondition"
                      << std::endl;
            return false;
        }

        NetDriverAccess::SetHookArgumentRebindEnabled(true);
        std::cout << "[MULTIMATCH_TRACE] destination-net-driver-rebind=complete"
                  << std::endl;
        return true;
    }
}

void Initialize()
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    NetDriverAccess::SetHookArgumentRebindEnabled(true);
    gState = RuntimeState{};
    std::string detail;
    RuntimeConfig config;
    bool valid = false;
    try
    {
        valid = LoadRuntimeConfig(config, detail);
    }
    catch (const std::exception& error)
    {
        detail = std::string("configuration validation failed: ") + error.what();
    }
    catch (...)
    {
        detail = "configuration validation failed with an unknown error";
    }
    if (!valid)
    {
        std::cout << "[MULTIMATCH] Disabled: " << detail << std::endl;
        return;
    }

    gState.Config = std::move(config);
    gState.ActiveMap = std::string(::Config.MapName.begin(), ::Config.MapName.end());
    gState.MatchGeneration = 1;
    gState.Lifecycle = LifecycleState::Running;
    std::cout << "[MULTIMATCH] Enabled with " << gState.Config.Playlist.size()
        << " ordered map(s); native seamless travel is required." << std::endl;
    PublishStatusLocked();
}

bool IsEnabled()
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    return gState.Config.Enabled;
}

bool OwnsWorldTransition()
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    return gState.Config.Enabled && !gState.FallbackStarted &&
        (gState.Lifecycle == LifecycleState::Traveling ||
         gState.Lifecycle == LifecycleState::LoadingNext);
}

EngineBrowseInterceptResult InterceptEngineBrowse(void* worldContext)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled || gState.FallbackStarted ||
        gState.Lifecycle != LifecycleState::Traveling ||
        !gState.NativeTravelQueued || !worldContext)
    {
        return EngineBrowseInterceptResult::PassThrough;
    }

    UWorld* const contextWorld = *reinterpret_cast<UWorld**>(
        reinterpret_cast<uintptr_t>(worldContext) + kWorldContextWorldOffset);
    if (contextWorld != gState.SourceWorld)
        return EngineBrowseInterceptResult::PassThrough;

    auto failOwnedBrowse = [](const char* reason) {
        gState.NativeTravelQueued = false;
        gState.EngineBrowseDispatchFailed = true;
        gState.EngineBrowseFailureReason = reason ? reason : "unknown Browse failure";
        std::cout << "[MULTIMATCH_TRACE] engine-browse intercepted=1 result=failure reason="
            << gState.EngineBrowseFailureReason << std::endl;
        return EngineBrowseInterceptResult::HandledFailure;
    };

    if (!contextWorld || contextWorld != UWorld::GetWorld() ||
        !gState.SourceNetDriver || gState.SourceNetDriver->World != contextWorld ||
        !ConnectionSetMatchesLocked(gState.SourceNetDriver) ||
        gState.PendingTravelUrl.empty())
    {
        return failOwnedBrowse("source world, NetDriver, or connection proof changed");
    }

    FString* const nextUrl = reinterpret_cast<FString*>(
        reinterpret_cast<uintptr_t>(contextWorld) + kWorldNextUrlOffset);
    FString url(gState.PendingTravelUrl.c_str());
    const std::string expectedUrl = url.ToString();
    if (!nextUrl || nextUrl->Num() <= 1 || nextUrl->ToString() != expectedUrl)
        return failOwnedBrowse("TickWorldTravel URL does not match the queued playlist URL");

    std::vector<APlayerController*> travelingPlayers;
    travelingPlayers.reserve(gState.SourceConnections.size());
    for (UNetConnection* connection : gState.SourceConnections)
    {
        APlayerController* const playerController =
            connection ? connection->PlayerController : nullptr;
        if (!playerController || playerController->bActorIsBeingDestroyed)
            return failOwnedBrowse("source connection lost its PlayerController");
        travelingPlayers.push_back(playerController);
    }

    FGuid packageGuid{};
    gState.NativeTravelQueued = false;
    gState.SeamlessTravelDispatched = true;

    // TickWorldTravel has already invoked the native StartToLeaveMap virtual.
    // Notify clients at this boundary. The pinned UWorld::SeamlessTravel
    // detour defers both peers until the enclosing UGameEngine::Tick returns,
    // so neither side ticks the retired World's timers after migration starts.
    for (APlayerController* playerController : travelingPlayers)
    {
        playerController->ClientTravel(
            url,
            ETravelType::TRAVEL_Relative,
            true,
            packageGuid);
    }
    std::cout << "[MULTIMATCH_TRACE] engine-browse intercepted=1 client-travel="
        << travelingPlayers.size() << std::endl;

    using SeamlessTravelFn = void(__fastcall*)(
        UWorld*, const FString*, bool, const FGuid*);
    const auto seamlessTravel = reinterpret_cast<SeamlessTravelFn>(
        BaseAddress + kWorldSeamlessTravelRva);
    NetDriverAccess::SetHookArgumentRebindEnabled(false);
    seamlessTravel(contextWorld, &url, false, &packageGuid);
    // Browse normally replaces the source World, so its success path does not
    // clear UWorld::NextURL. Clear the owned queue only after the seamless
    // request has been accepted by the deferral hook, matching native order.
    nextUrl->Clear();
    std::cout << "[MULTIMATCH_TRACE] engine-browse intercepted=1 seamless-travel=requested"
        << std::endl;
    return EngineBrowseInterceptResult::HandledSuccess;
}

void OnGameEnginePostTick()
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled || gState.FallbackStarted ||
        (gState.Lifecycle != LifecycleState::Traveling &&
         gState.Lifecycle != LifecycleState::LoadingNext))
    {
        return;
    }

    RepairDestinationNetDriverBindingLocked();
    CompleteTravelIfReadyLocked(gState.SourceNetDriver);
}

void Tick(const float deltaSeconds, UNetDriver* tickNetDriver)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled || gState.FallbackStarted)
        return;

        if (gState.Lifecycle == LifecycleState::Voting &&
            gState.VoteRemainingSeconds > 0.0f)
        {
        gState.VoteRemainingSeconds = (std::max)(
            0.0f, gState.VoteRemainingSeconds - (std::max)(0.0f, deltaSeconds));
            if (gState.VoteRemainingSeconds <= 0.0f)
            {
                std::cout << "[MULTIMATCH_TRACE] vote-expired" << std::endl;
                SetLifecycleLocked(LifecycleState::WaitingToTravel);
                BroadcastVoteCountsLocked();
                std::cout << "[MULTIMATCH_TRACE] vote-expired broadcast-complete"
                    << std::endl;
            }
    }

    if (DedicatedMultiMatchPolicy::ShouldBeginTravelAfterWaitingToEnd(
            gState.WaitingToEndReceived,
            gState.Lifecycle == LifecycleState::WaitingToTravel,
            gState.CompletedPostWaitingTicks))
    {
        BeginTravelLocked();
    }
    else if (gState.WaitingToEndReceived &&
        gState.Lifecycle == LifecycleState::WaitingToTravel)
    {
        ++gState.CompletedPostWaitingTicks;
        std::cout << "[MULTIMATCH] Preserved one post-WaitingToEndGame engine tick "
                     "before travel."
                  << std::endl;
    }

    if (gState.Lifecycle == LifecycleState::Traveling ||
        gState.Lifecycle == LifecycleState::LoadingNext)
    {
        if (gState.EngineBrowseDispatchFailed)
        {
            const std::string reason = gState.EngineBrowseFailureReason;
            gState.EngineBrowseDispatchFailed = false;
            StartFallbackLocked(reason.c_str());
            return;
        }
        gState.TravelElapsedSeconds += (std::max)(0.0f, deltaSeconds);
        CompleteTravelIfReadyLocked(tickNetDriver);
        if (!gState.FallbackStarted &&
            gState.TravelElapsedSeconds >= gState.Config.TravelTimeoutSeconds)
        {
            StartFallbackLocked("seamless travel timed out");
        }
    }
}

void OnShowingMatchResult(APBGameMode* gameMode)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled || gState.FallbackStarted ||
        gState.Lifecycle != LifecycleState::Running ||
        !IsDedicatedWorld(gameMode, "showing-result"))
    {
        return;
    }
    // The native result transition deliberately clears UWorld::NetDriver.
    // Keep TickFlush observation read-only during the result/vote interval;
    // BeginTravelLocked performs the one explicit, validated rebind.
    NetDriverAccess::SetHookArgumentRebindEnabled(false);
    PrepareResultLocked();
}

bool HandleWaitingToEndGame(APBGameMode* gameMode)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled)
        return false;
    if (gState.FallbackStarted)
        return true;
    if (!IsDedicatedWorld(gameMode, "waiting-to-end"))
        return false;

    UWorld* const world = UWorld::GetWorld();
    if (!world || world->AuthorityGameMode != gameMode)
        return false;

    if (gState.Lifecycle == LifecycleState::Running)
        PrepareResultLocked();

    const bool firstWaitingToEnd = !gState.WaitingToEndReceived;
    gState.WaitingToEndReceived = true;
    if (firstWaitingToEnd)
    {
        gState.CompletedPostWaitingTicks = 0;
        const std::size_t destroyedCharacters =
            DestroySourceCharactersBeforeTravel(world);
        std::cout << "[MULTIMATCH] Retired " << destroyedCharacters
                  << " source character actor(s) before seamless travel; "
                     "preserving two NetDriver flush boundaries."
                  << std::endl;
    }
    if (RemotePlayerControllersLocked().empty())
        gState.VoteRemainingSeconds = 0.0f;
    if (!gState.Config.VoteEnabled || gState.VoteRemainingSeconds <= 0.0f)
        SetLifecycleLocked(LifecycleState::WaitingToTravel);

    if (firstWaitingToEnd)
    {
        std::cout << "[MULTIMATCH] Suppressed native return-to-menu/exit at WaitingToEndGame."
            << std::endl;
    }
    return true;
}

bool ShouldSuppressRetiredEndMatch(APBGameMode* gameMode)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    UWorld* const world = UWorld::GetWorld();
    const bool travelInProgress =
        gState.Lifecycle == LifecycleState::Traveling ||
        gState.Lifecycle == LifecycleState::LoadingNext;
    return gameMode && DedicatedMultiMatchPolicy::
        ShouldSuppressRetiredGameModeEndMatch(
            gState.Config.Enabled,
            gState.FallbackStarted,
            travelInProgress,
            gState.SourceGameMode == gameMode,
            gState.RetiredGameModes.contains(gameMode),
            world && world->AuthorityGameMode == gameMode);
}

bool ShouldBypassNullResultMvp(APBGameMode* gameMode)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    UWorld* const world = UWorld::GetWorld();
    return DedicatedMultiMatchPolicy::ShouldBypassNullResultMvp(
        gState.Config.Enabled,
        gState.FallbackStarted,
        gameMode && world && world->AuthorityGameMode == gameMode,
        false);
}

bool ShouldSuppressNativeFinalCleanup(APBGameMode* gameMode)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    return gameMode && gState.Config.Enabled && !gState.FallbackStarted;
}

bool HandleServerSay(APBPlayerController* playerController, const std::string& message)
{
    if (!IsVoteCommand(message))
        return false;

    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (!gState.Config.Enabled)
        return false;
    UWorld* const world = UWorld::GetWorld();
    if (!world || !world->AuthorityGameMode ||
        !world->AuthorityGameMode->IsA(APBGameMode::StaticClass()) ||
        !IsDedicatedWorld(
            static_cast<APBGameMode*>(world->AuthorityGameMode), "server-say"))
    {
        return false;
    }
    if (gState.Lifecycle != LifecycleState::Voting)
    {
        SendMessageToPlayerLocked(playerController, "[VOTE] Voting is not open.");
        return true;
    }

    const auto candidate = DedicatedMultiMatchPolicy::ParseVoteCommand(
        message, gState.Candidates.size());
    if (!candidate)
    {
        std::ostringstream usage;
        usage << "[VOTE] Use /vote <number> from 1 to "
            << gState.Candidates.size() << ".";
        SendMessageToPlayerLocked(playerController, usage.str());
        return true;
    }
    if (!IsRemotePlayerControllerLocked(playerController))
    {
        return true;
    }

    gState.Votes[playerController] = *candidate;
    SendMessageToPlayerLocked(
        playerController, "[VOTE] Selected " + gState.Candidates[*candidate] + ".");
    BroadcastVoteCountsLocked();
    return true;
}

void OnPlayerDisconnected(APBPlayerController* playerController)
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    if (gState.Votes.erase(playerController) > 0 &&
        gState.Lifecycle == LifecycleState::Voting)
    {
        BroadcastVoteCountsLocked();
    }
}

nlohmann::json BuildStatusPayload()
{
    std::lock_guard<std::recursive_mutex> lock(gMutex);
    return BuildStatusLocked();
}
}
