// Hooks.cpp
#include "Hooks.h"
#include "ServerHookPolicy.h"
#include <Windows.h>
#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <unordered_set>
#include <vector>
#include "ArchiveCompletionPolicy.h"
#include "../SDK.hpp"
#include "../Network/NetDriverAccess.h"
#include "../Network/Network.h"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../safetyhook/safetyhook.hpp"
#include "../Libs/json.hpp"
#include "../Replication/libreplicate.h"
#include "../ServerLogic/LateJoinManager.h"
#include "../ServerLogic/JoinUiSyncPolicy.h"
#include "../ServerLogic/RespawnStatePolicy.h"
#include "../Loadout/LoadoutManager.h"
#include "../Config/Config.h"
#include "../Config/CommandLinePolicy.h"
#include "../Debug/Debug.h"
#include "../Debug/DebugTool.h"
#include "../ServerLogic/ServerLogic.h"
#include "../ServerLogic/DedicatedMultiMatch.h"
#include "../ServerLogic/DedicatedMultiMatchPolicy.h"
#include "../ClientLogic/ClientLogic.h"
#include "../ClientLogic/DirectMatchUiCleanupPolicy.h"
#include "../ClientLogic/SeamlessIntroCameraPolicy.h"
#include "../Utility/Utility.h"
#include "../BattleLog/BattleLogExtractor.h"

extern uintptr_t BaseAddress;
extern LibReplicate* libReplicate;
extern DebugTool* gDebugTool;
extern LoadoutManager* gLoadoutManager;
extern std::recursive_mutex gLoadoutManagerMutex;

using namespace SDK;

namespace
{
    constexpr uintptr_t kStrictRosterPreLoginRva = 0x01639D90;
    constexpr uint8_t kStrictRosterPreLoginPrologue[] = {
        0x48, 0x89, 0x5C, 0x24, 0x08, 0x48, 0x89, 0x6C,
        0x24, 0x10, 0x48, 0x89, 0x74, 0x24, 0x18, 0x57
    };

    // Pinned ProjectBoundarySteam-Win64-Shipping.exe
    // SHA-256 181C49FFB522B3EB01014C84FD9D3A2A5C0B66AE80A6A6ADDFF4BDD6F8125843.
    // APBPlayerState implements IGenericTeamAgentInterface in the secondary
    // subobject at +0x320. Its SetGenericTeamId entry is vtable +0x10 and the
    // concrete body below only accepts authority Role==3 before writing
    // MyTeamID at player-state +0x344.
    constexpr uintptr_t kStrictRosterSetTeamRva = 0x01681100;
    constexpr uintptr_t kStrictRosterGetTeamRva = 0x016675B0;
    constexpr uintptr_t kStrictRosterTeamInterfaceOffset = 0x0320;
    constexpr uintptr_t kStrictRosterTeamSetterVtableOffset = 0x0010;
    constexpr uintptr_t kStrictRosterTeamGetterVtableOffset = 0x0018;
    constexpr uint8_t kStrictRosterSetTeamPrologue[] = {
        0x80, 0xB9, 0xD0, 0xFD, 0xFF, 0xFF, 0x03, 0x75,
        0x06, 0x0F, 0xB6, 0x02, 0x88, 0x41, 0x24, 0xC3
    };
    constexpr uint8_t kStrictRosterGetTeamPrologue[] = {
        0x0F, 0xB6, 0x41, 0x24, 0x88, 0x02, 0x48, 0x8B,
        0xC2, 0xC3
    };

    // Authority-only APBPlayerState camp setter. It writes MyCampID (+0x348),
    // then executes the native actor replication/update path.
    constexpr uintptr_t kStrictRosterSetCampRva = 0x01680EF0;
    constexpr uint8_t kStrictRosterSetCampPrologue[] = {
        0x40, 0x53, 0x48, 0x83, 0xEC, 0x20, 0x80, 0xB9,
        0xF0, 0x00, 0x00, 0x00, 0x03, 0x48, 0x8B, 0xD9,
        0x75, 0x53
    };

    // UPBTeamManager::QueryCorrespondingCampIDByTeamID native body. The
    // world subsystem owns the current mode's TeamID -> CampID map, so the
    // roster never assumes that the two enum values happen to be identical.
    constexpr uintptr_t kStrictRosterQueryCampRva = 0x0167C9E0;
    constexpr uint8_t kStrictRosterQueryCampPrologue[] = {
        0x8B, 0x41, 0x50, 0x3B, 0x41, 0x7C, 0x74, 0x4E,
        0x48, 0x63, 0x81, 0x90, 0x00, 0x00, 0x00, 0x4C
    };

    static_assert(offsetof(AActor, Role) == 0x00F0);
    static_assert(offsetof(APlayerState, UniqueId) == 0x0250);
    static_assert(offsetof(APBPlayerState, MyTeamID) == 0x0344);
    static_assert(offsetof(APBPlayerState, MyCampID) == 0x0348);
    static_assert(offsetof(APBPlayerController, PBPlayerState) == 0x05B8);

    StrictRoster::Policy* gStrictRosterPolicy = nullptr;
    SafetyHookInline gStrictRosterPreLoginHook;
    std::atomic_bool gStrictRosterNativeSeatPathReady{false};
    std::mutex gStrictRosterLocalHostSeatMutex;
    std::optional<StrictRoster::SeatDecision> gStrictRosterLocalHostSeat;
    std::mutex gStrictRosterControllerMutex;
    std::unordered_map<APBPlayerController*, StrictRoster::SeatDecision>
        gStrictRosterControllerSeats;

    enum class StrictRosterSeatApplyResult
    {
        Inactive,
        Applied,
        Pending,
        Rejected,
    };

    std::int64_t StrictRosterEpochSeconds() noexcept
    {
        return std::chrono::duration_cast<std::chrono::seconds>(
            std::chrono::system_clock::now().time_since_epoch()).count();
    }

    bool IsReadableAddress(const void* address, const size_t size) noexcept
    {
        if (!address || size == 0)
            return false;
        MEMORY_BASIC_INFORMATION memory{};
        if (VirtualQuery(address, &memory, sizeof(memory)) != sizeof(memory) ||
            memory.State != MEM_COMMIT || (memory.Protect & PAGE_GUARD) != 0 ||
            memory.Protect == PAGE_NOACCESS)
        {
            return false;
        }
        const uintptr_t start = reinterpret_cast<uintptr_t>(address);
        const uintptr_t end = start + size;
        const uintptr_t regionEnd = reinterpret_cast<uintptr_t>(memory.BaseAddress) +
            memory.RegionSize;
        return end >= start && end <= regionEnd;
    }

    bool IsExecutableAddress(const void* address) noexcept
    {
        MEMORY_BASIC_INFORMATION memory{};
        if (!address || VirtualQuery(address, &memory, sizeof(memory)) != sizeof(memory) ||
            memory.State != MEM_COMMIT || (memory.Protect & PAGE_GUARD) != 0)
        {
            return false;
        }
        const DWORD protection = memory.Protect & 0xFFU;
        return protection == PAGE_EXECUTE || protection == PAGE_EXECUTE_READ ||
            protection == PAGE_EXECUTE_READWRITE || protection == PAGE_EXECUTE_WRITECOPY;
    }

    bool MatchesPinnedBytes(
        const uintptr_t rva,
        const uint8_t* expected,
        const size_t expectedSize) noexcept
    {
        if (BaseAddress == 0 || !expected || expectedSize == 0)
            return false;
        const void* const address = reinterpret_cast<const void*>(BaseAddress + rva);
        return IsReadableAddress(address, expectedSize) &&
            std::memcmp(address, expected, expectedSize) == 0;
    }

    bool StrictRosterNativeSeatPathMatchesPinnedImage() noexcept
    {
        return MatchesPinnedBytes(
                kStrictRosterSetTeamRva,
                kStrictRosterSetTeamPrologue,
                sizeof(kStrictRosterSetTeamPrologue)) &&
            MatchesPinnedBytes(
                kStrictRosterGetTeamRva,
                kStrictRosterGetTeamPrologue,
                sizeof(kStrictRosterGetTeamPrologue)) &&
            MatchesPinnedBytes(
                kStrictRosterSetCampRva,
                kStrictRosterSetCampPrologue,
                sizeof(kStrictRosterSetCampPrologue)) &&
            MatchesPinnedBytes(
                kStrictRosterQueryCampRva,
                kStrictRosterQueryCampPrologue,
                sizeof(kStrictRosterQueryCampPrologue));
    }

    bool ExtractStrictRosterPlatformId(
        const FUniqueNetIdRepl* uniqueId,
        std::string& platformId) noexcept
    {
        platformId.clear();
        if (!IsReadableAddress(uniqueId, 0x18U))
            return false;
        void* const nativeId = *reinterpret_cast<void* const*>(
            reinterpret_cast<const uint8_t*>(uniqueId) + 0x08U);
        if (!IsReadableAddress(nativeId, sizeof(void*)))
            return false;
        void** const vtable = *reinterpret_cast<void***>(nativeId);
        if (!IsReadableAddress(vtable, 0x38U))
            return false;
        void* const validityEntry = vtable[0x28U / sizeof(void*)];
        void* const toStringEntry = vtable[0x30U / sizeof(void*)];
        if (!IsExecutableAddress(validityEntry) || !IsExecutableAddress(toStringEntry))
            return false;
        using IsValidFn = bool(__fastcall*)(void*);
        using ToStringFn = void(__fastcall*)(void*, FString*);
        if (!reinterpret_cast<IsValidFn>(validityEntry)(nativeId))
            return false;
        FString value;
        reinterpret_cast<ToStringFn>(toStringEntry)(nativeId, &value);
        platformId = value.ToString();
        return platformId.size() >= 15U && platformId.size() <= 20U &&
            std::all_of(platformId.begin(), platformId.end(), [](const unsigned char ch) {
                return ch >= '0' && ch <= '9';
            });
    }

    void RejectStrictRosterPreLogin(FString* errorMessage, const wchar_t* reason)
    {
        if (errorMessage)
            *errorMessage = FString(reason);
    }

    void StrictRosterPreLogin(
        AGameMode* gameMode,
        const FString* options,
        const FString* address,
        const FUniqueNetIdRepl* uniqueId,
        FString* errorMessage)
    {
        gStrictRosterPreLoginHook.call<void>(
            gameMode, options, address, uniqueId, errorMessage);
        if (!gStrictRosterPolicy || !gStrictRosterPolicy->AdmissionActive())
            return;
        if (!errorMessage || !errorMessage->ToWString().empty())
            return;
        std::string platformId;
        if (!ExtractStrictRosterPlatformId(uniqueId, platformId))
        {
            RejectStrictRosterPreLogin(errorMessage,
                L"STRICT_ROSTER_IDENTITY_UNAVAILABLE");
            std::cout << "[STRICT-ROSTER] PreLogin rejected an unreadable platform identity."
                << std::endl;
            return;
        }
        const StrictRoster::SeatDecision decision =
            gStrictRosterPolicy->ConsumeStagedJoinGrant(
                platformId, StrictRosterEpochSeconds());
        if (!decision.accepted)
        {
            RejectStrictRosterPreLogin(errorMessage,
                L"STRICT_ROSTER_ADMISSION_REQUIRED");
            std::cout << "[STRICT-ROSTER] PreLogin rejected: " << decision.code << "."
                << std::endl;
            return;
        }
        std::cout << "[STRICT-ROSTER] PreLogin admitted frozen team="
            << decision.teamId << " slot=" << decision.logicalSlot
            << " generation=" << decision.connectionGeneration << "." << std::endl;
    }

    StrictRosterSeatApplyResult ApplyStrictRosterSeat(
        APBPlayerController* playerController,
        const char* stage)
    {
        if (!gStrictRosterPolicy || !gStrictRosterPolicy->AdmissionActive())
            return StrictRosterSeatApplyResult::Inactive;
        if (!playerController || playerController->bActorIsBeingDestroyed ||
            !playerController->PBPlayerState)
        {
            return StrictRosterSeatApplyResult::Pending;
        }

        APBPlayerState* const playerState = playerController->PBPlayerState;
        if (!IsReadableAddress(playerState, sizeof(APBPlayerState)) ||
            playerState->Role != ENetRole::ROLE_Authority)
        {
            return StrictRosterSeatApplyResult::Rejected;
        }

        std::string platformId;
        const bool hasPlatformIdentity =
            ExtractStrictRosterPlatformId(&playerState->UniqueId, platformId);
        std::optional<StrictRoster::SeatDecision> decision;
        if (hasPlatformIdentity)
            decision = gStrictRosterPolicy->ActiveDecision(platformId);

        // A listen host does not traverse remote NMT_Login/PreLogin and the
        // native PlayerState UniqueId is still empty when StartServer invokes
        // PostLogin synchronously. Bind only the exact local controller to the
        // HOST seat already authenticated by the signed allocation. If the
        // native identity has become available, it must agree with that seat.
        if (!decision && playerController == GetLocalPlayerController())
        {
            std::lock_guard<std::mutex> lock(gStrictRosterLocalHostSeatMutex);
            if (gStrictRosterLocalHostSeat &&
                (!hasPlatformIdentity ||
                    gStrictRosterLocalHostSeat->platformId == platformId))
            {
                decision = gStrictRosterLocalHostSeat;
            }
        }

        if (!decision || !decision->accepted ||
            (decision->teamId != 1 && decision->teamId != 2) ||
            decision->teamSlot < 0 || decision->logicalSlot < 0 ||
            decision->connectionGeneration < 1)
        {
            if (!hasPlatformIdentity)
                return StrictRosterSeatApplyResult::Pending;
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": no active frozen decision." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        // The exact image hash and every native byte signature are verified
        // once, before the strict authority can listen. Keep that startup
        // result latched: read-only dynamic observers install transparent
        // detours at these entries, so comparing the now-patched prologues on
        // every spawn would incorrectly fail a path already proven at boot.
        if (!gStrictRosterNativeSeatPathReady.load(std::memory_order_acquire))
        {
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": pinned Team/Camp byte gate changed." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        auto* const subsystem = USubsystemBlueprintLibrary::GetWorldSubsystem(
            playerState, UPBTeamManager::StaticClass());
        if (!subsystem || !subsystem->IsA(UPBTeamManager::StaticClass()))
            return StrictRosterSeatApplyResult::Pending;
        auto* const teamManager = static_cast<UPBTeamManager*>(subsystem);

        void* const teamInterface = reinterpret_cast<uint8*>(playerState) +
            kStrictRosterTeamInterfaceOffset;
        if (!IsReadableAddress(teamInterface, sizeof(void*)))
            return StrictRosterSeatApplyResult::Rejected;
        void** const teamVtable = *reinterpret_cast<void***>(teamInterface);
        if (!IsReadableAddress(
                teamVtable,
                kStrictRosterTeamGetterVtableOffset + sizeof(void*)) ||
            teamVtable[kStrictRosterTeamSetterVtableOffset / sizeof(void*)] !=
                reinterpret_cast<void*>(BaseAddress + kStrictRosterSetTeamRva) ||
            teamVtable[kStrictRosterTeamGetterVtableOffset / sizeof(void*)] !=
                reinterpret_cast<void*>(BaseAddress + kStrictRosterGetTeamRva))
        {
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": IGenericTeamAgentInterface vtable gate failed." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        using SetTeamFn = void(__fastcall*)(void*, const FGenericTeamId*);
        using GetTeamFn = FGenericTeamId*(__fastcall*)(
            const void*, FGenericTeamId*);
        using SetCampFn = void(__fastcall*)(APBPlayerState*, int32);
        using QueryCampFn = int32(__fastcall*)(
            UPBTeamManager*, const FGenericTeamId*);

        // The Meta contract defines public team 1 as native Solar (0) and
        // public team 2 as native Star (1). Follow the exact native sequence
        // seen in both fixed-build assignment callers: setter, getter,
        // TeamManager mapping, then the authority-only Camp setter.
        FGenericTeamId requestedNativeTeam{};
        requestedNativeTeam.TeamID = static_cast<uint8>(decision->teamId - 1);
        reinterpret_cast<SetTeamFn>(BaseAddress + kStrictRosterSetTeamRva)(
            teamInterface, &requestedNativeTeam);
        FGenericTeamId verifiedNativeTeam{};
        reinterpret_cast<GetTeamFn>(BaseAddress + kStrictRosterGetTeamRva)(
            teamInterface, &verifiedNativeTeam);
        if (verifiedNativeTeam.TeamID != requestedNativeTeam.TeamID ||
            playerState->MyTeamID.TeamID != requestedNativeTeam.TeamID)
        {
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": native Team setter/getter readback mismatch." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        const int32 nativeCamp = reinterpret_cast<QueryCampFn>(
            BaseAddress + kStrictRosterQueryCampRva)(
                teamManager, &verifiedNativeTeam);
        if (nativeCamp != static_cast<int32>(EPBCamp::Friend) &&
            nativeCamp != static_cast<int32>(EPBCamp::Enemy))
        {
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": TeamManager returned a non-playable camp." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        reinterpret_cast<SetCampFn>(BaseAddress + kStrictRosterSetCampRva)(
            playerState, nativeCamp);

        if (playerController->PBPlayerState != playerState ||
            playerState->MyTeamID.TeamID != verifiedNativeTeam.TeamID ||
            playerState->MyCampID != nativeCamp)
        {
            std::cout << "[STRICT-ROSTER] Seat application rejected at "
                << (stage ? stage : "unknown")
                << ": native Team/Camp readback mismatch." << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }

        {
            std::lock_guard<std::mutex> lock(gStrictRosterControllerMutex);
            gStrictRosterControllerSeats[playerController] = *decision;
        }
        const StrictRoster::Decision connected =
            gStrictRosterPolicy->MarkConnected(
                decision->playerId, decision->connectionGeneration);
        if (!connected.accepted)
        {
            std::lock_guard<std::mutex> lock(gStrictRosterControllerMutex);
            gStrictRosterControllerSeats.erase(playerController);
            std::cout << "[STRICT-ROSTER] Connected-seat report rejected at "
                << (stage ? stage : "unknown") << ": " << connected.code << "."
                << std::endl;
            return StrictRosterSeatApplyResult::Rejected;
        }
        std::cout << "[STRICT-ROSTER] Applied frozen seat at "
            << (stage ? stage : "unknown")
            << ": team=" << decision->teamId
            << " team_slot=" << decision->teamSlot
            << " logical_slot=" << decision->logicalSlot
            << " generation=" << decision->connectionGeneration
            << " native_team=" << static_cast<int>(verifiedNativeTeam.TeamID)
            << " native_camp=" << nativeCamp << "." << std::endl;
        return StrictRosterSeatApplyResult::Applied;
    }

    void DisconnectStrictRosterSeat(APBPlayerController* playerController)
    {
        if (!playerController || !gStrictRosterPolicy)
            return;
        std::optional<StrictRoster::SeatDecision> decision;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterControllerMutex);
            const auto found = gStrictRosterControllerSeats.find(playerController);
            if (found != gStrictRosterControllerSeats.end())
            {
                decision = found->second;
                gStrictRosterControllerSeats.erase(found);
            }
        }
        if (decision)
        {
            const StrictRoster::Decision result =
                gStrictRosterPolicy->MarkDisconnected(
                    decision->playerId, decision->connectionGeneration);
            std::cout << "[STRICT-ROSTER] Released controller binding: generation="
                << decision->connectionGeneration << " result=" << result.code
                << "." << std::endl;
        }
    }
}

void SetStrictRosterLocalHostSeat(
    const StrictRoster::SeatDecision& decision)
{
    std::lock_guard<std::mutex> lock(gStrictRosterLocalHostSeatMutex);
    if (decision.accepted && decision.teamId >= 1 && decision.teamId <= 2 &&
        decision.teamSlot >= 0 && decision.logicalSlot >= 0 &&
        decision.connectionGeneration >= 1)
    {
        gStrictRosterLocalHostSeat = decision;
    }
    else
    {
        gStrictRosterLocalHostSeat.reset();
    }
}

void ClearStrictRosterLocalHostSeat()
{
    std::lock_guard<std::mutex> lock(gStrictRosterLocalHostSeatMutex);
    gStrictRosterLocalHostSeat.reset();
}

// Retained for generated ProcessEvent calls made by server-side loadout
// serialization/application helpers. The production client never constructs
// LoadoutManager; this guard does not maintain a client archive mirror.
static thread_local unsigned int gClientProcessEventSuppressionDepth = 0;

extern "C" void PayloadPushClientProcessEventSuppression()
{
    ++gClientProcessEventSuppressionDepth;
}

extern "C" void PayloadPopClientProcessEventSuppression()
{
    if (gClientProcessEventSuppressionDepth > 0)
        --gClientProcessEventSuppressionDepth;
}

static std::uint64_t ListenHostRecoveryGeneration = 0;
static std::unordered_set<APBPlayerController*>
    SynthesizedListenHostControllers;
static std::unordered_set<APBPlayerController*>
    ListenHostRoleConfirmationAttempts;

static void SynchronizeListenHostRecoveryGeneration()
{
    const std::uint64_t generation = GetServerMatchGeneration();
    if (ListenHostRecoveryGeneration == generation)
        return;

    ListenHostRecoveryGeneration = generation;
    SynthesizedListenHostControllers.clear();
    ListenHostRoleConfirmationAttempts.clear();
}

// NumExpectedPlayers can be established or changed after one or more role
// confirmations have already arrived. Keep the start gate derived from the
// authoritative confirmation set whenever either side of the quorum changes.
// Before StartMatch this may also move true -> false when a new initial player
// joins and raises the expected count.
static void RecomputeMatchStartGate(const char* reason)
{
    NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
    if (DidProcStartMatch)
        return;

    const bool hasQuorum = NumExpectedPlayers > 0 &&
        NumPlayersSelectedRole >= NumExpectedPlayers;
    if (canStartMatch != hasQuorum)
    {
        std::cout << "[MATCH] Start gate " << (hasQuorum ? "ready" : "waiting")
                  << " after " << (reason ? reason : "state update")
                  << " (" << NumPlayersSelectedRole << "/" << NumExpectedPlayers << ")"
                  << std::endl;
    }
    canStartMatch = hasQuorum;
    if (hasQuorum)
        StartMatchTimer = -1.0f;
}

static bool IsCurrentGameStatePlayer(
    UWorld* const world,
    APBPlayerController* const playerController)
{
    if (!world || !world->GameState ||
        !world->GameState->IsA(AGameState::StaticClass()) ||
        !playerController || !playerController->PlayerState)
    {
        return false;
    }
    auto* const gameState = static_cast<AGameState*>(world->GameState);
    for (APlayerState* const playerState : gameState->PlayerArray)
    {
        if (playerState == playerController->PlayerState)
            return true;
    }
    return false;
}

static bool RegisterAuthoritativeMatchParticipant(
    AGameMode* const gameMode,
    APBPlayerController* const playerController,
    const bool synthesizedListenHost)
{
    if (!gameMode || !playerController ||
        DisconnectedPlayerControllers.contains(playerController) ||
        playerController->bActorIsBeingDestroyed)
    {
        return false;
    }
    try
    {
        if (!playerController->HasAuthority())
            return false;
    }
    catch (...)
    {
        return false;
    }
    if (ConnectedPlayerControllers.contains(playerController))
        return true;

    const StrictRosterSeatApplyResult strictSeatResult =
        ApplyStrictRosterSeat(playerController,
            synthesizedListenHost ? "ListenHostRecovery" : "PostLogin");
    if (strictSeatResult == StrictRosterSeatApplyResult::Rejected)
    {
        std::cout << "[STRICT-ROSTER] Participant kept non-playable after a "
                     "frozen-seat rejection." << std::endl;
        return false;
    }
    if (strictSeatResult == StrictRosterSeatApplyResult::Pending)
    {
        std::cout << "[STRICT-ROSTER] Participant seat data is pending; "
                     "RestartPlayer remains fail-closed." << std::endl;
    }

    ConnectedPlayerControllers.insert(playerController);
    NumPlayersJoined = static_cast<int>(ConnectedPlayerControllers.size());
    if (synthesizedListenHost)
    {
        SynthesizedListenHostControllers.insert(playerController);
        std::cout << "[LISTEN] Registered the current local host in the match "
                     "quorum after world activation."
                  << std::endl;
    }
    else
    {
        std::cout << "Player Connected!" << std::endl;
    }

    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        if (gLoadoutManager)
            gLoadoutManager->OnPlayerConnected(playerController);
    }

    if (gLateJoinManager &&
        gLateJoinManager->OnPostLogin(gameMode, playerController))
    {
        return true;
    }

    if (!DidProcStartMatch && NumExpectedPlayers > 0)
    {
        NumExpectedPlayers = NumPlayersJoined;
        RecomputeMatchStartGate("initial player connected");
    }

    if (gLateJoinManager)
    {
        gLateJoinManager->QueueInitialJoinPlayer(
            gameMode, playerController);
        return true;
    }

    if (playerController->Pawn)
        playerController->ServerSuicide(0);
    return true;
}

static void EnsureListenHostMatchParticipant(UWorld* const world)
{
    SynchronizeListenHostRecoveryGeneration();
    if (!amListenServer || !world || !world->AuthorityGameMode ||
        !world->OwningGameInstance)
    {
        return;
    }

    using GetLocalPlayerFn = void*(__fastcall*)(APlayerController*);
    const auto getLocalPlayer =
        reinterpret_cast<GetLocalPlayerFn>(BaseAddress + 0x34FB080);
    for (UObject* const object :
        getObjectsOfClass(APBPlayerController::StaticClass(), false))
    {
        auto* const playerController =
            object ? static_cast<APBPlayerController*>(object) : nullptr;
        const bool currentWorldPlayer = IsCurrentGameStatePlayer(
            world, playerController);
        const bool alreadyRegistered = playerController &&
            ConnectedPlayerControllers.contains(playerController);
        if (!playerController || !currentWorldPlayer || alreadyRegistered ||
            playerController->bActorIsBeingDestroyed ||
            playerController->PBGameInstance != world->OwningGameInstance)
        {
            continue;
        }
        const bool hasLocalPlayer = getLocalPlayer(playerController) != nullptr;
        if (!ServerHookPolicy::ShouldRegisterListenHostParticipant(
                true, currentWorldPlayer, hasLocalPlayer,
                alreadyRegistered))
        {
            continue;
        }
        RegisterAuthoritativeMatchParticipant(
            static_cast<AGameMode*>(world->AuthorityGameMode),
            playerController,
            true);
        return;
    }
}

static void CleanupDisconnectedPlayer(APBPlayerController* playerController, const char* reason)
{
    if (!playerController)
        return;

    // Tombstone first: teardown can re-enter PostLogin/role hooks before the
    // native destroy/logout call has fully unwound.
    DisconnectedPlayerControllers.insert(playerController);
    DisconnectStrictRosterSeat(playerController);

    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        if (gLoadoutManager)
            gLoadoutManager->OnPlayerDisconnected(playerController);
    }
    if (gLateJoinManager)
        gLateJoinManager->OnPlayerDisconnected(playerController);
    DedicatedMultiMatch::OnPlayerDisconnected(playerController);

    PlayerRespawnAllowedMap.erase(playerController);
    PlayersConfirmedRole.erase(playerController);
    SynthesizedListenHostControllers.erase(playerController);
    ListenHostRoleConfirmationAttempts.erase(playerController);
    PendingNameUpdatePlayers.erase(playerController);
    AppliedNameUpdatePlayers.erase(playerController);
    ConnectedPlayerControllers.erase(playerController);
    NumPlayersJoined = static_cast<int>(ConnectedPlayerControllers.size());
    NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
    if (!DidProcStartMatch && NumExpectedPlayers > NumPlayersJoined)
        NumExpectedPlayers = NumPlayersJoined;
    RecomputeMatchStartGate(reason);
}

static bool IsCurrentConnectedController(APBPlayerController* playerController)
{
    return playerController &&
        ConnectedPlayerControllers.contains(playerController) &&
        !DisconnectedPlayerControllers.contains(playerController) &&
        !playerController->bActorIsBeingDestroyed;
}

static bool IsCurrentSelectedRole(
    APBPlayerController* playerController,
    const FName& roleId)
{
    if (!IsCurrentConnectedController(playerController) ||
        !playerController->PBPlayerState)
    {
        return false;
    }

    APBPlayerState* const expectedPlayerState = playerController->PBPlayerState;
    bool hasSelectedRole = false;
    try { hasSelectedRole = expectedPlayerState->HasSelectedRole(); }
    catch (...) { return false; }

    if (!IsCurrentConnectedController(playerController) ||
        playerController->PBPlayerState != expectedPlayerState ||
        !hasSelectedRole)
    {
        return false;
    }

    const std::string selected = expectedPlayerState->SelectedCharacterID.ToString();
    const std::string submitted = roleId.ToString();
    return !selected.empty() && selected != "None" &&
        !submitted.empty() && submitted != "None" && selected == submitted;
}

static void RecoverListenHostRoleConfirmation(UWorld* const world)
{
    SynchronizeListenHostRecoveryGeneration();
    if (!amListenServer || !world || !world->OwningGameInstance ||
        !gLateJoinManager || !DidBroadcastRoleSelection)
    {
        return;
    }

    const std::vector<APBPlayerController*> listenHosts(
        SynthesizedListenHostControllers.begin(),
        SynthesizedListenHostControllers.end());
    for (APBPlayerController* const playerController : listenHosts)
    {
        const bool currentWorldPlayer =
            IsCurrentConnectedController(playerController) &&
            IsCurrentGameStatePlayer(world, playerController) &&
            playerController->PBGameInstance == world->OwningGameInstance;
        const bool initialJoin = currentWorldPlayer &&
            gLateJoinManager->IsInitialJoinPlayer(playerController);
        const bool alreadyConfirmed = playerController &&
            PlayersConfirmedRole.contains(playerController);
        const bool alreadyAttempted = playerController &&
            ListenHostRoleConfirmationAttempts.contains(playerController);

        APBPlayerState* playerState = currentWorldPlayer
            ? playerController->PBPlayerState
            : nullptr;
        bool hasSelectedRole = false;
        FName selectedRole{};
        std::string selectedRoleId;
        if (playerState)
        {
            try
            {
                hasSelectedRole = playerState->HasSelectedRole();
                selectedRole = playerState->SelectedCharacterID;
                selectedRoleId = selectedRole.ToString();
            }
            catch (...)
            {
                hasSelectedRole = false;
                selectedRoleId.clear();
            }
        }

        const bool stillCurrent = currentWorldPlayer &&
            IsCurrentConnectedController(playerController) &&
            IsCurrentGameStatePlayer(world, playerController) &&
            playerController->PBPlayerState == playerState &&
            playerController->PBGameInstance == world->OwningGameInstance;
        const bool concreteRole = !selectedRoleId.empty() &&
            selectedRoleId != "None";
        if (!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
                true,
                SynthesizedListenHostControllers.contains(playerController),
                stillCurrent,
                initialJoin,
                DidBroadcastRoleSelection,
                alreadyConfirmed,
                alreadyAttempted,
                hasSelectedRole,
                concreteRole))
        {
            continue;
        }

        // Mark before entering ProcessEvent: the generated RPC wrapper is
        // synchronous and re-enters this module's role-confirmation hook.
        ListenHostRoleConfirmationAttempts.insert(playerController);
        std::cout << "[LISTEN] Replaying current local host role confirmation: role="
                  << selectedRoleId << std::endl;
        playerController->ServerConfirmRoleSelection(selectedRole);
    }
}

// ======================================================
//  SECTION 7 — HOOK DETOURS (ENGINE HOOKS)
// ======================================================

static SafetyHookInline TickFlush = {};
static SafetyHookInline EngineBrowse = {};
static SafetyHookInline DeferredTravelGameEngineTick = {};
static SafetyHookInline WorldSeamlessTravel = {};
static SafetyHookInline ScriptMulticastDelegateProcess = {};
static std::once_flag TravelDeferralHooksOnce;
static thread_local unsigned int GameEngineTickDepth = 0;
static std::uint64_t MatchIntroFlushGeneration = 0;
static unsigned int CompletedNativeMatchIntroFlushes = 0;
static bool LoggedPendingNativeMatchIntroFlush = false;

namespace
{
    struct DeferredSeamlessTravel
    {
        bool Pending = false;
        SDK::UWorld* World = nullptr;
        std::wstring Url;
        bool Absolute = false;
        SDK::FGuid PackageGuid{};
        bool HasPackageGuid = false;
    };

    std::mutex DeferredSeamlessTravelMutex;
    DeferredSeamlessTravel DeferredTravel;
    // Null-owner delegates from a retired source world have now fired more
    // than five minutes after ClientTravel in the pinned client. Their exact
    // low addresses can never be valid UObject/delegate storage, so once an
    // explicitly marked multi-match travel arms this compatibility guard,
    // keep the fixed-build exact delegate allow-list active for the process
    // session (including the role-selection RoundState member at +0x2C0).
    // The broader PlayerState/GameInstance fallbacks retain a bounded window.
    constexpr ULONGLONG OwnedTravelCompatibilityGuardDurationMs = 300000;
    std::atomic<ULONGLONG> OwnedTravelCompatibilityGuardDeadlineMs = 0;
    std::atomic<bool> OwnedTravelDelegateGuardArmed = false;
    std::atomic<bool> InvalidTravelDelegateSuppressionLogged = false;

    void ArmOwnedTravelDelegateGuard()
    {
        OwnedTravelDelegateGuardArmed.store(true, std::memory_order_release);
        OwnedTravelCompatibilityGuardDeadlineMs.store(
            ::GetTickCount64() + OwnedTravelCompatibilityGuardDurationMs,
            std::memory_order_release);
        InvalidTravelDelegateSuppressionLogged.store(false, std::memory_order_release);
        std::cout << "[MULTIMATCH_TRACE] pinned-invalid-travel-delegate="
                     "armed lifetime=multi-match-session compatibility_ms="
                  << OwnedTravelCompatibilityGuardDurationMs
                  << std::endl;
    }

    bool IsOwnedTravelDelegateGuardActive()
    {
        return OwnedTravelDelegateGuardArmed.load(std::memory_order_acquire);
    }

    bool IsOwnedTravelCompatibilityWindowActive()
    {
        const ULONGLONG deadline = OwnedTravelCompatibilityGuardDeadlineMs.load(
            std::memory_order_acquire);
        return deadline != 0 && ::GetTickCount64() <= deadline;
    }

    bool IsOwnedMultiMatchTravelUrl(const SDK::FString* url)
    {
        if (!url || url->Num() <= 1)
            return false;
        try
        {
            return DedicatedMultiMatchPolicy::IsOwnedTravelUrl(url->ToString());
        }
        catch (...)
        {
            return false;
        }
    }

    void DispatchDeferredSeamlessTravel()
    {
        DeferredSeamlessTravel pending;
        {
            std::lock_guard<std::mutex> lock(DeferredSeamlessTravelMutex);
            if (!DeferredTravel.Pending)
                return;
            pending = std::move(DeferredTravel);
            DeferredTravel = DeferredSeamlessTravel{};
        }

        if (!pending.World || pending.World != SDK::UWorld::GetWorld() ||
            pending.Url.empty())
        {
            std::cout << "[MULTIMATCH_TRACE] post-engine-tick seamless-travel=discarded "
                         "reason=source-world-changed"
                      << std::endl;
            return;
        }

        SDK::FString url(pending.Url.c_str());
        const SDK::FGuid* const packageGuid = pending.HasPackageGuid
            ? &pending.PackageGuid
            : nullptr;
        WorldSeamlessTravel.call<void>(
            pending.World, &url, pending.Absolute, packageGuid);
        std::cout << "[MULTIMATCH_TRACE] post-engine-tick seamless-travel=dispatched"
                  << std::endl;
    }
}

void ScriptMulticastDelegateProcessHook(void* delegateThis, void* parameters)
{
    const bool ownedTravelWindow = IsOwnedTravelDelegateGuardActive();
    if (DedicatedMultiMatchPolicy::ShouldSuppressInvalidTravelDelegate(
            reinterpret_cast<std::uintptr_t>(delegateThis), ownedTravelWindow))
    {
        if (!InvalidTravelDelegateSuppressionLogged.exchange(
                true, std::memory_order_acq_rel))
        {
            std::cout << "[MULTIMATCH_TRACE] pinned-invalid-travel-delegate="
                         "suppressed this=0x"
                      << std::hex << reinterpret_cast<std::uintptr_t>(delegateThis)
                      << std::dec << std::endl;
        }
        return;
    }

    ScriptMulticastDelegateProcess.call<void>(delegateThis, parameters);
}

void WorldSeamlessTravelHook(
    SDK::UWorld* world,
    const SDK::FString* url,
    bool absolute,
    const SDK::FGuid* packageGuid)
{
    const bool ownedTravel = world && IsOwnedMultiMatchTravelUrl(url);
    if (ownedTravel)
        ArmOwnedTravelDelegateGuard();

    if (GameEngineTickDepth == 0 || !ownedTravel)
    {
        WorldSeamlessTravel.call<void>(world, url, absolute, packageGuid);
        return;
    }

    std::lock_guard<std::mutex> lock(DeferredSeamlessTravelMutex);
    if (!DeferredTravel.Pending)
    {
        DeferredTravel.Pending = true;
        DeferredTravel.World = world;
        DeferredTravel.Url = std::wstring(url->CStr());
        DeferredTravel.Absolute = absolute;
        DeferredTravel.HasPackageGuid = packageGuid != nullptr;
        if (packageGuid)
            DeferredTravel.PackageGuid = *packageGuid;
        std::cout << "[MULTIMATCH_TRACE] in-engine-tick seamless-travel=deferred"
                  << std::endl;
        return;
    }

    const bool duplicate = DeferredTravel.World == world &&
        DeferredTravel.Url == std::wstring(url->CStr());
    std::cout << "[MULTIMATCH_TRACE] in-engine-tick seamless-travel="
              << (duplicate ? "duplicate-suppressed" : "collision-suppressed")
              << std::endl;
}

void DeferredTravelGameEngineTickHook(
    void* gameEngine,
    float deltaSeconds,
    bool idleMode)
{
    ++GameEngineTickDepth;
    DeferredTravelGameEngineTick.call<void>(gameEngine, deltaSeconds, idleMode);
    --GameEngineTickDepth;
    if (GameEngineTickDepth == 0)
    {
        DispatchDeferredSeamlessTravel();
        DedicatedMultiMatch::OnGameEnginePostTick();
    }
}

void InitTravelDeferralHooks()
{
    std::call_once(TravelDeferralHooksOnce, []() {
        // Pinned UGameEngine vtable +0x2B0 and UWorld::SeamlessTravel. The
        // marker gate leaves every native/P2P URL on the original path; only
        // the explicit dedicated multi-match URL is deferred beyond the old
        // World's final UWorld::Tick/FTimerManager pass.
        WorldSeamlessTravel = safetyhook::create_inline(
            (void*)(BaseAddress + 0x36D3D10), WorldSeamlessTravelHook);
        // Pinned TMulticastScriptDelegate::ProcessMulticastDelegate. During
        // marked seamless travel only, the fixed game image can invoke it with
        // the proven null-owner +0x2C0/+0x300/+0x310/+0x3B0/+0x3C0 member
        // addresses.
        // The hook otherwise forwards every call unchanged on both peers.
        ScriptMulticastDelegateProcess = safetyhook::create_inline(
            (void*)(BaseAddress + 0x8FB1B0), ScriptMulticastDelegateProcessHook);
        DeferredTravelGameEngineTick = safetyhook::create_inline(
            (void*)(BaseAddress + 0x32683F0), DeferredTravelGameEngineTickHook);
    });
}

int EngineBrowseHook(
    void* gameEngine,
    void* worldContext,
    void* url,
    SDK::FString* error)
{
    const auto result = DedicatedMultiMatch::InterceptEngineBrowse(worldContext);
    if (result == DedicatedMultiMatch::EngineBrowseInterceptResult::HandledSuccess)
    {
        // Pinned EBrowseReturnVal::Success.
        return 0;
    }
    if (result == DedicatedMultiMatch::EngineBrowseInterceptResult::HandledFailure)
    {
        // Pinned EBrowseReturnVal::Failure. TickWorldTravel will clear its
        // queued URL; the next server tick performs the normal fallback exit.
        return 1;
    }
    return EngineBrowse.call<int>(gameEngine, worldContext, url, error);
}

void TickFlushHook(UNetDriver *NetDriver, float DeltaTime)
{
    if (listening && NetDriver && UWorld::GetWorld())
    {
        UWorld* const currentWorld = UWorld::GetWorld();
        NetDriverAccess::Observe(NetDriver, currentWorld, NetDriverAccess::Source::HookArgument);
        EnsureServerMatchWorld(currentWorld);
        EnsureListenHostMatchParticipant(currentWorld);
        RefreshServerStatusSnapshot();

        if (PlayerJoinTimerSelectFuck > 0.0f)
        {
            PlayerJoinTimerSelectFuck -= DeltaTime;

            if (PlayerJoinTimerSelectFuck <= 0.0f)
            {
                DidBroadcastRoleSelection = true;

                std::vector<APBPlayerController*> rolePromptControllers(
                    ConnectedPlayerControllers.begin(),
                    ConnectedPlayerControllers.end());
                for (APBPlayerController* playerController : rolePromptControllers)
                {
                    if (!playerController ||
                        !ConnectedPlayerControllers.contains(playerController) ||
                        DisconnectedPlayerControllers.contains(playerController) ||
                        playerController->bActorIsBeingDestroyed)
                    {
                        continue;
                    }

                    if (gLateJoinManager &&
                        gLateJoinManager->ShouldDeferInitialRoleSelectionPrompt(playerController))
                    {
                        std::cout << "[LATEJOIN] Deferring initial role-selection prompt until client match-state sync."
                                  << std::endl;
                        continue;
                    }

                    const bool canSelectRole = playerController->CanSelectRole();
                    if (!ConnectedPlayerControllers.contains(playerController) ||
                        DisconnectedPlayerControllers.contains(playerController) ||
                        playerController->bActorIsBeingDestroyed)
                    {
                        continue;
                    }
                    if (canSelectRole)
                    {
                        std::cout << "Selecting role..." << std::endl;
                        playerController->ClientSelectRole();
                        if (gLateJoinManager &&
                            ConnectedPlayerControllers.contains(playerController) &&
                            !DisconnectedPlayerControllers.contains(playerController))
                        {
                            gLateJoinManager->OnRoleSelectionPromptSent(playerController);
                        }
                    }
                    else
                    {
                        std::cout << "CANT SELECT ROLE WEE WOO WEE WOO" << std::endl;
                    }
                }
            }
        }

        std::vector<LibReplicate::FActorInfo> ActorInfos = std::vector<LibReplicate::FActorInfo>();
        std::vector<UNetConnection *> Connections = std::vector<UNetConnection *>();
        std::vector<void *> PlayerControllers = std::vector<void *>();

        for (UNetConnection *Connection : NetDriver->ClientConnections)
        {
            if (Connection->OwningActor)
            {
                Connection->ViewTarget = Connection->PlayerController ? Connection->PlayerController->GetViewTarget() : Connection->OwningActor;
                Connections.push_back(Connection);
            }
        }

        for (int i = 0; i < UWorld::GetWorld()->Levels.Num(); i++)
        {
            ULevel *Level = UWorld::GetWorld()->Levels[i];

            if (Level)
            {
                for (int j = 0; j < Level->Actors.Num(); j++)
                {
                    AActor *actor = Level->Actors[j];

                    if (!actor)
                        continue;

                    if (actor->RemoteRole == ENetRole::ROLE_None)
                        continue;

                    if (!actor->bReplicates)
                        continue;

                    if (actor->bActorIsBeingDestroyed)
                        continue;

                    if (actor->Class == APlayerController_BP_C::StaticClass())
                    {
                        PlayerControllers.push_back((void *)actor);
                        if (((APlayerController *)actor)->Character && ((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass()))
                        {
                            ((UCharacterMovementComponent *)(((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bIgnoreClientMovementErrorChecksAndCorrection = true;
                            ((UCharacterMovementComponent *)(((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bServerAcceptClientAuthoritativePosition = true;
                        }
                        continue;
                    }

                    ActorInfos.push_back(LibReplicate::FActorInfo(actor, actor->bNetTemporary));
                }
            }
        }

        std::vector<LibReplicate::FPlayerControllerInfo> PlayerControllerInfos = std::vector<LibReplicate::FPlayerControllerInfo>();

        for (void *PlayerController : PlayerControllers)
        {
            for (UNetConnection *Connection : Connections)
            {
                if (Connection->PlayerController == PlayerController)
                {
                    PlayerControllerInfos.push_back(LibReplicate::FPlayerControllerInfo(Connection, PlayerController));
                    break;
                }
            }
        }

        std::vector<void *> CastConnections = std::vector<void *>();

        for (UNetConnection *Connection : Connections)
        {
            CastConnections.push_back((void *)Connection);
        }

        static FName *ActorName = nullptr;

        if (!ActorName)
        {
            ActorName = new FName();
            ActorName->ComparisonIndex = UKismetStringLibrary::Conv_StringToName(L"Actor").ComparisonIndex;
            ActorName->Number = UKismetStringLibrary::Conv_StringToName(L"Actor").Number;
        }

        if (ActorInfos.size() > 0 && CastConnections.size() > 0)
        {
            if (NetDriver)
            {
                libReplicate->CallFromTickFlushHook(ActorInfos, PlayerControllerInfos, CastConnections, ActorName, NetDriver);

                int *counter = reinterpret_cast<int *>(reinterpret_cast<char *>(NetDriver) + 0x420);
                *counter = *counter + 1;
            }
        }

        // Consume completed HTTP work and replay any role confirmation whose
        // bounded loadout grace period has completed before LateJoin attempts
        // to create a Pawn this frame.
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager)
                gLoadoutManager->TickServer(DeltaTime);
        }

        // Drive LateJoin state machine
        if (gLateJoinManager)
            gLateJoinManager->Tick(DeltaTime);

        // A listen host can already own a selected role when the shared prompt
        // opens. In that state the client emits no new role-confirm RPC, so
        // replay the existing role through the full authoritative path once.
        RecoverListenHostRoleConfirmation(currentWorld);
    }

    APBGameState *CurrentGameState = GetPBGameState();
    if (CurrentGameState && !CurrentGameState->IsRoundInProgress())
    {
        if (CurrentGameState->RoundState.ToString().contains("InvalidState"))
        {

            if (NumPlayersJoined >= Config.MinPlayersToStart)
            {
                if (!DidProcFlow)
                {
                    if (MatchStartCountdown == -1.0f)
                    {
                        MatchStartCountdown = 30.0f;

                        NumExpectedPlayers = NumPlayersJoined;
                        RecomputeMatchStartGate("countdown initialized");
                    }
                    else
                    {
                        MatchStartCountdown -= DeltaTime;

                        if (NumExpectedPlayers > NumPlayersJoined)
                        {
                            NumExpectedPlayers = NumPlayersJoined;
                            RecomputeMatchStartGate("player count decreased");

                            MatchStartCountdown += 15.0f;
                        }

                        if (MatchStartCountdown <= 0.0f)
                        {
                            DidProcFlow = true;

                            std::cout << "All players connected, beginning role selection flow!" << std::endl;

                            PlayerJoinTimerSelectFuck = 5.0f;

                            NumExpectedPlayers = NumPlayersJoined;
                            RecomputeMatchStartGate("role selection opened");
                        }
                    }
                }
            }
        }

        if (CurrentGameState->RoundState.ToString().contains("CountdownToStart"))
        {

            for (UNetConnection *pc : NetDriver->ClientConnections)
            {
                if (pc->PlayerController && pc->PlayerController->Pawn)
                    pc->PlayerController->Possess(pc->PlayerController->Pawn);
            }
        }
    }

    UWorld* const currentWorld = UWorld::GetWorld();
    APBGameMode* const currentGameMode = currentWorld &&
            currentWorld->AuthorityGameMode
        ? static_cast<APBGameMode*>(currentWorld->AuthorityGameMode)
        : nullptr;
    const std::uint64_t currentMatchGeneration = GetServerMatchGeneration();
    if (MatchIntroFlushGeneration != currentMatchGeneration)
    {
        MatchIntroFlushGeneration = currentMatchGeneration;
        CompletedNativeMatchIntroFlushes = 0;
        LoggedPendingNativeMatchIntroFlush = false;
    }
    const bool initialPlayersReady = !gLateJoinManager ||
        gLateJoinManager->AreInitialPlayersReadyForStart();
    const bool matchIntroEntered = currentGameMode &&
        currentGameMode->MatchSubState.ToString().contains("MatchIntro");
    const bool awaitingNativeMatchIntroFlush = canStartMatch &&
        !DidProcStartMatch && initialPlayersReady && matchIntroEntered &&
        CompletedNativeMatchIntroFlushes == 0;
    if (awaitingNativeMatchIntroFlush)
    {
        if (CurrentGameState)
            CurrentGameState->ForceNetUpdate();
        if (!LoggedPendingNativeMatchIntroFlush)
        {
            LoggedPendingNativeMatchIntroFlush = true;
            std::cout << "[MATCH] Native MatchIntro observed; preserving one "
                         "complete NetDriver flush before StartMatch."
                      << std::endl;
        }
    }
    if (JoinUiSyncPolicy::ShouldDispatchStartMatch(
            canStartMatch,
            DidProcStartMatch,
            initialPlayersReady,
            matchIntroEntered,
            CompletedNativeMatchIntroFlushes > 0))
    {
        DidProcStartMatch = true;
        std::cout << "[MATCH] Replicated native MatchIntro boundary completed; "
                     "dispatching StartMatch."
                  << std::endl;
        currentGameMode->StartMatch();
    }

    static bool wasF8Down = false;
    const SHORT f8State = GetAsyncKeyState(VK_F8);
    const bool isF8Down = (f8State & 0x8000) != 0;
    const bool wasF8PressedSinceLastPoll = (f8State & 0x0001) != 0;
    if ((wasF8PressedSinceLastPoll || (isF8Down && !wasF8Down)) && amServer)
    {
        // Debug-only local PVE death trigger. UObject history contains retired
        // and AI controllers; invoking RPCs on all of them can terminate the
        // dedicated process. Restrict the trigger to the authoritative current
        // connection snapshot used by the production lifecycle hooks.
        const std::vector<APBPlayerController*> currentPlayers(
            ConnectedPlayerControllers.begin(), ConnectedPlayerControllers.end());
        for (APBPlayerController* playerController : currentPlayers)
        {
            if (IsCurrentConnectedController(playerController))
                playerController->ServerSuicide(0);
        }
    }
    wasF8Down = isF8Down;

    // Native TickFlush must finish before a queued multi-match seamless travel
    // is allowed to replace World/NetDriver state. Starting the seamless
    // handler from the pre-flush half of this detour can migrate channels while
    // the driver is still on its own flush stack and strand the game thread.
    TickFlush.call(NetDriver, DeltaTime);
    UWorld* const postFlushWorld = UWorld::GetWorld();
    APBGameMode* const postFlushGameMode = postFlushWorld &&
            postFlushWorld->AuthorityGameMode
        ? static_cast<APBGameMode*>(postFlushWorld->AuthorityGameMode)
        : nullptr;
    const bool stillAwaitingStartAfterFlush =
        MatchIntroFlushGeneration == GetServerMatchGeneration() &&
        postFlushWorld == currentWorld &&
        postFlushGameMode == currentGameMode &&
        canStartMatch && !DidProcStartMatch &&
        (!gLateJoinManager || gLateJoinManager->AreInitialPlayersReadyForStart()) &&
        postFlushGameMode &&
        postFlushGameMode->MatchSubState.ToString().contains("MatchIntro");
    if (stillAwaitingStartAfterFlush && CompletedNativeMatchIntroFlushes == 0)
    {
        CompletedNativeMatchIntroFlushes = 1;
        std::cout << "[MATCH] Completed native MatchIntro NetDriver flush."
                  << std::endl;
    }
    if (listening && NetDriver && UWorld::GetWorld())
        DedicatedMultiMatch::Tick(DeltaTime, NetDriver);
}

// ======================================================
//  SECTION 8 — HOOK DETOURS (GAMEPLAY HOOKS)
// ======================================================

static SafetyHookInline NotifyActorDestroyed = {};

// APBPlayerController's match-ending implementation always tears down the
// local InGameMenu before it forwards to the engine ClientGameEnded path. A
// headless server-side controller has no local UI root at +0xF8, but the pinned
// build still dereferences that pointer at RVA 0x015C8D0E. Keep the native
// match lifecycle intact and skip only this impossible UI operation.
static SafetyHookInline ServerInGameMenuTransition = {};
static SafetyHookInline ServerEndMatch = {};
static SafetyHookMid ServerNullResultMvp = {};
static SafetyHookInline ServerConfirmRoleSelectionValidate = {};
static SafetyHookInline ServerRoleConfirmationRestartPlayer = {};
static SafetyHookInline ServerStartShowingMatchResult = {};
static SafetyHookInline ServerStartWaitingToEndGame = {};
static SafetyHookInline ServerBeginFinalCleanup = {};

static thread_local APBPlayerController*
    gLiveRoleConfirmationController = nullptr;
static thread_local RespawnStatePolicy::LiveRoleConfirmationAction
    gLiveRoleConfirmationAction =
        RespawnStatePolicy::LiveRoleConfirmationAction::NativeConfirmAndRestart;
static thread_local bool gRoleConfirmationRestartWasSuppressed = false;

class ScopedLiveRoleConfirmationRestartPolicy
{
public:
    ScopedLiveRoleConfirmationRestartPolicy(
        APBPlayerController* playerController,
        RespawnStatePolicy::LiveRoleConfirmationAction action)
        : PreviousController(gLiveRoleConfirmationController)
        , PreviousAction(gLiveRoleConfirmationAction)
        , PreviousRestartWasSuppressed(
            gRoleConfirmationRestartWasSuppressed)
    {
        gLiveRoleConfirmationController = playerController;
        gLiveRoleConfirmationAction = action;
        gRoleConfirmationRestartWasSuppressed = false;
    }

    ~ScopedLiveRoleConfirmationRestartPolicy()
    {
        gLiveRoleConfirmationController = PreviousController;
        gLiveRoleConfirmationAction = PreviousAction;
        gRoleConfirmationRestartWasSuppressed =
            PreviousRestartWasSuppressed;
    }

    bool WasRestartSuppressed() const
    {
        return gRoleConfirmationRestartWasSuppressed;
    }

private:
    APBPlayerController* PreviousController = nullptr;
    RespawnStatePolicy::LiveRoleConfirmationAction PreviousAction =
        RespawnStatePolicy::LiveRoleConfirmationAction::NativeConfirmAndRestart;
    bool PreviousRestartWasSuppressed = false;
};

__int64 ServerInGameMenuTransitionHook(APBPlayerController* playerController, bool opening)
{
    constexpr uintptr_t LocalUiRootOffset = 0xF8;
    if (!playerController ||
        !*reinterpret_cast<void**>(
            reinterpret_cast<uintptr_t>(playerController) + LocalUiRootOffset))
    {
        static std::atomic_bool logged = false;
        if (!logged.exchange(true))
        {
            std::cout << "[MATCH] Skipped headless InGameMenu transition; "
                         "continuing native ClientGameEnded lifecycle."
                      << std::endl;
        }
        return 0;
    }

    return ServerInGameMenuTransition.call<__int64>(playerController, opening);
}

void ServerEndMatchHook(APBGameMode* gameMode)
{
    if (DedicatedMultiMatch::ShouldSuppressRetiredEndMatch(gameMode))
    {
        std::cout << "[MULTIMATCH] Suppressed stale/retired GameMode native "
                     "EndMatch/result-freeze."
                  << std::endl;
        return;
    }
    ServerEndMatch.call<void>(gameMode);
}

void ServerNullResultMvpHook(SafetyHookContext& context)
{
    if (context.rdi != 0)
        return;

    auto* const gameMode = reinterpret_cast<APBGameMode*>(context.rbx);
    if (!DedicatedMultiMatch::ShouldBypassNullResultMvp(gameMode))
        return;

    // APBGameMode's pinned result freezer obtains the selected MVP from
    // GameState+0x428, then calls the player-state helper at 0x16728C0 without
    // checking it. Continue at the native common path used when that helper
    // reports no work; preserve later result-state/ShowingResult dispatches.
    context.rip = BaseAddress + 0x163791C;
    std::cout << "[MULTIMATCH] Skipped null MVP decoration; continuing native "
                 "result lifecycle."
              << std::endl;
}

bool ServerConfirmRoleSelectionValidateHook(
    APBPlayerController* playerController,
    const FName* roleId)
{
    bool bypass = false;
    if (playerController && roleId)
    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        bypass = gLoadoutManager &&
            gLoadoutManager->ShouldBypassSeamlessRoleValidator(
                playerController, *roleId);
    }
    if (bypass)
    {
        std::cout << "[MULTIMATCH] Bypassed transient destination role-set "
                     "validator for seeded seamless confirmation: role="
                  << roleId->ToString() << std::endl;
        return true;
    }

    return ServerConfirmRoleSelectionValidate.call<bool>(
        playerController, roleId);
}

void ServerRoleConfirmationRestartPlayerHook(
    APBGameMode* gameMode,
    AController* newPlayer)
{
    // Fixed-build APBGameMode::RestartPlayer checks this int32 at
    // RVA 0x0163D2D9. A non-zero value selects the native deferred-respawn
    // branch, which notifies the client and appends the controller to the
    // +0x430 queue instead of spawning a Pawn.
    constexpr uintptr_t RespawnQueueModeOffset = 0x428;
    APBPlayerController* playerController =
        newPlayer && newPlayer->IsA(APBPlayerController::StaticClass())
            ? static_cast<APBPlayerController*>(newPlayer)
            : nullptr;
    const StrictRosterSeatApplyResult strictSeatResult =
        ApplyStrictRosterSeat(playerController, "RestartPlayer");
    if (strictSeatResult == StrictRosterSeatApplyResult::Pending ||
        strictSeatResult == StrictRosterSeatApplyResult::Rejected)
    {
        gRoleConfirmationRestartWasSuppressed = true;
        std::cout << "[STRICT-ROSTER] Suppressed RestartPlayer because the "
                     "frozen native seat is not verified; result="
                  << (strictSeatResult == StrictRosterSeatApplyResult::Pending
                        ? "pending" : "rejected")
                  << "." << std::endl;
        return;
    }
    const bool sameController = playerController &&
        playerController == gLiveRoleConfirmationController;
    const bool controllerStillHasPawn =
        playerController && playerController->Pawn;
    const bool nativeRestartWouldEnterRespawnQueue = gameMode &&
        *reinterpret_cast<const int32*>(
            reinterpret_cast<const uint8*>(gameMode) +
            RespawnQueueModeOffset) != 0;
    if (RespawnStatePolicy::ShouldSuppressRoleConfirmationRestart(
            gLiveRoleConfirmationAction,
            sameController))
    {
        gRoleConfirmationRestartWasSuppressed = true;
        const bool liveNextLife = gLiveRoleConfirmationAction ==
            RespawnStatePolicy::
                LiveRoleConfirmationAction::CommitForNextRespawn;
        std::cout << "[RESPAWN] Suppressed ServerConfirmRoleSelection "
                     "RestartPlayer; native role/loadout commit retained. "
                     "reason="
                  << (liveNextLife
                        ? "live_next_life"
                        : "post_death_replace_with_pb_quick")
                  << " native_queue_mode="
                  << (nativeRestartWouldEnterRespawnQueue ? 1 : 0)
                  << " existing_pawn="
                  << (controllerStillHasPawn ? 1 : 0)
                  << std::endl;
        return;
    }

    ServerRoleConfirmationRestartPlayer.call<void>(gameMode, newPlayer);
}

void ServerStartShowingMatchResultHook(APBGameMode* gameMode)
{
    ServerStartShowingMatchResult.call<void>(gameMode);
    DedicatedMultiMatch::OnShowingMatchResult(gameMode);
}

void ServerStartWaitingToEndGameHook(APBGameMode* gameMode)
{
    if (DedicatedMultiMatch::HandleWaitingToEndGame(gameMode))
        return;

    BeginGracefulDedicatedExit(gameMode, "process-per-match");
}

void ServerBeginFinalCleanupHook(APBGameMode* gameMode, float cleanupWait)
{
    if (DedicatedMultiMatch::ShouldSuppressNativeFinalCleanup(gameMode))
    {
        std::cout << "[MULTIMATCH] Suppressed retired GameMode native final cleanup/process exit."
                  << std::endl;
        return;
    }

    ServerBeginFinalCleanup.call<void>(gameMode, cleanupWait);
}

bool NotifyActorDestroyedHook(UWorld *World, AActor *Actor, bool SomeShit, bool SomeShit2)
{
    if (listening && Actor)
    {
        if (Actor->IsA(APBPlayerController::StaticClass()))
        {
            CleanupDisconnectedPlayer(
                static_cast<APBPlayerController*>(Actor), "controller destroyed");
        }
        else
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager)
                gLoadoutManager->OnActorDestroyed(Actor);
        }
    }

    bool ret = NotifyActorDestroyed.call<bool>(World, Actor, SomeShit, SomeShit2);

    if (listening && Actor && libReplicate)
    {
        LibReplicate::FActorInfo ActorInfo = LibReplicate::FActorInfo((void *)Actor, Actor->bNetTemporary);

        libReplicate->CallWhenActorDestroyed(ActorInfo);
    }

    return ret;
}

static SafetyHookInline NotifyAcceptingConnection = {};

__int64 NotifyAcceptingConnectionHook(UObject *obj)
{
    // The travel proof deliberately requires the exact pre-travel connection
    // set.  Do not admit a new connection while ownership of the world is in
    // flux; accepting it would make both the proof and native channel handoff
    // ambiguous.
    if (DedicatedMultiMatch::OwnsWorldTransition())
        return 0;
    return 1;
}

static SafetyHookInline NotifyControlMessage = {};

char NotifyControlMessageHook(unsigned __int64 ScuffedShit, __int64 a2, uint8_t a3, __int64 a4)
{
    if (UWorld *World = UWorld::GetWorld())
    {
        if (UNetDriver *ActiveNetDriver = NetDriverAccess::Resolve())
        {
            NetDriverAccess::Observe(ActiveNetDriver, World, NetDriverAccess::Source::Cached);
        }
    }

    return NotifyControlMessage.call<char>(ScuffedShit, a2, a3, a4);
}

static SafetyHookInline ProcessEvent;

static bool IsExplicitNativeRespawnForwardEnabled()
{
    static const bool enabled = CommandLinePolicy::FeatureEnabled(
        GetCommandLineA(), "-RespawnExplicitNative", true);
    return enabled;
}

static bool HandleManagedExplicitRespawn(
    APBPlayerController* playerController,
    const char* requestKind,
    UObject* object,
    UFunction* function,
    void* parms,
    const std::string& functionName)
{
    const bool managed = playerController && gLateJoinManager &&
        gLateJoinManager->IsManagedPlayer(playerController);
    const bool permitted = managed &&
        gLateJoinManager->HasManagedRestartPermit(playerController);
    const bool awaitingInput = managed &&
        gLateJoinManager->IsAwaitingRespawnInput(playerController);
    const bool canQueue = managed &&
        gLateJoinManager->CanQueueManagedRespawn(playerController);
    const auto action = RespawnStatePolicy::DecideExplicitRequest(
        managed, permitted, awaitingInput, canQueue,
        IsExplicitNativeRespawnForwardEnabled());

    using Action = RespawnStatePolicy::ExplicitRequestAction;
    if (action == Action::PassThrough)
        return false;

    if (action == Action::Deny)
    {
        std::cout << "[RESPAWN] origin=explicit_f request_kind="
            << requestKind
            << " hook_action=denied awaiting_input=" << awaitingInput
            << " can_queue=" << canQueue << std::endl;
        return true;
    }

    if (action == Action::QueueAndSuppress)
    {
        const bool queued = gLateJoinManager->QueueManagedRespawn(playerController);
        std::cout << "[RESPAWN] origin=explicit_f request_kind="
            << requestKind
            << " hook_action=" << (queued
                ? "queued_and_suppressed" : "denied_queue_failed")
            << " ab_mode=legacy_replacement" << std::endl;
        return true;
    }

    const bool deferEngineRestartToPBQuick =
        RespawnStatePolicy::ShouldDeferEngineRestartToPBQuickRespawn(
            action,
            functionName.contains("PlayerController.ServerRestartPlayer"));
    if (deferEngineRestartToPBQuick)
    {
        std::cout << "[RESPAWN] request_kind=" << requestKind
            << " hook_action=deferred_to_native_pb_quick" << std::endl;
        return true;
    }

    const bool forwarded = gLateJoinManager->DispatchManagedExplicitRespawn(
        playerController,
        requestKind,
        [&]() {
            ProcessEvent.call(object, function, parms);
        });
    if (!forwarded)
    {
        std::cout << "[RESPAWN] origin=explicit_f request_kind="
            << requestKind
            << " hook_action=denied_dispatch_failed" << std::endl;
        return true;
    }

    BattleLog::OnProcessEventPost(
        BattleLog::ProcessSide::Server, object, functionName, parms);
    return true;
}

void ProcessEventHook(UObject *Object, UFunction *Function, void *Parms)
{
    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";
    // A listen host owns both authority and a local player but can install only
    // one inline ProcessEvent hook. Complete this lifecycle signal only after
    // the native MainMenuBase Construct has returned so listen travel cannot
    // overtake the platform-login UI transition.
    const bool listenLoginCompletedEvent = amListenServer &&
        functionName.contains("UMG_MainMenuBase_C.Construct");
    const bool listenEnterGameEvent = amListenServer &&
        (functionName.contains("UMG_EnterGame_C.Construct") ||
            functionName.contains("UMG_EnterGame_C.BP_OnActivated"));
    if (listenEnterGameEvent)
    {
        static std::atomic_bool listenAutoLoginQueued{false};
        if (!listenAutoLoginQueued.exchange(true, std::memory_order_acq_rel))
        {
            ClientLog("[LOGIN] Listen EnterGame ready; forcing SPACE once.");
            std::thread([]()
                {
                    Sleep(1000);
                    PressSpace();
                })
                .detach();
        }
    }

    // 热键检测（游戏线程安全）— F6=dump, F7=reapply snapshot
    if (gDebugTool)
    {
        static auto nextHotkeyCheck = std::chrono::steady_clock::now();
        const auto now = std::chrono::steady_clock::now();
        if (now >= nextHotkeyCheck)
        {
            nextHotkeyCheck = now + std::chrono::milliseconds(500);
            try
            {
                if (GetAsyncKeyState(VK_F6) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F6);
                if (GetAsyncKeyState(VK_F7) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F7);
            }
            catch (...)
            {
                // 吞掉所有异常 — 调试工具不应导致游戏崩溃
            }
        }
    }

    // ServerSay：拦截调试命令（__DBG__ 前缀）
    if (functionName.contains("ServerSay"))
    {
        APBPlayerController *PBPlayerController = Object && Object->IsA(APBPlayerController::StaticClass())
                                                      ? (APBPlayerController *)Object
                                                      : nullptr;
        if (PBPlayerController)
        {
            auto *SayParms = static_cast<Params::PBPlayerController_ServerSay *>(Parms);
            if (SayParms)
            {
                const std::string msg = SayParms->Msg.ToString();

                // __DBG__ 前缀：运行时调试命令
                if (msg.rfind("__DBG__", 0) == 0)
                {
                    const std::string payload = msg.substr(7);
                    if (gDebugTool)
                        gDebugTool->ExecuteChat(payload);
                    // 抑制此聊天消息
                    return;
                }

                if (DedicatedMultiMatch::HandleServerSay(PBPlayerController, msg))
                    return;
            }
        }
    }

    // PBGameMode can restart a whole round in one batch. Split authoritative
    // player controllers out of that batch and queue them through the same
    // per-role lease/JIT seed path; AI and untracked controllers retain the
    // native call. A matching managed permit is installed only by
    // LateJoinManager immediately around its singleton replay.
    if (functionName.contains("PBGameMode.RestartPlayers"))
    {
        auto* restartParms = static_cast<Params::PBGameMode_RestartPlayers*>(Parms);
        if (restartParms && gLateJoinManager)
        {
            TArray<AController*> nativeControllers{};
            bool interceptedManagedPlayer = false;
            for (AController* controller : restartParms->InControllers)
            {
                APBPlayerController* playerController =
                    controller && controller->IsA(APBPlayerController::StaticClass())
                        ? static_cast<APBPlayerController*>(controller)
                        : nullptr;
                if (!playerController ||
                    !gLateJoinManager->IsManagedPlayer(playerController) ||
                    gLateJoinManager->HasManagedRestartPermit(playerController))
                {
                    nativeControllers.Add(controller);
                    continue;
                }

                interceptedManagedPlayer = true;
                const auto allowed = PlayerRespawnAllowedMap.find(playerController);
                const bool awaitingInput =
                    gLateJoinManager->IsAwaitingRespawnInput(playerController);
                if (!awaitingInput &&
                    (allowed == PlayerRespawnAllowedMap.end() || allowed->second))
                    gLateJoinManager->QueueManagedRespawn(playerController);
                else if (awaitingInput)
                    std::cout << "[LATEJOIN] Suppressed automatic RestartPlayers "
                                 "while awaiting native F/ESC input." << std::endl;
            }

            if (interceptedManagedPlayer)
            {
                if (nativeControllers.Num() > 0)
                {
                    restartParms->InControllers = nativeControllers;
                    ProcessEvent.call(Object, Function, Parms);
                    BattleLog::OnProcessEventPost(
                        BattleLog::ProcessSide::Server, Object, functionName, Parms);
                }
                return;
            }
        }
    }

    // Backstop direct GameModeBase restart entry points that do not use the
    // PBGameMode batch wrapper. Their first parameter is always NewPlayer.
    if (functionName.contains("GameModeBase.RestartPlayer"))
    {
        AController* controller = nullptr;
        if (functionName.contains("RestartPlayerAtPlayerStart"))
        {
            auto* restartParms =
                static_cast<Params::GameModeBase_RestartPlayerAtPlayerStart*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }
        else if (functionName.contains("RestartPlayerAtTransform"))
        {
            auto* restartParms =
                static_cast<Params::GameModeBase_RestartPlayerAtTransform*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }
        else
        {
            auto* restartParms = static_cast<Params::GameModeBase_RestartPlayer*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }

        APBPlayerController* playerController =
            controller && controller->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(controller)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            if (!gLateJoinManager->IsAwaitingRespawnInput(playerController) &&
                (allowed == PlayerRespawnAllowedMap.end() || allowed->second))
                gLateJoinManager->QueueManagedRespawn(playerController);
            return;
        }
    }

    // Last ProcessEvent-level backstop before a pawn is allocated. Native
    // engine code can bypass the public restart wrappers, but any reflected
    // SpawnDefaultPawn call for a tracked controller still requires the JIT
    // seed permit.
    if (functionName.contains("GameModeBase.SpawnDefaultPawn"))
    {
        AController* controller = nullptr;
        APawn** returnValue = nullptr;
        if (functionName.contains("SpawnDefaultPawnAtTransform"))
        {
            auto* spawnParms =
                static_cast<Params::GameModeBase_SpawnDefaultPawnAtTransform*>(Parms);
            if (spawnParms)
            {
                controller = spawnParms->NewPlayer;
                returnValue = &spawnParms->ReturnValue;
            }
        }
        else
        {
            auto* spawnParms = static_cast<Params::GameModeBase_SpawnDefaultPawnFor*>(Parms);
            if (spawnParms)
            {
                controller = spawnParms->NewPlayer;
                returnValue = &spawnParms->ReturnValue;
            }
        }

        APBPlayerController* playerController =
            controller && controller->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(controller)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            if (!gLateJoinManager->IsAwaitingRespawnInput(playerController) &&
                (allowed == PlayerRespawnAllowedMap.end() || allowed->second))
                gLateJoinManager->QueueManagedRespawn(playerController);
            if (returnValue) *returnValue = nullptr;
            return;
        }
    }

    if (functionName.contains("PBPlayerController.ServerQuickRespawn"))
    {
        APBPlayerController *PBPlayerController = (APBPlayerController *)Object;

        if (HandleManagedExplicitRespawn(
            PBPlayerController, "ServerQuickRespawn",
            Object, Function, Parms, functionName))
            return;

        if (PlayerRespawnAllowedMap.contains(PBPlayerController) &&
            PlayerRespawnAllowedMap[PBPlayerController] == false)
        {
            std::cout << "Denied quick respawn until role/loadout confirmation!" << std::endl;
            return;
        }
    }

    if (functionName.contains("PlayerController.ServerRestartPlayer"))
    {
        APBPlayerController *PBPlayerController = (APBPlayerController *)Object;

        if (HandleManagedExplicitRespawn(
            PBPlayerController, "ServerRestartPlayer",
            Object, Function, Parms, functionName))
            return;

        if (PlayerRespawnAllowedMap.contains(PBPlayerController) && PlayerRespawnAllowedMap[PBPlayerController] == false)
        {
            std::cout << "Denied restart!" << std::endl;
            return;
        }
    }

    // The destination PlayerState clears SelectedCharacterID even though the
    // source role and FieldMod baseline were preserved. LoadoutManager replays
    // the exact native confirmation once ServerNotifyLoadedWorld completes.
    // Permit only the synchronous Can* queries nested under that one internal
    // confirmation; client/UI submissions and ordinary/P2P queries stay native.
    APBPlayerController* internalRoleQueryPlayer = nullptr;
    if (functionName.contains("PBGameMode.CanPlayerSelectRole"))
    {
        auto* roleParms =
            static_cast<Params::PBGameMode_CanPlayerSelectRole*>(Parms);
        internalRoleQueryPlayer = roleParms ? roleParms->Player : nullptr;
        bool allow = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            allow = gLoadoutManager &&
                gLoadoutManager->IsInternalSeamlessRoleReconfirmInProgress(
                    internalRoleQueryPlayer);
        }
        if (allow)
        {
            roleParms->ReturnValue = true;
            return;
        }
    }
    else if (functionName.contains("PBPlayerController.CanSelectRole"))
    {
        internalRoleQueryPlayer =
            Object && Object->IsA(APBPlayerController::StaticClass())
            ? static_cast<APBPlayerController*>(Object)
            : nullptr;
        bool allow = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            allow = gLoadoutManager &&
                gLoadoutManager->IsInternalSeamlessRoleReconfirmInProgress(
                    internalRoleQueryPlayer);
        }
        if (allow)
        {
            auto* roleParms =
                static_cast<Params::PBPlayerController_CanSelectRole*>(Parms);
            if (roleParms)
                roleParms->ReturnValue = true;
            return;
        }
    }

    // LateJoin: role-selection interception (CanPlayerSelectRole / CanSelectRole)
    if (gLateJoinManager && gLateJoinManager->OnProcessEvent(Object, functionName, Parms))
    {
        // Already handled by LateJoinManager
        return;
    }

    // Always let the native server implementation validate and publish a
    // pre-order first. The bridge records a runtime override only when the
    // player's own +0x6C0 pre-ordering entry exactly matches afterwards.
    if (functionName.contains("PBPlayerController.ServerPreOrderInventory"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        auto* preOrderParms =
            static_cast<Params::PBPlayerController_ServerPreOrderInventory*>(Parms);
        // Capture this before the native pre-order and bridge callbacks. The
        // death screen replays all role configs before role confirmation, and
        // configuration traffic must not consume explicit F/ESC intent.
        const bool wasAwaitingRespawnInputBeforePreOrder =
            gLateJoinManager && playerController &&
            gLateJoinManager->IsAwaitingRespawnInput(playerController);

        bool internalManagerWrite = false;
        bool holdForSeamlessSeed = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            internalManagerWrite = gLoadoutManager &&
                gLoadoutManager->IsInternalPreOrderInProgress();
            holdForSeamlessSeed = !internalManagerWrite && gLoadoutManager &&
                playerController &&
                gLoadoutManager->ShouldHoldExternalPreOrderForSeamlessSeed(
                    playerController);
        }
        if (holdForSeamlessSeed)
        {
            std::cout << "[MULTIMATCH] Held destination native pre-order until "
                         "FieldMod roles and client travel are ready." << std::endl;
            return;
        }
        if (internalManagerWrite)
        {
            ProcessEvent.call(Object, Function, Parms);
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        ProcessEvent.call(Object, Function, Parms);
        bool recordedRuntimeOverride = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && preOrderParms &&
                ConnectedPlayerControllers.contains(playerController) &&
                !DisconnectedPlayerControllers.contains(playerController) &&
                !playerController->bActorIsBeingDestroyed)
            {
                recordedRuntimeOverride = gLoadoutManager->OnExternalPreOrderInventory(
                    playerController,
                    preOrderParms->InRoleID,
                    preOrderParms->InPreOrderingInventory);
            }
        }
        // The native RPC has returned. Retain the historical managed recovery
        // only for a blocked, non-awaiting lifecycle (for example TimedOut),
        // even when no bridge override was recorded. A pre-order that entered
        // during the explicit death wait remains configuration-only; role
        // confirmation or F owns the later respawn transition. The manager API
        // itself still rejects PendingRoleSelection and every first-spawn state.
        if (gLateJoinManager && playerController && preOrderParms)
        {
            const bool isCurrentSelectedRole =
                IsCurrentSelectedRole(playerController, preOrderParms->InRoleID);
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            const bool respawnIsBlocked =
                allowed != PlayerRespawnAllowedMap.end() && !allowed->second;
            if (RespawnStatePolicy::
                ShouldQueueManagedRespawnAfterPreOrderReturn(
                    isCurrentSelectedRole,
                    respawnIsBlocked,
                    wasAwaitingRespawnInputBeforePreOrder))
            {
                const bool queued =
                    gLateJoinManager->QueueManagedRespawn(playerController);
                if (queued && !recordedRuntimeOverride)
                {
                    std::cout << "[LATEJOIN] Native inventory accepted; queued "
                        "managed respawn without a recorded bridge override."
                        << std::endl;
                }
            }
            else if (isCurrentSelectedRole && respawnIsBlocked &&
                wasAwaitingRespawnInputBeforePreOrder)
            {
                std::cout << "[RESPAWN] Pre-order returned while awaiting "
                             "F/ESC; kept staged without queuing respawn. role="
                          << preOrderParms->InRoleID.ToString()
                          << " runtime_override="
                          << (recordedRuntimeOverride ? 1 : 0) << std::endl;
            }
        }
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    // Loadout/LateJoin: role confirmation can be deferred for at most one
    // second without blocking the game thread. A deferred confirmation is
    // replayed by LoadoutManager with copied parameters and a re-entry guard.
    if (functionName.contains("PBPlayerController.ServerConfirmRoleSelection"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        auto* confirmParms =
            static_cast<Params::PBPlayerController_ServerConfirmRoleSelection*>(Parms);

        const std::string requestedRole = confirmParms
            ? confirmParms->InRoleID.ToString()
            : std::string{};
        // Capture this before BeginRoleConfirmation applies the effective
        // pre-order and before the native confirmation enters its synchronous
        // RestartPlayer path. Both can re-enter reflected callbacks and mutate
        // the managed state before OnRoleConfirmed runs.
        const bool wasAwaitingRespawnInputBeforeConfirmation =
            gLateJoinManager && playerController &&
            gLateJoinManager->IsAwaitingRespawnInput(playerController);
        if (gLateJoinManager && playerController && confirmParms &&
            gLateJoinManager->IsRedundantSeamlessRoleConfirmation(
                playerController, requestedRole))
        {
            std::cout << "[MULTIMATCH] Suppressed redundant destination role "
                "confirmation: role=" << requestedRole << std::endl;
            return;
        }

        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && confirmParms &&
                IsCurrentConnectedController(playerController))
            {
                const LoadoutRoleConfirmDecision decision =
                    gLoadoutManager->BeginRoleConfirmation(
                        playerController, confirmParms->InRoleID);
                if (decision == LoadoutRoleConfirmDecision::Deferred)
                    return;
            }
        }

        QueuePendingPlayerNameUpdate(playerController);
        const bool isLateJoin = gLateJoinManager &&
            gLateJoinManager->IsLateJoinPlayer(playerController);
        const bool isDeferredInitialJoin = gLateJoinManager &&
            gLateJoinManager->IsInitialJoinPlayer(playerController);

        const bool stageForNextRespawn = gLateJoinManager &&
            playerController && confirmParms &&
            gLateJoinManager->ShouldStageLiveRoleConfirmation(
                playerController, requestedRole);
        const bool requestedRoleIsConcrete =
            !requestedRole.empty() && requestedRole != "None";
        const auto liveConfirmationAction =
            RespawnStatePolicy::DecideRoleConfirmationRestart(
                stageForNextRespawn,
                wasAwaitingRespawnInputBeforeConfirmation,
                requestedRoleIsConcrete);
        bool confirmationRestartWasSuppressed = false;
        {
            // The pinned native ServerConfirmRoleSelection implementation
            // commits PlayerState.SelectedCharacterID and its merged
            // pre-ordering cache before making this synchronous virtual
            // RestartPlayer call. Scope the suppression to that exact call.
            // A post-death confirmation is followed below by the PB-specific
            // ServerQuickRespawn path, which owns cooldown and observer state.
            ScopedLiveRoleConfirmationRestartPolicy restartPolicy(
                playerController, liveConfirmationAction);
            ProcessEvent.call(Object, Function, Parms);
            confirmationRestartWasSuppressed =
                restartPolicy.WasRestartSuppressed();
        }

        const bool connectionStillCurrent =
            IsCurrentConnectedController(playerController);
        bool roleWasAccepted = connectionStillCurrent;
        std::string committedRoleId;
        if (roleWasAccepted && (!playerController->PBPlayerState || !confirmParms))
            roleWasAccepted = false;
        if (roleWasAccepted && playerController->PBPlayerState && confirmParms)
        {
            auto* playerState = playerController->PBPlayerState;
            bool queriedSelectionState = false;
            bool hasSelectedRole = false;
            try
            {
                hasSelectedRole = playerState->HasSelectedRole();
                queriedSelectionState = true;
            }
            catch (...)
            {
                // Keep compatibility with SDK revisions where this helper is
                // unavailable; the concrete SelectedCharacterID check below
                // can still reject a mismatched selection.
            }

            if (!IsCurrentConnectedController(playerController) ||
                playerController->PBPlayerState != playerState)
            {
                roleWasAccepted = false;
            }

            const std::string acceptedRole = roleWasAccepted
                ? playerState->SelectedCharacterID.ToString()
                : std::string{};
            const std::string requestedRole = confirmParms->InRoleID.ToString();
            const bool acceptedRoleIsConcrete =
                !acceptedRole.empty() && acceptedRole != "None";
            const bool requestedRoleIsConcrete =
                !requestedRole.empty() && requestedRole != "None";

            // ServerConfirmRoleSelection is synchronous on the authoritative
            // player state. Advance neither spawn nor quorum unless the state
            // reports a selected role and it is exactly the requested one.
            if ((queriedSelectionState && !hasSelectedRole) ||
                !acceptedRoleIsConcrete || !requestedRoleIsConcrete ||
                acceptedRole != requestedRole)
            {
                roleWasAccepted = false;
            }
            else
            {
                committedRoleId = acceptedRole;
            }
        }
        if (!roleWasAccepted)
        {
            ClientLog("[LOADOUT] Role confirmation did not commit to the current connection");
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && confirmParms &&
                IsCurrentConnectedController(playerController))
            {
                gLoadoutManager->CommitRoleConfirmationAfterOriginal(
                    playerController, confirmParms->InRoleID);
            }
        }

        if (gLateJoinManager && (isLateJoin || isDeferredInitialJoin))
        {
            gLateJoinManager->OnRoleConfirmed(
                playerController, committedRoleId,
                wasAwaitingRespawnInputBeforeConfirmation,
                confirmationRestartWasSuppressed);

            // Deploy on the post-death role screen is both a role commit and
            // respawn intent. ServerConfirmRoleSelection's raw RestartPlayer
            // was suppressed above; replay the intent through the exact PB RPC
            // under a manager permit. If native cooldown rejects it, the
            // manager restores AwaitingRespawnInput with no fallback armed.
            if (wasAwaitingRespawnInputBeforeConfirmation &&
                IsCurrentConnectedController(playerController))
            {
                gLateJoinManager->DispatchPostDeathRoleDeployRespawn(
                    playerController,
                    [playerController]() {
                        playerController->ServerQuickRespawn();
                    });
            }
        }

        if (!IsCurrentConnectedController(playerController))
        {
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        // A player joining an already-running match must not alter the
        // original match-start quorum. Initial joins still count normally.
        if (!isLateJoin && playerController)
        {
            const auto [_, inserted] = PlayersConfirmedRole.insert(playerController);
            if (inserted)
            {
                NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
                std::cout << "[MATCH] Role confirmed ("
                    << NumPlayersSelectedRole << "/" << NumExpectedPlayers << ")"
                    << std::endl;
            }
            else
            {
                std::cout << "[MATCH] Ignoring duplicate role confirmation."
                    << std::endl;
            }

            RecomputeMatchStartGate(inserted ? "role confirmed" : "duplicate confirmation");
        }

        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    // Inventory actors now exist; detailed configs may safely be applied, but
    // only when their live identities match the effective inventory.
    if (functionName.contains("K2_InventorySpawned"))
    {
        APBCharacter* character =
            Object && Object->IsA(APBCharacter::StaticClass())
                ? static_cast<APBCharacter*>(Object)
                : nullptr;
        bool tombstonedBeforeOriginal = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            tombstonedBeforeOriginal = gLoadoutManager && character &&
                gLoadoutManager->IsCharacterTombstoned(character);
        }
        ProcessEvent.call(Object, Function, Parms);
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            const bool destroyedDuringOriginal =
                gLoadoutManager && character && !tombstonedBeforeOriginal &&
                gLoadoutManager->IsCharacterTombstoned(character);
            if (gLoadoutManager && character && !destroyedDuringOriginal)
                gLoadoutManager->OnInventorySpawned(character);
        }
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    if (functionName.contains("K2_OnLogout"))
    {
        auto* logoutParms = static_cast<Params::GameModeBase_K2_OnLogout*>(Parms);
        APBPlayerController* playerController =
            logoutParms && logoutParms->ExitingController &&
                    logoutParms->ExitingController->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(logoutParms->ExitingController)
                : nullptr;

        if (playerController)
            CleanupDisconnectedPlayer(playerController, "player disconnected");
        ProcessEvent.call(Object, Function, Parms);
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    if (functionName.contains("ReadyToMatchIntro_WaitingToStart"))
    {
        ApplyPendingPlayerNameUpdates("ReadyToMatchIntro_WaitingToStart");
        if (!JoinUiSyncPolicy::ShouldForwardReadyToMatchIntro(
                canStartMatch,
                DedicatedMultiMatch::OwnsWorldTransition()))
        {
            return;
        }

        const bool hasFreshSeamlessInitialPlayer = gLateJoinManager &&
            gLateJoinManager->HasFreshSeamlessInitialPlayer();
        if (hasFreshSeamlessInitialPlayer)
        {
            ProcessEvent.call(Object, Function, Parms);
            auto* readyParms = static_cast<
                Params::PBGameMode_ReadyToMatchIntro_WaitingToStart*>(Parms);
            const bool nativeReady = readyParms && readyParms->ReturnValue;
            const bool initialPlayersReady = gLateJoinManager &&
                gLateJoinManager->AreInitialPlayersReadyForStart();
            if (readyParms && JoinUiSyncPolicy::
                    ShouldRestoreFreshDestinationReadyToMatchIntro(
                        canStartMatch,
                        hasFreshSeamlessInitialPlayer,
                        initialPlayersReady,
                        nativeReady))
            {
                readyParms->ReturnValue = true;
                std::cout << "[MULTIMATCH] Restored post-spawn destination "
                             "ReadyToMatchIntro result (native=0)."
                          << std::endl;
            }
            else
            {
                std::cout << "[MULTIMATCH] Preserved pre-spawn destination "
                             "ReadyToMatchIntro result="
                          << (nativeReady ? 1 : 0)
                          << " initial_players_ready="
                          << (initialPlayersReady ? 1 : 0) << std::endl;
            }
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }
    }

    if (functionName.contains("PBPlayerController.ClientBeKilled"))
    {
        APBPlayerController* PBPlayerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        auto* killedParms =
            static_cast<Params::PBPlayerController_ClientBeKilled*>(Parms);
        const bool isLocalVictim = PBPlayerController && killedParms &&
            killedParms->VictimPlayerState &&
            killedParms->VictimPlayerState == PBPlayerController->PBPlayerState;

        if (isLocalVictim)
            PlayerRespawnAllowedMap[PBPlayerController] = false;
        // Deliver the native death notification first. The managed state
        // machine changes only the server-side restart permit afterwards; the
        // client retains its native F-to-respawn / ESC-to-select-role UI.
        ProcessEvent.call(Object, Function, Parms);
        if (gLateJoinManager && isLocalVictim)
        {
            std::cout << "[LATEJOIN] Intercepted local player death: role="
                << killedParms->VictimRoleID.ToString() << std::endl;
            gLateJoinManager->OnPlayerKilled(PBPlayerController);
        }
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    if (functionName.contains("PlayerController.CanRestartPlayer"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            auto* restartParms =
                static_cast<Params::PlayerController_CanRestartPlayer*>(Parms);
            if (restartParms)
            {
                restartParms->ReturnValue =
                    gLateJoinManager->IsAwaitingRespawnInput(playerController);
            }
            return;
        }
    }

    if (functionName.contains("GameModeBase.PlayerCanRestart"))
    {
        auto* restartParms = (Params::GameModeBase_PlayerCanRestart *)Parms;
        APBPlayerController* playerController = restartParms && restartParms->Player &&
                restartParms->Player->IsA(APBPlayerController::StaticClass())
            ? static_cast<APBPlayerController*>(restartParms->Player)
            : nullptr;
        bool seamlessRecoveryPermit = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            seamlessRecoveryPermit = gLoadoutManager &&
                gLoadoutManager->IsInternalSeamlessRoleReconfirmInProgress(
                    playerController);
        }
        if (restartParms && seamlessRecoveryPermit)
        {
            restartParms->ReturnValue = true;
            return;
        }
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController))
        {
            if (gLateJoinManager->HasManagedRestartPermit(playerController))
            {
                restartParms->ReturnValue = true;
                return;
            }

            if (gLateJoinManager->IsAwaitingRespawnInput(playerController))
            {
                restartParms->ReturnValue = true;
                return;
            }

            restartParms->ReturnValue = false;
            return;
        }

        restartParms->ReturnValue = ((AGameModeBase *)Object)->HasMatchStarted() ||
            (gLateJoinManager && gLateJoinManager->CanRestartBeforeMatch(playerController));
        return;
    }

    ProcessEvent.call(Object, Function, Parms);
    if (listenLoginCompletedEvent)
        NotifyClientLoginCompleted();
    if (amListenServer)
        PumpPendingClientCommands();
    BattleLog::OnProcessEventPost(
        BattleLog::ProcessSide::Server,
        Object,
        functionName,
        Parms);
}

static SafetyHookInline PostLoginHook;

void *PostLogin(AGameMode *GameMode, APBPlayerController *PC)
{
    EnsureServerMatchWorld(UWorld::GetWorld());
    if (PC)
        DisconnectedPlayerControllers.erase(PC);
    void *Ret = PostLoginHook.call<void *>(GameMode, PC);

    if (!PC || DisconnectedPlayerControllers.contains(PC) ||
        PC->bActorIsBeingDestroyed)
    {
        std::cout << "[SERVER] Ignored PostLogin result for a disconnected controller."
            << std::endl;
        return Ret;
    }
    RegisterAuthoritativeMatchParticipant(GameMode, PC, false);
    return Ret;
}

static SafetyHookInline OnFireWeaponHook;

void *OnFireWeapon(APBWeapon *Weapon)
{
    if ((uintptr_t)_ReturnAddress() - BaseAddress != 0x1608B31)
    {
        return nullptr;
    }
    else
    {
        return OnFireWeaponHook.call<void *>(Weapon);
    }
}

// ======================================================
//  SECTION 9 — HOOK DETOURS (CLIENT HOOKS)
// ======================================================

static SafetyHookInline ProcessEventClient;
static SafetyHookInline FixEquipErrorHook;
static SafetyHookInline FixCharacterSkinPaintingErrorHook;
static SafetyHookInline FixCharacterAppearanceErrorHook;
static SafetyHookInline FixWeaponOrnamentErrorHook;
static SafetyHookInline FixWeaponPartSkinPaintingErrorHook;
static SafetyHookInline FixWeaponPartSlotErrorHook;
static SafetyHookInline FixWeaponSuiteErrorHook;

static void LogArchiveCompletionTranslation(
    const char* completionKind,
    int completionCode,
    int normalizedCode,
    std::atomic<unsigned long long>& translationCount)
{
    const auto count = translationCount.fetch_add(1, std::memory_order_relaxed) + 1;
    ClientLog("[ARCHIVE] " + std::string(completionKind) +
        " completion translated " + std::to_string(completionCode) + "->" +
        std::to_string(normalizedCode) + " after persisted update; count=" +
        std::to_string(count));
}

void __fastcall FixEquipErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, int a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeEquipmentCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "equipment_archive", completionCode, normalized, translationCount);
    FixEquipErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixCharacterSkinPaintingErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, __int64 a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizePersistedCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "character_skin_painting", completionCode, normalized, translationCount);
    FixCharacterSkinPaintingErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixCharacterAppearanceErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, int a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizePersistedCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "character_appearance", completionCode, normalized, translationCount);
    FixCharacterAppearanceErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixWeaponOrnamentErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, __int64 a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeWeaponCustomizationCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "weapon_ornament", completionCode, normalized, translationCount);
    FixWeaponOrnamentErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixWeaponPartSkinPaintingErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4,
    __int64 a5, __int64 a6, __int64 a7)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeWeaponCustomizationCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "weapon_part_skin_painting", completionCode, normalized, translationCount);
    FixWeaponPartSkinPaintingErrorHook.call<void>(
        a1, normalized, a3, a4, a5, a6, a7);
}

void __fastcall FixWeaponPartSlotErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4,
    __int64 a5, int a6)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeWeaponCustomizationCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "weapon_part_slot", completionCode, normalized, translationCount);
    FixWeaponPartSlotErrorHook.call<void>(a1, normalized, a3, a4, a5, a6);
}

void __fastcall FixWeaponSuiteErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4,
    __int64 a5, __int64 a6)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeWeaponCustomizationCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "weapon_suite", completionCode, normalized, translationCount);
    FixWeaponSuiteErrorHook.call<void>(a1, normalized, a3, a4, a5, a6);
}

void ProcessEventHookClient(UObject *Object, UFunction *Function, void *Parms)
{
    if (gClientProcessEventSuppressionDepth > 0)
    {
        ProcessEventClient.call(Object, Function, Parms);
        return;
    }

    // 热键检测（游戏线程安全）— F6=dump, F7=reapply snapshot
    if (gDebugTool)
    {
        static auto nextHotkeyCheck = std::chrono::steady_clock::now();
        const auto now = std::chrono::steady_clock::now();
        if (now >= nextHotkeyCheck)
        {
            nextHotkeyCheck = now + std::chrono::milliseconds(500);
            try
            {
                if (GetAsyncKeyState(VK_F6) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F6);
                if (GetAsyncKeyState(VK_F7) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F7);
            }
            catch (...)
            {
                // 吞掉所有异常 — 调试工具不应导致游戏崩溃
            }
        }
    }

    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";

    // The server arms this fixed-build delegate guard at UWorld::SeamlessTravel.
    // The remote client reaches the same owned transition through the reflected
    // ClientTravel RPC instead, before its local UWorld sees a marker-bearing
    // seamless URL. Arm the identical exact-address guard on that peer here.
    if (DirectMatchUiCleanupPolicy::
            IsOwnedSeamlessTravelEvent(functionName))
    {
        auto* travelParms =
            static_cast<Params::PlayerController_ClientTravel*>(Parms);
        const bool ownedTravel = travelParms &&
            IsOwnedMultiMatchTravelUrl(&travelParms->URL);
        if (travelParms &&
            DedicatedMultiMatchPolicy::ShouldArmClientTravelDelegateGuard(
                ownedTravel, travelParms->bSeamless))
        {
            ArmOwnedTravelDelegateGuard();
            ArmOwnedSeamlessDestinationUiCleanup();
            ArmOwnedSeamlessIntroCameraRecovery();
        }
    }

    if (DirectMatchUiCleanupPolicy::
            IsOwnedSeamlessDestinationStartEvent(functionName) &&
        Object && Object->IsA(APBPlayerController::StaticClass()))
    {
        // Run before the destination start RPC creates its new match layers.
        // If the retained HUD is not ready yet the pending flag survives and
        // the next start RPC retries on the same game thread.
        TryFinalizeOwnedSeamlessDestinationUi(
            static_cast<APBPlayerController*>(Object));
    }

    const bool nativeDeathEvent = DirectMatchUiCleanupPolicy::
        IsNativeDeathEvent(functionName);
    const bool nativeIntroCompletionEvent = SeamlessIntroCameraPolicy::
        IsNativeIntroCompletionEvent(functionName);
    const bool nativeCameraSettleEvent = SeamlessIntroCameraPolicy::
        IsNativeCameraSettleEvent(functionName);
    bool nativePlayableRestartEvent = false;
    if (DirectMatchUiCleanupPolicy::IsNativePlayableRestartEvent(
            functionName, Parms != nullptr))
    {
        const auto* restartParms =
            static_cast<Params::PlayerController_ClientRestart*>(Parms);
        nativePlayableRestartEvent = restartParms->NewPawn != nullptr;
    }
    if (nativeDeathEvent && Object &&
        Object->IsA(APBPlayerController::StaticClass()))
    {
        ArmNativeRespawnUiCleanup();
    }

    // TEMP LOGIN DEBUG DUMP (GameInstance only)
    // if (Object && Object->IsA(UPBGameInstance::StaticClass()))
    //{
    //    std::string fn = Function->GetFullName();
    //        std::cout << "[LOGIN-DUMP] GI :: " << fn << std::endl;
    //}
    // Froce space to login
    if (functionName.contains("UMG_EnterGame_C.Construct"))
    {
        ClientLog("[LOGIN] EnterGame Construct forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000); // small delay so widget is fully active
                PressSpace(); })
            .detach();
    }
    if (functionName.contains("UMG_EnterGame_C.BP_OnActivated"))
    {
        ClientLog("[LOGIN] EnterGame Activated forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000);
                PressSpace(); })
            .detach();
    }
    // Detect login completion after the native Construct returns below. This
    // prevents a queued direct travel from racing the final platform UI stack.
    const bool clientLoginCompletedEvent =
        functionName.contains("UMG_MainMenuBase_C.Construct");
    if (functionName.contains("OnConnectMatchServerTimeOut"))
    {
        ClientLog("[PE] " + std::string(Object->GetFullName()) + " - " + functionName);

        ConnectToMatch();
    }

    // 先执行原始 ProcessEvent，确保游戏状态已更新
    ProcessEventClient.call(Object, Function, Parms);
    if (clientLoginCompletedEvent)
        NotifyClientLoginCompleted();

    if (nativeIntroCompletionEvent && Object &&
        Object->IsA(APBGameState::StaticClass()))
    {
        // Do not recover in this same stack. PlayerCameraManager performs one
        // final AutoManage/ViewTarget update on its following camera tick.
        NotifyOwnedSeamlessIntroRoundBoundary();
    }

    if (nativeCameraSettleEvent && Object &&
        Object->IsA(APlayerCameraManager::StaticClass()))
    {
        // The first owning camera tick after round start has now returned, so
        // the intro camera can no longer overwrite the recovered ViewTarget.
        TryFinalizeOwnedSeamlessIntroCamera();
    }

    if (nativePlayableRestartEvent && Object &&
        Object->IsA(APBPlayerController::StaticClass()))
    {
        TryFinalizeNativeRespawnUi(
            static_cast<APBPlayerController*>(Object));
    }

    // Pipe and command-line match transitions are consumed only on this game thread.
    PumpPendingClientCommands();

    BattleLog::OnProcessEventPost(
        BattleLog::ProcessSide::Client,
        Object,
        functionName,
        Parms);
}

static SafetyHookInline ClientDeathCrash;
static SafetyHookInline ClientOnRepPlayerState;
static SafetyHookInline ClientTravelInputEligibility;
static SafetyHookMid ClientMatchStartUnavailableGameInstance;

void ClientOnRepPlayerStateHook(APBPlayerController* playerController)
{
    const bool ownedTravelWindow = IsOwnedTravelCompatibilityWindowActive();
    bool playerStateUsable = playerController && playerController->PlayerState;
    if (ownedTravelWindow && playerStateUsable)
    {
        try
        {
            playerStateUsable =
                playerController->PlayerState->IsA(APBPlayerState::StaticClass());
        }
        catch (...)
        {
            playerStateUsable = false;
        }
    }
    if (!DedicatedMultiMatchPolicy::ShouldUseInvalidPlayerStateTravelFallback(
            ownedTravelWindow, playerStateUsable))
    {
        ClientOnRepPlayerState.call<void>(playerController);
        return;
    }

    // Pinned APBPlayerController::OnRep_PlayerState starts by forwarding to
    // AController::OnRep_PlayerState at RVA 0x031E79B0.  Keep that native
    // replication notification, but do not enter the PB-only tail whose first
    // virtual dispatch dereferences the transient null PlayerState.
    using BaseOnRepPlayerStateFn = void(*)(APBPlayerController*);
    reinterpret_cast<BaseOnRepPlayerStateFn>(
        BaseAddress + 0x31E79B0)(playerController);
    playerController->PBPlayerState = nullptr;

    static std::atomic_bool logged = false;
    if (!logged.exchange(true))
    {
        ClientLog("[MULTIMATCH] Preserved native Engine OnRep_PlayerState during "
            "the seamless invalid-PlayerState phase.");
    }
}

bool ClientTravelInputEligibilityHook(APBPlayerController* playerController)
{
    bool gameInstanceAvailable = true;
    const bool ownedTravelWindow = IsOwnedTravelCompatibilityWindowActive();
    if (ownedTravelWindow)
    {
        // This is the checked resolver called by the pinned helper immediately
        // before its missing-null-check accessor at RVA 0x175D7D0.
        using ResolvePBGameInstanceFn = void*(__fastcall*)(
            APBPlayerController*);
        gameInstanceAvailable =
            reinterpret_cast<ResolvePBGameInstanceFn>(
                BaseAddress + 0x153FF10)(playerController) != nullptr;
    }
    if (!DedicatedMultiMatchPolicy::
            ShouldUseUnavailableGameInstanceTravelFallback(
                ownedTravelWindow, gameInstanceAvailable))
    {
        return ClientTravelInputEligibility.call<bool>(playerController);
    }

    static std::atomic_bool logged = false;
    if (!logged.exchange(true))
    {
        ClientLog("[MULTIMATCH] Suppressed transient seamless input query "
            "while PBGameInstance resolution was unavailable.");
    }
    return false;
}

void ClientMatchStartUnavailableGameInstanceHook(SafetyHookContext& context)
{
    // APBPlayerController::ClientMatchHasStarted at the pinned RVA calls the
    // same unchecked PBGameInstance accessor used by the input eligibility
    // helper. A null resolver becomes 0x380, then the instruction at 0x15A873D
    // reads +0x38 and crashes at 0x3B8. Preserve every earlier side effect and
    // the remainder of the native function; only substitute false for this
    // optional state byte during an explicitly owned seamless-travel window.
    const bool gameInstanceAvailable = context.rax != 0x380;
    if (!DedicatedMultiMatchPolicy::
            ShouldUseUnavailableGameInstanceTravelFallback(
                IsOwnedTravelCompatibilityWindowActive(),
                gameInstanceAvailable))
    {
        return;
    }

    // Let the original `movzx edi, byte ptr [rax+38h]` execute in sequence.
    // Jumping into the bytes adjacent to a mid-hook can land inside the hook's
    // patched instruction range. A stable zero object preserves the exact
    // native load and therefore gives the optional flag its safe false value.
    static const std::uint8_t UnavailableMatchStartState[0x39]{};
    context.rax = reinterpret_cast<std::uintptr_t>(
        UnavailableMatchStartState);

    static std::atomic_bool logged = false;
    if (!logged.exchange(true))
    {
        ClientLog("[MULTIMATCH] Preserved ClientMatchHasStarted while the "
            "destination PBGameInstance was transiently unavailable.");
    }
}

__int64 ClientDeathCrashHook(__int64 a1)
{
    return 0;
}

// ======================================================
//  SECTION 10 — HOOK DETOURS (MISC HOOKS)
// ======================================================

static SafetyHookInline ObjectNeedsLoad;

char ObjectNeedsLoadHook(UObject *a1)
{
    return 1;
}

static SafetyHookInline ActorNeedsLoad;

char ActorNeedsLoadHook(UObject *a1)
{
    return 1;
}

// Pinned APBPlayerController client-HUD helper at RVA 0x1584730. On a listen
// host it is invoked for remote controllers too. The native body obtains the
// ULocalPlayer at RVA 0x34FB080 but fails to validate the null result before
// inserting it into PBGameViewportClient's player-layer map; the later
// dereference at RVA 0x156193B reads null + 0x70 during NMT_Login.
static SafetyHookInline PlayerViewportLayerRequest;
static std::atomic_bool RemotePlayerViewportLayerGuardLogged{false};

void PlayerViewportLayerRequestHook(
    APlayerController* const playerController,
    APlayerController* const viewportOwner,
    const std::uint8_t layer,
    const std::uint8_t inputMode)
{
    using GetLocalPlayerFn = void*(__fastcall*)(APlayerController*);
    void* const localPlayer = viewportOwner
        ? reinterpret_cast<GetLocalPlayerFn>(BaseAddress + 0x34FB080)(viewportOwner)
        : nullptr;
    if (!ServerHookPolicy::ShouldForwardPlayerViewportLayerRequest(
            true, viewportOwner != nullptr, localPlayer != nullptr))
    {
        if (!RemotePlayerViewportLayerGuardLogged.exchange(true))
        {
            std::cout << "[LISTEN] Suppressed a remote PlayerController client "
                         "viewport-layer request."
                << std::endl;
        }
        return;
    }
    PlayerViewportLayerRequest.call<void>(
        playerController, viewportOwner, layer, inputMode);
}

static SafetyHookInline MessageBoxWHook;

int WINAPI MessageBoxW_Detour(HWND hWnd, LPCWSTR lpText, LPCWSTR lpCaption, UINT uType)
{
    if (lpText && wcsstr(lpText, L"Roboto"))
    {
        return IDOK;
    }
    return MessageBoxWHook.call<int>(hWnd, lpText, lpCaption, uType);
}

static SafetyHookInline HudFunctionThatCrashesTheGame;

__int64 HudFunctionThatCrashesTheGameHook(__int64 a1, __int64 a2)
{
    return 0;
}

static SafetyHookInline GameEngineTick;

__int64 GameEngineTickHook(APlayerController *a1,
                           float a2,
                           __int64 a3,
                           __int64 a4)
{

    static bool flip = true;

    flip = !flip;

    if (flip)
    {
        std::cout << "NO TICKY" << std::endl;
        return 0;
    }

    return GameEngineTick.call<__int64>(a1, a2, a3, a4);
}

static SafetyHookInline IsDedicatedServerHook;

bool IsDedicatedServer(void *WorldContextOrSomething)
{
    return true;
}

static SafetyHookInline IsServerHook;

bool IsServer(void *WorldContextOrSomething)
{
    return true;
}

static SafetyHookInline IsStandaloneHook;

bool IsStandalone(void *WorldContextOrSomething)
{
    return false;
}

// ======================================================
//  SECTION 11 — HOOK INITIALIZATION
// ======================================================

extern uintptr_t BaseAddress;
extern LibReplicate *libReplicate;

void InitMessageBoxHook()
{
    HMODULE user32 = GetModuleHandleA("user32.dll");
    if (!user32)
        return;

    void *addr = GetProcAddress(user32, "MessageBoxW");
    if (!addr)
        return;

    MessageBoxWHook = safetyhook::create_inline(addr, MessageBoxW_Detour);
}

bool InitStrictRosterAdmissionHooks(StrictRoster::Policy* policy)
{
    gStrictRosterNativeSeatPathReady.store(false, std::memory_order_release);
    if (!policy || BaseAddress == 0)
        return false;
    policy->SetNativeAdmissionPathReady(false);
    const void* const target = reinterpret_cast<const void*>(
        BaseAddress + kStrictRosterPreLoginRva);
    if (!MatchesPinnedBytes(
            kStrictRosterPreLoginRva,
            kStrictRosterPreLoginPrologue,
            sizeof(kStrictRosterPreLoginPrologue)))
    {
        std::cout << "[STRICT-ROSTER] Pinned PreLogin byte gate failed."
            << std::endl;
        return false;
    }
    if (!StrictRosterNativeSeatPathMatchesPinnedImage())
    {
        std::cout << "[STRICT-ROSTER] Pinned Team/Camp native byte gate failed."
            << std::endl;
        return false;
    }
    try
    {
        gStrictRosterPolicy = policy;
        gStrictRosterPreLoginHook = safetyhook::create_inline(
            const_cast<void*>(target), StrictRosterPreLogin);
    }
    catch (...)
    {
        gStrictRosterPolicy = nullptr;
        std::cout << "[STRICT-ROSTER] Pinned PreLogin hook installation failed."
            << std::endl;
        return false;
    }
    if (!gStrictRosterPreLoginHook)
    {
        gStrictRosterPolicy = nullptr;
        return false;
    }
    gStrictRosterNativeSeatPathReady.store(true, std::memory_order_release);
    std::cout << "[STRICT-ROSTER] Pinned PreLogin plus Team/Camp native paths "
                 "verified at boot; identity hook installed." << std::endl;
    return true;
}

void InitServerHooks(bool forceDedicatedMode)
{
    const ServerHookPolicy::InstallPlan installPlan =
        ServerHookPolicy::BuildInstallPlan(forceDedicatedMode);
    InitTravelDeferralHooks();
    std::cout << "[RESPAWN] explicit_native_forward="
        << (IsExplicitNativeRespawnForwardEnabled() ? "enabled" : "disabled")
        << " switch=-RespawnExplicitNative" << std::endl;
    NotifyActorDestroyed = safetyhook::create_inline((void *)(BaseAddress + 0x33403E0), NotifyActorDestroyedHook);
    ServerInGameMenuTransition = safetyhook::create_inline(
        (void *)(BaseAddress + 0x15C8CF0), ServerInGameMenuTransitionHook);
    ServerEndMatch = safetyhook::create_inline(
        (void *)(BaseAddress + 0x162ABD0), ServerEndMatchHook);
    ServerNullResultMvp = safetyhook::create_mid(
        (void*)(BaseAddress + 0x163787B), ServerNullResultMvpHook);
    ServerConfirmRoleSelectionValidate = safetyhook::create_inline(
        (void *)(BaseAddress + 0x15C09C0),
        ServerConfirmRoleSelectionValidateHook);
    ServerRoleConfirmationRestartPlayer = safetyhook::create_inline(
        (void *)(BaseAddress + 0x163D250),
        ServerRoleConfirmationRestartPlayerHook);
    ServerStartShowingMatchResult = safetyhook::create_inline(
        (void *)(BaseAddress + 0x162B190), ServerStartShowingMatchResultHook);
    ServerStartWaitingToEndGame = safetyhook::create_inline(
        (void *)(BaseAddress + 0x162B1C0), ServerStartWaitingToEndGameHook);
    ServerBeginFinalCleanup = safetyhook::create_inline(
        (void *)(BaseAddress + 0x163EFD0), ServerBeginFinalCleanupHook);
    NotifyAcceptingConnection = safetyhook::create_inline((void *)(BaseAddress + 0x36CDC90), NotifyAcceptingConnectionHook);
    NotifyControlMessage = safetyhook::create_inline((void *)(BaseAddress + 0x36CDCE0), NotifyControlMessageHook);
    TickFlush = safetyhook::create_inline((void *)(BaseAddress + 0x33E05F0), TickFlushHook);
    // UGameEngine::Browse is vtable +0x450 in all three engine vtables that
    // contain TickWorldTravel at +0x458 in the pinned image.
    EngineBrowse = safetyhook::create_inline(
        (void *)(BaseAddress + 0x36664D0), EngineBrowseHook);
    ProcessEvent = safetyhook::create_inline((void *)(BaseAddress + 0x1BCBE40), ProcessEventHook);
    if (installPlan.ForceServerOnlyObjectLoading)
    {
        ObjectNeedsLoad = safetyhook::create_inline(
            (void *)(BaseAddress + 0x1B7B710), ObjectNeedsLoadHook);
        ActorNeedsLoad = safetyhook::create_inline(
            (void *)(BaseAddress + 0x3124E70), ActorNeedsLoadHook);
    }
    std::cout << "[SERVER] server-only-load-overrides="
        << (installPlan.ForceServerOnlyObjectLoading ? "enabled" : "native")
        << std::endl;
    if (installPlan.GuardRemotePlayerViewportLayers)
    {
        PlayerViewportLayerRequest = safetyhook::create_inline(
            (void *)(BaseAddress + 0x1584730), PlayerViewportLayerRequestHook);
    }
    std::cout << "[SERVER] remote-player-viewport-guard="
        << (installPlan.GuardRemotePlayerViewportLayers ? "enabled" : "disabled")
        << std::endl;
    OnFireWeaponHook = safetyhook::create_inline((void *)(BaseAddress + 0x1610500), OnFireWeapon);
    PostLoginHook = safetyhook::create_inline((void *)(BaseAddress + 0x32903B0), PostLogin);
    if (installPlan.ForceDedicatedNetMode)
    {
        IsDedicatedServerHook = safetyhook::create_inline((void *)(BaseAddress + 0x33266F0), IsDedicatedServer);
        IsServerHook = safetyhook::create_inline((void *)(BaseAddress + 0x3326C60), IsServer);
        IsStandaloneHook = safetyhook::create_inline((void *)(BaseAddress + 0x3326CE0), IsStandalone);
    }
}

void InitClientArchiveHooks()
{
    ClientDeathCrash = safetyhook::create_inline((void *)(BaseAddress + 0x16abe10), ClientDeathCrashHook);
    FixEquipErrorHook = safetyhook::create_inline((void *)(BaseAddress + 0x16DD080), FixEquipErrorHookFn);
    FixCharacterSkinPaintingErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DCEC0), FixCharacterSkinPaintingErrorHookFn);
    FixCharacterAppearanceErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DCD80), FixCharacterAppearanceErrorHookFn);
    FixWeaponOrnamentErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DD1D0), FixWeaponOrnamentErrorHookFn);
    FixWeaponPartSkinPaintingErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DD490), FixWeaponPartSkinPaintingErrorHookFn);
    FixWeaponPartSlotErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DD5F0), FixWeaponPartSlotErrorHookFn);
    FixWeaponSuiteErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DD740), FixWeaponSuiteErrorHookFn);
    ClientLog("[ARCHIVE] Installed pinned-build completion compatibility hooks "
        "(character/weapon customization 404->0; equipment 404/9002->0).");
}

void InitClientHook()
{
    InitTravelDeferralHooks();
    ClientOnRepPlayerState = safetyhook::create_inline(
        (void*)(BaseAddress + 0x15BACC0), ClientOnRepPlayerStateHook);
    ClientTravelInputEligibility = safetyhook::create_inline(
        (void*)(BaseAddress + 0x15A60D0), ClientTravelInputEligibilityHook);
    ClientMatchStartUnavailableGameInstance = safetyhook::create_mid(
        (void*)(BaseAddress + 0x15A873D),
        ClientMatchStartUnavailableGameInstanceHook);
    ProcessEventClient = safetyhook::create_inline(
        (void *)(BaseAddress + 0x1BCBE40), ProcessEventHookClient);
    InitClientArchiveHooks();
}
