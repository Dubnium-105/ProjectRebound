// Main.cpp
#include <Windows.h>
#include <wincrypt.h>
#include <array>
#include <atomic>
#include <chrono>
#include <charconv>
#include <condition_variable>
#include <cstdint>
#include <cstring>
#include <thread>
#include <fstream>
#include <filesystem>
#include <iostream>
#include <iomanip>
#include <memory>
#include <mutex>
#include <optional>
#include <sstream>
#include <string_view>

#include "SDK.hpp"
#include "Network/NetDriverAccess.h"
#include "SDK/Engine_parameters.hpp"
#include "SDK/ProjectBoundary_parameters.hpp"
#include "safetyhook/safetyhook.hpp"
#include "Libs/json.hpp"
#include "Replication/libreplicate.h"
#include "ServerLogic/LateJoinManager.h"
#include "ServerLogic/DedicatedMultiMatch.h"
#include "Communication/CommandFramework.h"
#include "Admission/Ed25519Verifier.h"
#include "Admission/StrictRosterAdmissionGate.h"
#include "Admission/StrictAuthorityStartDispatch.h"
#include "Admission/StrictAuthorityLease.h"
#include "Admission/StrictRosterCleanupReceipt.h"
#include "Admission/StrictRosterPolicy.h"
#include "Loadout/LoadoutManager.h"

#include "Config/Config.h"
#include "Config/CommandLinePolicy.h"
#include "Debug/Debug.h"
#include "Debug/DebugTool.h"
#include "ServerLogic/ServerLogic.h"
#include "ClientLogic/ClientLogic.h"
#include "ClientLogic/LocalQosDiscoveryPolicy.h"
#include "Hooks/Hooks.h"
#include "Hooks/StrictRosterSteamAuth.h"
#include "Network/Network.h"
#include "Utility/Utility.h"

using namespace SDK;
// ======================================================
//  SECTION 3 — GLOBAL VARIABLES (now owned by Main)
// ======================================================

uintptr_t BaseAddress = 0x0;
LibReplicate* libReplicate = nullptr; // was static in original, but extern needed by other modules
HMODULE gPayloadModule = nullptr;
static CommandFramework* g_CmdFramework = nullptr;
static std::mutex g_CmdFrameworkMutex;
DebugTool* gDebugTool = nullptr;
LoadoutManager* gLoadoutManager = nullptr;
std::recursive_mutex gLoadoutManagerMutex;
StrictRoster::Policy gStrictRosterPolicy(StrictRoster::VerifyEd25519, false);

nlohmann::json ProcessStrictRosterClearMatchAllocationResult(
    const nlohmann::json& arguments);

namespace
{
constexpr char kSupportedExecutableSha256[] =
    "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843";
// v2 adds the per-native-handshake nonce to every admission reservation,
// confirmation, release, and connection event.  The admission mode remains
// strict_roster_v1; this is the Payload/IPC wire version.
constexpr char kStrictRosterPayloadVersion[] = "strict-roster-v2";
constexpr DWORD kSupportedExecutableImageSize = 105431040;
constexpr uintptr_t kGetNetModeRva = 0x036CC300;
constexpr uintptr_t kQosDiscoveryFStringRva = 0x05C63C88;
constexpr uintptr_t kQosDiscoveryInitializerRva = 0x0068ADE0;
constexpr uintptr_t kRpcFramePatchPageOffset = 0x009C3000;
constexpr SIZE_T kRpcFramePatchPageSize = 0x1000;

std::string gVerifiedExecutableHash;
std::mutex gStrictAuthorityStartMutex;
std::atomic_bool gStrictAuthorityRuntimeReady{false};
std::atomic_bool gStrictNativeHooksReady{false};
std::atomic_bool gStrictAuthorityServerStarted{false};
std::atomic_bool gStrictAuthorityAwaitingWorldTeardown{false};
// This is a game-thread observation.  Pipe callbacks may read it, but only
// the engine tick updates it after inspecting UWorld/NetDriver.
std::atomic_bool gStrictAuthorityWorldListening{false};
std::atomic<int> gNativeNetModeSnapshot{-1};
// Serializes allocation installation against an authority start transaction.
// It stays held by the pipe callback while the scoped request is waiting, so a
// late game-thread completion cannot be followed by a new allocation install.
std::atomic_bool gStrictAuthorityMutationInFlight{false};

// CommandFramework owns a worker/listener thread, but StartServer touches
// UWorld, executes the native travel command, and creates the NetDriver.  It
// must therefore run at the game-thread boundary.  Keep one scoped request so
// a second pipe caller cannot race a different native travel or reuse a stale
// response.
struct StrictAuthorityStartRequest
    : StrictAuthorityStartDispatch::Request
{
    std::string hostingKind;
    std::string nativeConnectionNonce;
    std::string worldInstanceId;
    StrictRoster::AllocationScope allocationScope;
    nlohmann::json result;
    nlohmann::json cancellationResult;
    nlohmann::json successResult;
    // These fields belong exclusively to the game-thread producer. They
    // survive listener timeout so a later tick can finish scoped cleanup.
    bool nativeTravelRequested = false;
    UWorld* previousWorld = nullptr;
    UWorld* streamingWorld = nullptr;
    std::string streamingWorldInstanceId;
    std::chrono::steady_clock::time_point worldDeadline{};
};

StrictAuthorityStartDispatch::Queue gStrictAuthorityStartQueue;

struct StrictRosterClearRequest
    : StrictAuthorityStartDispatch::Request
{
    nlohmann::json arguments;
    nlohmann::json result;
};

StrictAuthorityStartDispatch::Queue gStrictRosterClearQueue;

std::mutex gStrictAuthorityWorldMutex;
UWorld* gStrictAuthorityWorld = nullptr;
UWorld* gObservedAuthorityWorld = nullptr;
UNetDriver* gObservedAuthorityNetDriver = nullptr;
std::chrono::steady_clock::time_point gAuthorityWorldObservedAt{};
std::string gStrictAuthorityWorldInstanceId;
std::uint64_t gStrictAuthorityWorldSequence = 0;

struct StrictRosterCleanupState
{
    bool pending = false;
    bool teardownRequested = false;
    bool processExitRequested = false;
    std::string attemptId;
    std::string authoritySessionId;
    std::string worldInstanceId;
    std::int64_t rosterRevision = 0;
    int routeGeneration = 0;
    UWorld* retiredWorld = nullptr;
    UNetDriver* retiredNetDriver = nullptr;
    bool quarantined = false;
};

struct StrictAuthorityCleanupPlan
{
    StrictRosterCleanupState state;
    std::string reason;
};

// A successful authority start is also the idempotency record for a retried
// start/ACK request. The same allocation/world must replay the original native
// handshake nonce; generating a fresh nonce while reusing the live world would
// let an old response and the new response describe different handshakes.
struct StrictAuthorityReadyLease
{
    bool active = false;
    std::string hostingKind;
    std::string nativeConnectionNonce;
    std::string worldInstanceId;
    std::uint64_t clientOperationSequence = 0;
    std::optional<StrictRoster::AllocationScope> allocationScope;
    UWorld* world = nullptr;
    UNetDriver* netDriver = nullptr;
};

StrictAuthorityReadyLease gStrictAuthorityReadyLease;
std::optional<StrictAuthorityCleanupPlan> gStrictAuthorityFailedStageCleanup;

using StrictAuthorityMutationLease = StrictAuthorityLease::MutationLease;

std::mutex gStrictRosterCleanupMutex;
StrictRosterCleanupState gStrictRosterCleanup;
StrictRosterCleanupReceipt::Journal gStrictRosterCleanupReceiptJournal;

std::int64_t EpochSecondsNow() noexcept
{
    return std::chrono::duration_cast<std::chrono::seconds>(
        std::chrono::system_clock::now().time_since_epoch()).count();
}

std::string ObserveStrictAuthorityWorld(UWorld* const world)
{
    if (!world)
        return {};
    std::string instanceId;
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
        if (gStrictAuthorityWorld == world && !gStrictAuthorityWorldInstanceId.empty())
            instanceId = gStrictAuthorityWorldInstanceId;
        else
        {
            gStrictAuthorityWorld = world;
            ++gStrictAuthorityWorldSequence;
            std::ostringstream id;
            id << "world_" << std::hex << gStrictAuthorityWorldSequence << "_"
               << reinterpret_cast<std::uintptr_t>(world);
            gStrictAuthorityWorldInstanceId = id.str();
            instanceId = gStrictAuthorityWorldInstanceId;
        }
    }
    gStrictRosterPolicy.SetNativeWorldInstanceId(instanceId);
    return instanceId;
}

std::string CurrentStrictAuthorityWorldInstanceId()
{
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    return gStrictAuthorityWorldInstanceId;
}

bool CurrentStrictAuthorityWorldMatches(const StrictAuthorityReadyLease& lease)
{
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    return StrictAuthorityLease::OwnedWorldMatches(
        gObservedAuthorityWorld, gStrictAuthorityWorldInstanceId, lease.world, lease.worldInstanceId) &&
        gStrictAuthorityWorld == lease.world && lease.netDriver &&
        gObservedAuthorityNetDriver == lease.netDriver &&
        gAuthorityWorldObservedAt != std::chrono::steady_clock::time_point{} &&
        std::chrono::steady_clock::now() - gAuthorityWorldObservedAt <= std::chrono::seconds(2);
}

void ClearStrictAuthorityWorldIdentity(const UWorld* expectedWorld)
{
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    if (!expectedWorld || gStrictAuthorityWorld == expectedWorld)
    {
        gStrictAuthorityWorld = nullptr;
        gObservedAuthorityWorld = nullptr;
        gObservedAuthorityNetDriver = nullptr;
        gAuthorityWorldObservedAt = {};
        gStrictAuthorityWorldInstanceId.clear();
    }
}

bool StrictRosterCleanupScopeEquals(
    const StrictRosterCleanupState& cleanup,
    const std::string_view attemptId,
    const std::string_view authoritySessionId,
    const std::string_view worldInstanceId,
    const std::int64_t rosterRevision,
    const int routeGeneration)
{
    return cleanup.pending && cleanup.attemptId == attemptId &&
        cleanup.authoritySessionId == authoritySessionId &&
        cleanup.worldInstanceId == worldInstanceId &&
        cleanup.rosterRevision == rosterRevision &&
        cleanup.routeGeneration == routeGeneration;
}

StrictRosterNativeTeardownRequestResult RequestScopedStrictRosterWorldTeardown(
    const StrictRosterCleanupState& cleanup)
{
    UWorld* const world = UWorld::GetWorld();
    const bool ownsWorld = StrictAuthorityLease::OwnedWorldMatches(
        world, CurrentStrictAuthorityWorldInstanceId(),
        cleanup.retiredWorld, cleanup.worldInstanceId);
    if (cleanup.quarantined || !ownsWorld ||
        (cleanup.retiredNetDriver && world->NetDriver != cleanup.retiredNetDriver))
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        if (StrictRosterCleanupScopeEquals(
                gStrictRosterCleanup, cleanup.attemptId, cleanup.authoritySessionId,
                cleanup.worldInstanceId, cleanup.rosterRevision, cleanup.routeGeneration))
            gStrictRosterCleanup.quarantined = true;
        return StrictRosterNativeTeardownRequestResult::NotRequested;
    }
    return RequestStrictRosterNativeWorldTeardown();
}

bool StrictNativeWorldTeardownComplete()
{
    StrictRosterCleanupState cleanup;
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        cleanup = gStrictRosterCleanup;
    }
    if (!cleanup.pending || cleanup.quarantined)
        return false;

    // A dispatched StartServer/clear path with no safely captured UObject or
    // NetDriver has no local evidence of teardown.  An empty snapshot is not
    // proof that the native world was never created; only the dedicated
    // supervisor's process/creation-time proof can close that case.
    if (!cleanup.retiredWorld && !cleanup.retiredNetDriver)
        return false;

    // Dedicated process-per-match cleanup is intentionally terminal from the
    // Payload's point of view.  The old process may exit before another pipe
    // frame can be served; only the external supervisor can prove the old
    // PID plus creation-time is gone and register a fresh instance.  Never
    // turn a local world observation into NativeCleared for this mode.
    if (cleanup.processExitRequested)
        return false;

    UWorld* const currentWorld = UWorld::GetWorld();
    if (cleanup.retiredWorld && currentWorld == cleanup.retiredWorld)
        return false;

    if (cleanup.retiredWorld && UObject::GObjects)
    {
        // A different current world is a generation change, but do not claim
        // teardown while the retired UObject is still owned by the native
        // object array.  The comparison is pointer-only; no stale world is
        // dereferenced after native destruction starts.
        for (UObject* const object : getObjectsOfClass(UWorld::StaticClass(), false))
        {
            if (object == cleanup.retiredWorld)
                return false;
        }
    }

    if (cleanup.retiredNetDriver)
    {
        if (currentWorld && currentWorld->NetDriver == cleanup.retiredNetDriver)
            return false;
        // Do not ask NetDriverAccess to resolve its cached binding here.  Its
        // no-scan path may inspect the cached driver pointer, which is exactly
        // the object whose destruction this observer is waiting to prove.
        // The current World reference and the native object-array scan below
        // are the safe observations after teardown has started.
        for (UNetDriver* const driver : NetDriverAccess::SnapshotNetDrivers())
        {
            if (driver == cleanup.retiredNetDriver)
                return false;
        }
    }
    return true;
}

int GetNativeNetModeInternal(UWorld* world);
const char* NetModeName(int mode);

bool ParseAuthorityTarget(
    const std::string_view target,
    std::string& endpointHost,
    int& endpointPort) noexcept
{
    std::string failureReason;
    const auto parsed = CommandProtocol::ParseMatchTarget(target, &failureReason);
    if (!parsed)
        return false;
    endpointHost = parsed->host;
    endpointPort = parsed->port;
    return true;
}

nlohmann::json PolicyResult(const StrictRoster::Decision& decision)
{
    return nlohmann::json{
        {"accepted", decision.accepted},
        {"code", decision.code},
        {"message", decision.message}
    };
}

nlohmann::json BuildPayloadStatus()
{
    const std::string commandLine = GetCommandLineA();
    const bool offlinePve =
        StrictRosterAdmissionGate::IsExplicitOfflinePve(commandLine);
    const bool executableVerified =
        gVerifiedExecutableHash == kSupportedExecutableSha256;
    const bool nativeAuthorityPath =
        gStrictNativeHooksReady.load(std::memory_order_acquire);
    // The client-side Grant injection point is intentionally a separate
    // capability. A verified PreLogin hook on the authority does not prove
    // that a member's JWT reaches NMT_Login.
    const bool nativeClientGrantInjection =
        IsStrictRosterNativeClientGrantInjectionReady();
    // This remains a separate locked-build capability bit.  It is deliberately
    // independent of a current allocation/world so an idle authority can be
    // checked without a scheduler self-lock. The complete native admission
    // chain for this fixed image still needs a real-game execution receipt;
    // component tests for Steam proof and Team/Camp hooks do not establish it.
    constexpr bool nativeAuthorityAdmissionVerified = false;
    // Dedicated authority has no local client archive/NMT grant injector;
    // requiring that client-only capability here would self-block a valid
    // dedicated authority. Listen/P2P roles still require both native sides.
    const bool nativeClientGrantInjectionRequired =
        !(amServer && !amListenServer &&
            CommandLinePolicy::HasExactSwitch(commandLine, "-server"));
    const bool nativeAuthorityPathRequired = amServer;
    const bool strictReady = StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        executableVerified, offlinePve, nativeAuthorityPath,
        nativeAuthorityAdmissionVerified, nativeClientGrantInjection,
        nativeClientGrantInjectionRequired, nativeAuthorityPathRequired);
    const bool payloadReady = StrictRosterAdmissionGate::CanReportPayloadReady(
        executableVerified, strictReady, offlinePve);
    // The pipe worker must not dereference a UWorld while the game thread
    // replaces or destroys it. Before the first observed tick this is invalid.
    const int netMode = gNativeNetModeSnapshot.load(std::memory_order_acquire);
    const nlohmann::json clientMatch = GetClientMatchStatus();
    return nlohmann::json{
        {"status", payloadReady ? "ready" : "blocked"},
        {"ready", payloadReady},
        {"code", executableVerified
            ? (payloadReady ? "ready" : "native_admission_unverified")
            : "game_binary_unverified"},
        {"protocol_version", kStrictRosterPayloadVersion},
        {"game_binary_sha256", gVerifiedExecutableHash},
        {"net_mode", NetModeName(netMode)},
        {"native_authority_path_ready", nativeAuthorityPath},
        {"native_authority_path_required", nativeAuthorityPathRequired},
        {"native_client_grant_injection_ready", nativeClientGrantInjection},
        {"native_client_grant_injection_required", nativeClientGrantInjectionRequired},
        {"native_authority_admission_verified", nativeAuthorityAdmissionVerified},
        {"strict_online_ready", strictReady},
        {"offline_pve", offlinePve},
        {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()},
        {"match", clientMatch}
    };
}

struct NativeRpcPatch
{
    uintptr_t offset;
    const uint8_t* expected;
    const uint8_t* replacement;
    size_t size;
};

constexpr uint8_t kLengthGuardExpected[] = {0x81, 0xFE, 0x00, 0x00, 0x10, 0x00};
constexpr uint8_t kLengthGuardReplacement[] = {0x81, 0xFE, 0x00, 0x00, 0x20, 0x00};
constexpr uint8_t kOutputAllocationExpected[] = {0xBA, 0x0A, 0x00, 0x10, 0x00};
constexpr uint8_t kOutputAllocationReplacement[] = {0xBA, 0x0A, 0x00, 0x20, 0x00};
constexpr uint8_t kOutputCapacityExpected[] = {0x8D, 0x83, 0x0A, 0x00, 0x10, 0x00};
constexpr uint8_t kOutputCapacityReplacement[] = {0x8D, 0x83, 0x0A, 0x00, 0x20, 0x00};
constexpr uint8_t kOutputClearExpected[] = {0x41, 0xB8, 0x0A, 0x00, 0x10, 0x00};
constexpr uint8_t kOutputClearReplacement[] = {0x41, 0xB8, 0x0A, 0x00, 0x20, 0x00};

constexpr NativeRpcPatch kNativeRpcPatches[] = {
    {0x009C37BB, kLengthGuardExpected, kLengthGuardReplacement,
        sizeof(kLengthGuardExpected)},
    {0x009C3B47, kOutputAllocationExpected, kOutputAllocationReplacement,
        sizeof(kOutputAllocationExpected)},
    {0x009C3B68, kOutputCapacityExpected, kOutputCapacityReplacement,
        sizeof(kOutputCapacityExpected)},
    {0x009C3B87, kOutputClearExpected, kOutputClearReplacement,
        sizeof(kOutputClearExpected)},
};

constexpr uint8_t kQosInitializerExpected[] = {
    0x48, 0x83, 0xEC, 0x28, 0xBA, 0x51, 0x00, 0x00,
    0x00, 0x48, 0x8D, 0x0D, 0x98, 0x8E, 0x5D, 0x05,
};
constexpr wchar_t kOriginalQosDiscoveryUrl[] =
    L"https://qos.multiplay.com/v1/fleets/59e74bef-4124-464e-ac31-1a001c070829/servers";

struct RawFString
{
    wchar_t* Data;
    int32_t Num;
    int32_t Max;
};
static_assert(sizeof(RawFString) == 16);

enum class QosPatchResult : int
{
    Pending,
    Success,
    Failure,
};

SafetyHookInline gQosInitializerHook{};
std::wstring gLocalQosDiscoveryUrl;
std::atomic<QosPatchResult> gQosPatchResult{QosPatchResult::Pending};

bool IsWritableRange(const void* address, size_t size)
{
    if (!address || size == 0)
        return false;
    MEMORY_BASIC_INFORMATION information{};
    if (VirtualQuery(address, &information, sizeof(information)) != sizeof(information) ||
        information.State != MEM_COMMIT ||
        (information.Protect & (PAGE_GUARD | PAGE_NOACCESS)) != 0)
    {
        return false;
    }
    const DWORD protection = information.Protect & 0xFF;
    const bool writable = protection == PAGE_READWRITE || protection == PAGE_WRITECOPY ||
        protection == PAGE_EXECUTE_READWRITE || protection == PAGE_EXECUTE_WRITECOPY;
    const uintptr_t begin = reinterpret_cast<uintptr_t>(address);
    const uintptr_t regionEnd = reinterpret_cast<uintptr_t>(information.BaseAddress) +
        information.RegionSize;
    return writable && begin <= regionEnd && size <= regionEnd - begin;
}

QosPatchResult TryPatchQosDiscoveryUrl()
{
    auto* value = reinterpret_cast<RawFString*>(BaseAddress + kQosDiscoveryFStringRva);
    if (!value->Data || value->Num <= 0 || value->Max <= 0)
        return QosPatchResult::Pending;
    if (value->Num > value->Max || value->Max > 4096 || gLocalQosDiscoveryUrl.empty())
        return QosPatchResult::Failure;
    const size_t capacity = static_cast<size_t>(value->Max);
    if (!IsWritableRange(value->Data, capacity * sizeof(wchar_t)))
        return QosPatchResult::Failure;
    const size_t currentLength = wcsnlen_s(value->Data, capacity);
    if (currentLength == capacity)
        return QosPatchResult::Failure;
    const std::wstring_view current(value->Data, currentLength);
    if (current == gLocalQosDiscoveryUrl)
        return QosPatchResult::Success;
    if (current != kOriginalQosDiscoveryUrl || gLocalQosDiscoveryUrl.size() + 1 > capacity)
        return QosPatchResult::Failure;

    std::memcpy(value->Data, gLocalQosDiscoveryUrl.data(),
        gLocalQosDiscoveryUrl.size() * sizeof(wchar_t));
    value->Data[gLocalQosDiscoveryUrl.size()] = L'\0';
    value->Num = static_cast<int32_t>(gLocalQosDiscoveryUrl.size() + 1);
    std::atomic_thread_fence(std::memory_order_seq_cst);
    const size_t verifiedLength = wcsnlen_s(value->Data, capacity);
    return verifiedLength == gLocalQosDiscoveryUrl.size() &&
        value->Num == static_cast<int32_t>(verifiedLength + 1) &&
        std::wstring_view(value->Data, verifiedLength) == gLocalQosDiscoveryUrl
        ? QosPatchResult::Success
        : QosPatchResult::Failure;
}

void QosDiscoveryInitializerDetour()
{
    gQosInitializerHook.call<void>();
    gQosPatchResult.store(TryPatchQosDiscoveryUrl(), std::memory_order_release);
}

bool SignalLocalQosReady(std::string_view eventName)
{
    const std::wstring wide(eventName.begin(), eventName.end());
    HANDLE event = OpenEventW(EVENT_MODIFY_STATE, FALSE, wide.c_str());
    if (!event)
        return false;
    const bool signaled = SetEvent(event) != FALSE;
    CloseHandle(event);
    return signaled;
}

bool ApplyLocalPveQosRedirect(const std::string& commandLine)
{
    const auto decision = LocalQosDiscoveryPolicy::Evaluate(commandLine);
    if (decision.state == LocalQosDiscoveryPolicy::State::Disabled)
        return true;
    if (decision.state == LocalQosDiscoveryPolicy::State::Invalid)
    {
        ClientLog("[LOCAL-QOS] Refusing invalid opt-in: " + decision.error);
        return false;
    }

    gLocalQosDiscoveryUrl.assign(
        decision.discoveryUrl.begin(), decision.discoveryUrl.end());
    QosPatchResult result = TryPatchQosDiscoveryUrl();
    if (result == QosPatchResult::Pending)
    {
        const void* initializer = reinterpret_cast<const void*>(
            BaseAddress + kQosDiscoveryInitializerRva);
        if (std::memcmp(initializer, kQosInitializerExpected,
            sizeof(kQosInitializerExpected)) != 0)
        {
            ClientLog("[LOCAL-QOS] Refusing initializer hook: instruction guard mismatch.");
            return false;
        }
        gQosPatchResult.store(QosPatchResult::Pending, std::memory_order_release);
        gQosInitializerHook = safetyhook::create_inline(
            reinterpret_cast<void*>(BaseAddress + kQosDiscoveryInitializerRva),
            QosDiscoveryInitializerDetour);
        if (!gQosInitializerHook)
        {
            ClientLog("[LOCAL-QOS] Failed to install the temporary initializer hook.");
            return false;
        }

        const ULONGLONG deadline = GetTickCount64() + 15000;
        while (GetTickCount64() < deadline)
        {
            result = gQosPatchResult.load(std::memory_order_acquire);
            if (result == QosPatchResult::Pending)
            {
                const QosPatchResult observed = TryPatchQosDiscoveryUrl();
                if (observed == QosPatchResult::Success)
                {
                    result = observed;
                    gQosPatchResult.store(result, std::memory_order_release);
                }
            }
            if (result != QosPatchResult::Pending)
                break;
            Sleep(1);
        }
        // Give a detour that just published its result time to return through
        // the trampoline before restoring the initializer bytes.
        Sleep(10);
        gQosInitializerHook.reset();
    }

    if (result != QosPatchResult::Success)
    {
        ClientLog("[LOCAL-QOS] QoS discovery FString did not pass guarded readback.");
        return false;
    }
    if (!SignalLocalQosReady(decision.readyEvent))
    {
        ClientLog("[LOCAL-QOS] Failed to signal the Toolbox readiness event.");
        return false;
    }
    ClientLog("[LOCAL-QOS] Guarded loopback QoS discovery redirect is active.");
    return true;
}

bool HashExecutable(std::string& digest)
{
    std::array<wchar_t, 32768> path{};
    const DWORD pathLength = GetModuleFileNameW(nullptr, path.data(),
        static_cast<DWORD>(path.size()));
    if (pathLength == 0 || pathLength >= path.size())
        return false;

    HANDLE file = CreateFileW(path.data(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_DELETE,
        nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (file == INVALID_HANDLE_VALUE)
        return false;

    HCRYPTPROV provider = 0;
    HCRYPTHASH hash = 0;
    bool success = CryptAcquireContextW(&provider, nullptr, nullptr, PROV_RSA_AES,
        CRYPT_VERIFYCONTEXT) != FALSE;
    if (success)
        success = CryptCreateHash(provider, CALG_SHA_256, 0, 0, &hash) != FALSE;

    std::array<BYTE, 64 * 1024> buffer{};
    while (success)
    {
        DWORD bytesRead = 0;
        if (!ReadFile(file, buffer.data(), static_cast<DWORD>(buffer.size()),
            &bytesRead, nullptr))
        {
            success = false;
            break;
        }
        if (bytesRead == 0)
            break;
        success = CryptHashData(hash, buffer.data(), bytesRead, 0) != FALSE;
    }

    std::array<BYTE, 32> hashBytes{};
    DWORD hashSize = static_cast<DWORD>(hashBytes.size());
    if (success)
        success = CryptGetHashParam(hash, HP_HASHVAL, hashBytes.data(), &hashSize, 0) != FALSE &&
            hashSize == hashBytes.size();

    if (hash != 0)
        CryptDestroyHash(hash);
    if (provider != 0)
        CryptReleaseContext(provider, 0);
    CloseHandle(file);
    if (!success)
        return false;

    std::ostringstream output;
    output << std::hex << std::setfill('0');
    for (BYTE value : hashBytes)
        output << std::setw(2) << static_cast<unsigned int>(value);
    digest = output.str();
    return true;
}

bool VerifySupportedExecutable(uintptr_t moduleBase, std::string& executableHash)
{
    const auto* dosHeader = reinterpret_cast<const IMAGE_DOS_HEADER*>(moduleBase);
    if (!dosHeader || dosHeader->e_magic != IMAGE_DOS_SIGNATURE)
        return false;
    const auto* ntHeaders = reinterpret_cast<const IMAGE_NT_HEADERS64*>(
        moduleBase + dosHeader->e_lfanew);
    if (!ntHeaders || ntHeaders->Signature != IMAGE_NT_SIGNATURE ||
        ntHeaders->OptionalHeader.SizeOfImage != kSupportedExecutableImageSize)
    {
        return false;
    }
    return HashExecutable(executableHash) &&
        executableHash == kSupportedExecutableSha256;
}

int GetNativeNetModeInternal(UWorld* world)
{
    if (!world || BaseAddress == 0) return -1;
    using GetNetModeFn = int(__fastcall*)(const UWorld*);
    const auto getNetMode = reinterpret_cast<GetNetModeFn>(
        BaseAddress + kGetNetModeRva);
    try { return getNetMode(world); }
    catch (...) { return -1; }
}

const char* NetModeName(int mode)
{
    switch (mode)
    {
    case 0: return "standalone";
    case 1: return "dedicated";
    case 2: return "listen";
    case 3: return "client";
    default: return "invalid";
    }
}

bool IsAuthoritativeListeningWorld(UWorld* world, std::string& detail)
{
    NetDriverAccess::Snapshot snapshot{};
    const bool hasSnapshot =
        NetDriverAccess::TryGetSnapshot(snapshot, false);
    const bool hasAuthorityGameMode = world && world->AuthorityGameMode;
    const bool worldMatches = hasSnapshot && snapshot.World == world &&
        snapshot.WorldMatches;
    const bool listeningDriver = hasSnapshot &&
        snapshot.NetDriver && snapshot.ServerConnection == nullptr;
    const bool listening = listeningDriver;

    std::ostringstream output;
    output << "authority_game_mode=" << (hasAuthorityGameMode ? 1 : 0)
           << " net_driver=" << (hasSnapshot && snapshot.NetDriver ? 1 : 0)
           << " world_matches=" << (worldMatches ? 1 : 0)
           << " server_connection="
           << (hasSnapshot && snapshot.ServerConnection ? 1 : 0)
           << " listening=" << (listening ? 1 : 0);
    detail = output.str();
    return hasAuthorityGameMode && worldMatches && listeningDriver && listening;
}

bool ApplyNativeRpcFrameLimitPatch(uintptr_t moduleBase)
{
    std::string executableHash;
    if (!VerifySupportedExecutable(moduleBase, executableHash))
    {
        ClientLog("[NATIVE-RPC] Refusing frame patch: executable SHA-256 mismatch.");
        return false;
    }

    bool allExpected = true;
    bool allPatched = true;
    for (const NativeRpcPatch& patch : kNativeRpcPatches)
    {
        const void* address = reinterpret_cast<const void*>(moduleBase + patch.offset);
        allExpected = allExpected && std::memcmp(address, patch.expected, patch.size) == 0;
        allPatched = allPatched && std::memcmp(address, patch.replacement, patch.size) == 0;
    }
    if (allPatched)
    {
        ClientLog("[NATIVE-RPC] Two-megabyte frame limit already active.");
        return true;
    }
    if (!allExpected)
    {
        ClientLog("[NATIVE-RPC] Refusing frame patch: instruction guard mismatch.");
        return false;
    }

    void* patchPage = reinterpret_cast<void*>(moduleBase + kRpcFramePatchPageOffset);
    DWORD oldProtection = 0;
    if (!VirtualProtect(patchPage, kRpcFramePatchPageSize, PAGE_EXECUTE_READWRITE,
        &oldProtection))
    {
        ClientLog("[NATIVE-RPC] Frame patch failed: VirtualProtect denied the patch page.");
        return false;
    }

    for (const NativeRpcPatch& patch : kNativeRpcPatches)
        std::memcpy(reinterpret_cast<void*>(moduleBase + patch.offset),
            patch.replacement, patch.size);
    FlushInstructionCache(GetCurrentProcess(), patchPage, kRpcFramePatchPageSize);

    bool verified = true;
    for (const NativeRpcPatch& patch : kNativeRpcPatches)
    {
        verified = verified && std::memcmp(
            reinterpret_cast<const void*>(moduleBase + patch.offset),
            patch.replacement, patch.size) == 0;
    }
    if (!verified)
    {
        for (const NativeRpcPatch& patch : kNativeRpcPatches)
            std::memcpy(reinterpret_cast<void*>(moduleBase + patch.offset),
                patch.expected, patch.size);
        FlushInstructionCache(GetCurrentProcess(), patchPage, kRpcFramePatchPageSize);
    }

    DWORD ignoredProtection = 0;
    const bool restored = VirtualProtect(patchPage, kRpcFramePatchPageSize,
        oldProtection, &ignoredProtection) != FALSE;
    if (!verified || !restored)
    {
        ClientLog("[NATIVE-RPC] Frame patch failed verification or page restoration.");
        return false;
    }

    ClientLog("[NATIVE-RPC] Raised the pinned client frame and output-buffer limit to 2097152 bytes.");
    return true;
}

bool LoadoutFeatureEnabled(const std::string& commandLine, const std::string& key)
{
    return CommandLinePolicy::FeatureEnabled(commandLine, key);
}

LoadoutBridgeOptions GetLoadoutBridgeOptions()
{
    const std::string commandLine = GetCommandLineA();
    if (CommandLinePolicy::HasExactSwitch(commandLine, "-NativeArchiveOnly"))
        return {false, false, false, false};

    return {
        LoadoutFeatureEnabled(commandLine, "-LoadoutBaselineBridge"),
        LoadoutFeatureEnabled(commandLine, "-LoadoutPreOrderIntercept"),
        LoadoutFeatureEnabled(commandLine, "-LoadoutConfirmDeferral"),
        LoadoutFeatureEnabled(commandLine, "-LoadoutSpawnBridge"),
    };
}
}

std::string ReadStrictAuthorityWorldInstanceId()
{
    return CurrentStrictAuthorityWorldInstanceId();
}

namespace
{
    nlohmann::json StrictAuthorityStartFailure(
        const char* code,
        const char* message)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", code},
            {"message", message}
        };
    }

    // Capture the UObject/NetDriver pointers only on the game-thread pump.
    // The resulting plan is later registered as a small, lock-only ownership
    // record before a timed-out pipe request is published to its caller.
    StrictAuthorityCleanupPlan CaptureStrictAuthorityCleanupPlan(
        const StrictAuthorityStartRequest& request,
        const std::string_view reason)
    {
        const std::string& worldInstanceId = request.streamingWorldInstanceId;
        UWorld* const currentWorld = UWorld::GetWorld();
        UWorld* const retiredWorld = StrictAuthorityLease::OwnedWorldMatches(
            currentWorld, CurrentStrictAuthorityWorldInstanceId(),
            request.streamingWorld, worldInstanceId) ? request.streamingWorld : nullptr;
        // Never bind cleanup to whichever world happens to be current after
        // an unexpected swap. Missing ownership retains quarantine and needs
        // the external supervisor's exact owned-process exit proof.
        UNetDriver* const retiredNetDriver = retiredWorld ? retiredWorld->NetDriver : nullptr;
        return StrictAuthorityCleanupPlan{
            StrictRosterCleanupState{
            true, false, false,
            request.allocationScope.attemptId,
            request.allocationScope.authoritySessionId,
            worldInstanceId,
            request.allocationScope.rosterRevision,
            request.allocationScope.routeGeneration,
            retiredWorld,
            retiredNetDriver, retiredWorld == nullptr},
            std::string(reason)};
    }

    // This is deliberately free of UWorld, UObject, Steam, policy reset, and
    // logging calls.  CommitCompleted may hold its short queue/request
    // critical section while this function records the cleanup lease.  The
    // game-thread side effects run only after terminal publication.
    bool RegisterStrictAuthorityCleanupOwner(
        const StrictAuthorityCleanupPlan& plan)
    {
        const StrictRosterCleanupState& cleanup = plan.state;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            if (gStrictRosterCleanup.pending)
            {
                return StrictRosterCleanupScopeEquals(
                    gStrictRosterCleanup,
                    cleanup.attemptId,
                    cleanup.authoritySessionId,
                    cleanup.worldInstanceId,
                    cleanup.rosterRevision,
                    cleanup.routeGeneration);
            }
            gStrictRosterCleanup = cleanup;
        }
        gStrictAuthorityAwaitingWorldTeardown.store(
            true, std::memory_order_release);
        // Do not take gStrictAuthorityStartMutex while CommitCompleted holds
        // the queue lock.  The game-thread post-publication phase clears the
        // ReadyLease under that mutex after the terminal result is visible.
        gStrictAuthorityServerStarted.store(false, std::memory_order_release);
        gStrictAuthorityWorldListening.store(false, std::memory_order_release);
        return true;
    }

    // Request the native cleanup only after the queue has published the
    // scoped terminal result.  This function is called from the game-thread
    // pump, never from CommitCompleted's queue critical section.
    void ExecuteStrictAuthorityCleanup(
        const StrictAuthorityCleanupPlan& plan)
    {
        if (!RegisterStrictAuthorityCleanupOwner(plan))
            return;

        gStrictRosterPolicy.Reset();
        StrictRosterSteamAuth::ClearAllExpectedProofs();

        const StrictRosterNativeTeardownRequestResult teardownResult =
            RequestScopedStrictRosterWorldTeardown(plan.state);
        const bool requested = teardownResult !=
            StrictRosterNativeTeardownRequestResult::NotRequested;
        const bool processExitRequested = teardownResult ==
            StrictRosterNativeTeardownRequestResult::DedicatedProcessExitRequested;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            if (gStrictRosterCleanup.pending &&
                StrictRosterCleanupScopeEquals(
                    gStrictRosterCleanup,
                    plan.state.attemptId,
                    plan.state.authoritySessionId,
                    plan.state.worldInstanceId,
                    plan.state.rosterRevision,
                    plan.state.routeGeneration))
            {
                gStrictRosterCleanup.teardownRequested = requested;
                gStrictRosterCleanup.processExitRequested = processExitRequested;
            }
        }
        {
            std::lock_guard<std::mutex> startLock(gStrictAuthorityStartMutex);
            gStrictAuthorityReadyLease = StrictAuthorityReadyLease{};
        }
        ClearStrictRosterLocalHostSeat();
        ClearStrictRosterControllerSeats();
        ClientLog(std::string("[STRICT-ROSTER] Authority start entered scoped cleanup: ") +
            plan.reason + (requested
                ? "; teardown requested, native_cleared remains false."
                : "; native teardown entry was unavailable, native_cleared remains false."));
    }

    // The pipe listener may only copy the world/driver ownership captured at
    // the successful game-thread start. Native teardown is deferred to a tick.
    bool QueueFailedHostStageCleanup(
        const StrictRoster::AllocationScope& scope,
        const std::string& worldInstanceId,
        const std::string& nativeConnectionNonce)
    {
        StrictAuthorityCleanupPlan plan{
            StrictRosterCleanupState{true, false, false, scope.attemptId,
                scope.authoritySessionId, worldInstanceId, scope.rosterRevision,
                scope.routeGeneration, nullptr, nullptr, true},
            "local HOST scope staging failed"};
        {
            std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
            const auto& lease = gStrictAuthorityReadyLease;
            if (lease.active && lease.allocationScope &&
                lease.worldInstanceId == worldInstanceId &&
                lease.nativeConnectionNonce == nativeConnectionNonce &&
                lease.allocationScope->attemptId == scope.attemptId &&
                lease.allocationScope->authoritySessionId == scope.authoritySessionId &&
                lease.allocationScope->rosterRevision == scope.rosterRevision &&
                lease.allocationScope->routeGeneration == scope.routeGeneration)
            {
                plan.state.retiredWorld = lease.world;
                plan.state.retiredNetDriver = lease.netDriver;
                plan.state.quarantined = lease.world == nullptr;
            }
        }
        if (!RegisterStrictAuthorityCleanupOwner(plan))
            return false;
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        gStrictAuthorityFailedStageCleanup = plan;
        return true;
    }

    // Compatibility wrapper for paths that already run on the game thread.
    // It keeps registration and native side effects in their correct phases.
    bool ArmStrictAuthorityStartCleanup(
        const StrictAuthorityStartRequest& request,
        const std::string_view reason,
        const bool requestTeardown = true)
    {
        const StrictAuthorityCleanupPlan plan =
            CaptureStrictAuthorityCleanupPlan(request, reason);
        if (!RegisterStrictAuthorityCleanupOwner(plan))
            return false;
        if (requestTeardown)
        {
            ExecuteStrictAuthorityCleanup(plan);
        }
        return true;
    }

    void RequestArmedStrictAuthorityCleanup(
        const StrictAuthorityStartRequest& request)
    {
        StrictRosterCleanupState cleanup;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            cleanup = gStrictRosterCleanup;
        }
        if (!StrictRosterCleanupScopeEquals(
                cleanup,
                request.allocationScope.attemptId,
                request.allocationScope.authoritySessionId,
                cleanup.worldInstanceId,
                request.allocationScope.rosterRevision,
                request.allocationScope.routeGeneration) ||
            cleanup.teardownRequested)
        {
            return;
        }
        const StrictRosterNativeTeardownRequestResult teardownResult =
            RequestScopedStrictRosterWorldTeardown(cleanup);
        const bool requested = teardownResult !=
            StrictRosterNativeTeardownRequestResult::NotRequested;
        const bool processExitRequested = teardownResult ==
            StrictRosterNativeTeardownRequestResult::DedicatedProcessExitRequested;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            if (gStrictRosterCleanup.pending &&
                gStrictRosterCleanup.attemptId == request.allocationScope.attemptId &&
                gStrictRosterCleanup.authoritySessionId == request.allocationScope.authoritySessionId &&
                gStrictRosterCleanup.rosterRevision == request.allocationScope.rosterRevision &&
                gStrictRosterCleanup.routeGeneration == request.allocationScope.routeGeneration)
            {
                gStrictRosterCleanup.teardownRequested = requested;
                gStrictRosterCleanup.processExitRequested = processExitRequested;
            }
        }
        ClearStrictRosterLocalHostSeat();
        ClearStrictRosterControllerSeats();
    }

    nlohmann::json QueueStrictAuthorityWorldStart(
        const std::string_view hostingKind,
        const std::string_view nativeConnectionNonce,
        const std::string_view worldInstanceId,
        const StrictRoster::AllocationScope& allocationScope)
    {
        auto request = std::make_shared<StrictAuthorityStartRequest>();
        request->hostingKind = std::string(hostingKind);
        request->nativeConnectionNonce = std::string(nativeConnectionNonce);
        request->worldInstanceId = std::string(worldInstanceId);
        request->allocationScope = allocationScope;
        if (!gStrictAuthorityStartQueue.Enqueue(request))
            return StrictAuthorityStartFailure(
                "authority_start_in_progress",
                "another native authority start is already pending");

        std::unique_lock<std::mutex> requestLock(request->mutex);
        const bool completed = request->completed.wait_for(
            requestLock,
            std::chrono::seconds(30),
            [&request]() {
                return request->state == StrictAuthorityStartDispatch::State::Completed ||
                    request->state == StrictAuthorityStartDispatch::State::Cancelled;
            });
        if (!completed)
        {
            requestLock.unlock();
            const auto cancelResult = gStrictAuthorityStartQueue.RequestCancel(request);
            if (cancelResult == StrictAuthorityStartDispatch::CancelResult::AlreadyFinalizing)
            {
                // Finalizing is the linearization point: cancellation is
                // frozen, so the listener must wait for the producer's
                // terminal publication for a short bounded handoff window.
                // If the game thread is stalled, return a scoped pending
                // result; the request remains owned and its cancellation
                // path will quarantine any late native side effect.
                requestLock.lock();
                const bool published = request->completed.wait_for(
                    requestLock,
                    std::chrono::seconds(2),
                    [&request]() {
                    return request->state == StrictAuthorityStartDispatch::State::Completed ||
                        request->state == StrictAuthorityStartDispatch::State::Cancelled;
                    });
                if (published &&
                    request->state == StrictAuthorityStartDispatch::State::Completed)
                {
                    const nlohmann::json result = request->result;
                    requestLock.unlock();
                    return result;
                }
                requestLock.unlock();
                if (!published)
                {
                    return nlohmann::json{
                        {"accepted", false},
                        {"code", "authority_start_pending"},
                        {"message", "native authority start remains owned by the game-thread cleanup path"},
                        {"status", "cleanup_pending"},
                        {"native_cleared", false},
                        {"world_teardown_required", true},
                        {"attempt_id", request->allocationScope.attemptId},
                        {"authority_session_id", request->allocationScope.authoritySessionId},
                        {"world_instance_id", request->worldInstanceId},
                        {"roster_revision", request->allocationScope.rosterRevision},
                        {"route_generation", request->allocationScope.routeGeneration}
                    };
                }
            }
            else if (cancelResult ==
                    StrictAuthorityStartDispatch::CancelResult::AlreadyCompleted ||
                cancelResult == StrictAuthorityStartDispatch::CancelResult::NotOwned)
            {
                // PublishCompleted clears the queue's active pointer before
                // this listener reacquires it. Read the request terminal state
                // before reporting a timeout; otherwise an accepted world can
                // become an unowned success solely due to this race.
                requestLock.lock();
                if (request->state == StrictAuthorityStartDispatch::State::Completed)
                {
                    const nlohmann::json result = request->result;
                    requestLock.unlock();
                    return result;
                }
                requestLock.unlock();
            }
            else if (cancelResult ==
                StrictAuthorityStartDispatch::CancelResult::CancelledProcessing)
            {
                // The game-thread consumer still owns the request. Return a
                // scoped pending response; PumpStrictAuthorityStart will
                // register quarantine before publishing its terminal result.
                return nlohmann::json{
                    {"accepted", false},
                    {"code", "authority_start_pending"},
                    {"status", "cleanup_pending"},
                    {"message", "native authority start remains owned by the game-thread cleanup path"},
                    {"native_cleared", false},
                    {"world_teardown_required", true},
                    {"attempt_id", request->allocationScope.attemptId},
                    {"authority_session_id", request->allocationScope.authoritySessionId},
                    {"world_instance_id", request->worldInstanceId},
                    {"roster_revision", request->allocationScope.rosterRevision},
                    {"route_generation", request->allocationScope.routeGeneration}
                };
            }
            return StrictAuthorityStartFailure(
                "authority_start_timeout",
                "the native game-thread authority start did not complete in time");
        }

        const nlohmann::json result = request->result;
        return result;
    }
}

void UpdateStrictAuthorityWorldObservationOnGameThread()
{
    UWorld* const world = UWorld::GetWorld();
    gNativeNetModeSnapshot.store(GetNativeNetModeInternal(world), std::memory_order_release);
    std::string detail;
    const bool listening = world && IsAuthoritativeListeningWorld(world, detail);
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
        gObservedAuthorityWorld = listening ? world : nullptr;
        gObservedAuthorityNetDriver = listening ? world->NetDriver : nullptr;
        gAuthorityWorldObservedAt = std::chrono::steady_clock::now();
    }
    if (listening)
        (void)ObserveStrictAuthorityWorld(world);
    gStrictAuthorityWorldListening.store(listening, std::memory_order_release);
    if (listening)
        ApplyStrictRosterLocalHostSeatOnGameThread();
}

bool IsStrictAuthorityWorldListeningSnapshot()
{
    return gStrictAuthorityWorldListening.load(std::memory_order_acquire);
}

void PumpStrictAuthorityStartOnGameThread()
{
    std::optional<StrictAuthorityCleanupPlan> failedStageCleanup;
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        failedStageCleanup.swap(gStrictAuthorityFailedStageCleanup);
    }
    if (failedStageCleanup)
    {
        ExecuteStrictAuthorityCleanup(*failedStageCleanup);
        return;
    }
    std::shared_ptr<StrictAuthorityStartDispatch::Request> baseRequest =
        gStrictAuthorityStartQueue.Claim();
    if (!baseRequest)
        baseRequest = gStrictAuthorityStartQueue.Processing();
    if (!baseRequest)
        return;
    const std::shared_ptr<StrictAuthorityStartRequest> request =
        std::static_pointer_cast<StrictAuthorityStartRequest>(baseRequest);

    nlohmann::json result;
    bool authoritativeListeningWorld = false;
    std::string authorityDetail;
    bool scopeStillCurrent = false;
    bool startAttempted = request->nativeTravelRequested;

    const auto scopeMatchesRequest = [&request](
        const std::optional<StrictRoster::AllocationScope>& scope,
        const std::optional<std::string>& hostingKind) {
        return scope && hostingKind &&
            scope->attemptId == request->allocationScope.attemptId &&
            scope->authoritySessionId == request->allocationScope.authoritySessionId &&
            scope->rosterRevision == request->allocationScope.rosterRevision &&
            scope->routeGeneration == request->allocationScope.routeGeneration &&
            *hostingKind == request->hostingKind;
    };

    if (!gStrictAuthorityRuntimeReady.load(std::memory_order_acquire))
    {
        result = StrictAuthorityStartFailure(
            "authority_runtime_not_ready",
            "the listen authority runtime is not initialized");
    }
    else
    {
        const auto scope = gStrictRosterPolicy.CurrentAllocationScope();
        const auto hostingKind = gStrictRosterPolicy.CurrentHostingKind();
        const std::string currentWorldInstanceId =
            CurrentStrictAuthorityWorldInstanceId();
        const bool worldScopeCurrent = request->streamingWorld
            ? StrictAuthorityLease::OwnedWorldMatches(
                UWorld::GetWorld(), currentWorldInstanceId,
                request->streamingWorld, request->streamingWorldInstanceId)
            : (request->nativeTravelRequested || request->worldInstanceId.empty() ||
                request->worldInstanceId == currentWorldInstanceId);
        scopeStillCurrent = scopeMatchesRequest(scope, hostingKind) && worldScopeCurrent;
        if (!scopeStillCurrent)
        {
            result = StrictAuthorityStartFailure(
                "authority_start_scope_stale",
                "the allocation scope changed before native game-thread dispatch");
        }
        else
        {
            authoritativeListeningWorld =
                gStrictAuthorityServerStarted.load(std::memory_order_acquire);
            if (!authoritativeListeningWorld &&
                !gStrictAuthorityStartQueue.IsCancellationRequested(baseRequest))
            {
                // Map travel needs subsequent engine ticks. Issue it once,
                // then return to the engine until the new map and streaming
                // levels are observed. Never sleep on this game thread.
                bool listenCompleted = false;
                try
                {
                    UWorld* const world = UWorld::GetWorld();
                    if (!request->nativeTravelRequested)
                    {
                        request->nativeTravelRequested = true;
                        request->previousWorld = world;
                        request->worldDeadline = std::chrono::steady_clock::now() +
                            std::chrono::seconds(25);
                        startAttempted = true;
                        if (BeginServerMapTravel())
                            return;
                        authorityDetail = "native server map travel could not be issued";
                    }
                    else if (std::chrono::steady_clock::now() >= request->worldDeadline)
                    {
                        authorityDetail = "native server map did not become ready within 25 seconds";
                    }
                    else if (request->streamingWorld && world != request->streamingWorld)
                    {
                        authorityDetail = "native world changed again during streaming readiness";
                    }
                    else if (!IsServerMapReady(world, request->previousWorld))
                    {
                        return;
                    }
                    else if (!request->streamingWorld)
                    {
                        request->streamingWorld = world;
                        request->streamingWorldInstanceId = ObserveStrictAuthorityWorld(world);
                        RequestServerStreamingLevels(world);
                        return;
                    }
                    else if (!AreServerStreamingLevelsReady(world))
                    {
                        return;
                    }
                    else
                    {
                        listenCompleted = CompleteServerListen(world);
                        if (!listenCompleted)
                            authorityDetail = "native NetDriver could not be created for the new world";
                    }
                }
                catch (...)
                {
                    authorityDetail = "native server bootstrap threw; cleanup is required";
                    ClientLog("[STRICT-ROSTER] Native server bootstrap threw; "
                              "the scoped cleanup owner remains active.");
                }
                UWorld* authoritativeWorld = UWorld::GetWorld();
                const int postTravelNetMode =
                    GetNativeNetModeInternal(authoritativeWorld);
                authoritativeListeningWorld = listenCompleted &&
                    StrictAuthorityLease::OwnedWorldMatches(
                        authoritativeWorld, CurrentStrictAuthorityWorldInstanceId(),
                        request->streamingWorld, request->streamingWorldInstanceId) &&
                    IsAuthoritativeListeningWorld(authoritativeWorld, authorityDetail);
                ClientLog(std::string("[STRICT-ROSTER] Post-travel authority: net_mode=") +
                    NetModeName(postTravelNetMode) + " " + authorityDetail +
                    " result=" + (authoritativeListeningWorld ? "ready" : "invalid"));
                const auto postStartScope = gStrictRosterPolicy.CurrentAllocationScope();
                const auto postStartHostingKind = gStrictRosterPolicy.CurrentHostingKind();
                scopeStillCurrent = scopeMatchesRequest(postStartScope, postStartHostingKind);
                if (authoritativeListeningWorld && scopeStillCurrent)
                {
                    std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
                    const std::string worldInstanceId =
                        ObserveStrictAuthorityWorld(authoritativeWorld);
                    gStrictAuthorityServerStarted.store(true, std::memory_order_release);
                    gStrictAuthorityAwaitingWorldTeardown.store(
                        false, std::memory_order_release);
                    gStrictAuthorityReadyLease.active = true;
                    gStrictAuthorityReadyLease.hostingKind = request->hostingKind;
                    gStrictAuthorityReadyLease.nativeConnectionNonce =
                        request->nativeConnectionNonce;
                    gStrictAuthorityReadyLease.worldInstanceId = worldInstanceId;
                    gStrictAuthorityReadyLease.clientOperationSequence = 0;
                    gStrictAuthorityReadyLease.allocationScope = request->allocationScope;
                    gStrictAuthorityReadyLease.world = authoritativeWorld;
                    gStrictAuthorityReadyLease.netDriver = authoritativeWorld->NetDriver;
                }
            }

            if (gStrictAuthorityStartQueue.IsCancellationRequested(baseRequest))
            {
                result = StrictAuthorityStartFailure(
                    "authority_start_cancelled",
                    startAttempted
                        ? "native authority start was cancelled after dispatch"
                        : "native authority start was cancelled before dispatch");
            }
            else if (authoritativeListeningWorld && scopeStillCurrent)
            {
                result = nlohmann::json{
                    {"accepted", true},
                    {"code", "accepted"},
                    {"native_connection_nonce", request->nativeConnectionNonce},
                    {"hosting_kind", request->hostingKind},
                    {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()}
                };
            }
            else
            {
                result = StrictAuthorityStartFailure(
                    scopeStillCurrent
                        ? "listen_authority_unavailable"
                        : "authority_start_scope_stale",
                    scopeStillCurrent
                        ? "the pinned native world did not become authoritative"
                        : "the allocation scope changed during native authority start");
            }
        }
    }

    std::optional<StrictAuthorityCleanupPlan> cleanupPlan;
    if (startAttempted || authoritativeListeningWorld)
    {
        cleanupPlan = CaptureStrictAuthorityCleanupPlan(
            *request, "native authority start requires scoped cleanup");
    }
    const auto addCleanupFields = [&request, &cleanupPlan](nlohmann::json& target) {
        target["status"] = "cleanup_pending";
        target["native_cleared"] = false;
        target["world_teardown_required"] = true;
        target["attempt_id"] = request->allocationScope.attemptId;
        target["authority_session_id"] = request->allocationScope.authoritySessionId;
        target["world_instance_id"] = cleanupPlan
            ? cleanupPlan->state.worldInstanceId
            : CurrentStrictAuthorityWorldInstanceId();
        target["roster_revision"] = request->allocationScope.rosterRevision;
        target["route_generation"] = request->allocationScope.routeGeneration;
    };

    // Any native travel attempt that did not produce the exact requested
    // authoritative world is a side-effect failure. Register the cleanup
    // owner before terminal publication; the actual UObject teardown request
    // is issued after the queue slot is committed on this game-thread tick.
    const bool cleanupRequired = startAttempted &&
        (!authoritativeListeningWorld || !scopeStillCurrent);
    if (cleanupRequired)
    {
        addCleanupFields(result);
    }

    request->successResult = std::move(result);
    request->cancellationResult = StrictAuthorityStartFailure(
        "authority_start_cancelled",
        (startAttempted || authoritativeListeningWorld)
            ? "the native authority completed after its pipe request timed out; teardown is pending"
            : "the native authority start was cancelled before native dispatch");
    if (startAttempted || authoritativeListeningWorld)
        addCleanupFields(request->cancellationResult);
    {
        // Fail-closed is the preloaded result. CommitCompleted swaps in the
        // success result only after it has atomically observed no cancellation.
        std::lock_guard<std::mutex> lock(request->mutex);
        request->result = request->cancellationResult;
    }

    bool cancellationRequested = false;
    bool cleanupOwnerRegistered = false;
    const bool committed = gStrictAuthorityStartQueue.CommitCompleted(
        baseRequest,
        cancellationRequested,
        [request, &cleanupPlan, &cleanupOwnerRegistered,
            cleanupRequired, startAttempted, authoritativeListeningWorld](
            const bool cancelled) {
            const bool needsCleanup = cleanupRequired ||
                (cancelled && (startAttempted || authoritativeListeningWorld));
            if (needsCleanup && cleanupPlan)
            {
                // Only the scoped owner registration and JSON selection are
                // allowed in this critical section.  UWorld/NetDriver,
                // policy, Steam, teardown, and logging happen below after
                // terminal publication on the same game-thread pump.
                cleanupOwnerRegistered = RegisterStrictAuthorityCleanupOwner(
                    *cleanupPlan);
            }
            if (!cancelled)
            {
                request->result.swap(request->successResult);
            }
            // A cancelled result was prepared before taking the queue locks.
            // Keep that result without allocating JSON or reading world state.
        });
    if (!committed)
        return;
    if (cleanupOwnerRegistered && cleanupPlan)
    {
        ExecuteStrictAuthorityCleanup(*cleanupPlan);
    }
    else if (cleanupRequired || cancellationRequested)
    {
        // A request cancelled before native dispatch has no world to tear
        // down.  Keep this fallback fail-closed for a pre-existing owner.
        RequestArmedStrictAuthorityCleanup(*request);
    }
}

namespace
{
    nlohmann::json QueueStrictRosterClearOnGameThread(
        const nlohmann::json& arguments)
    {
        StrictAuthorityMutationLease clearMutation(gStrictAuthorityMutationInFlight);
        if (!clearMutation || gStrictAuthorityStartQueue.HasActive())
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_transaction_in_progress"},
                {"message", "allocation clear is serialized with native authority start/install"},
                {"native_cleared", false},
                {"world_teardown_required", true}
            };
        }
        auto request = std::make_shared<StrictRosterClearRequest>();
        request->arguments = arguments;
        if (!gStrictRosterClearQueue.Enqueue(request))
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "clear_in_progress"},
                {"message", "another scoped native clear is already pending"},
                {"native_cleared", false},
                {"world_teardown_required", true}
            };
        }

        std::unique_lock<std::mutex> requestLock(request->mutex);
        const bool completed = request->completed.wait_for(
            requestLock,
            std::chrono::seconds(30),
            [&request]() {
                return request->state == StrictAuthorityStartDispatch::State::Completed ||
                    request->state == StrictAuthorityStartDispatch::State::Cancelled;
            });
        if (completed)
            return request->result;

        requestLock.unlock();
        const auto cancelResult = gStrictRosterClearQueue.RequestCancel(request);
        if (cancelResult == StrictAuthorityStartDispatch::CancelResult::AlreadyFinalizing)
        {
            // Clear owns teardown once processing starts. Give the game
            // thread a bounded handoff window; after that, return scoped
            // pending while the queue keeps the cleanup owner.
            requestLock.lock();
            const bool published = request->completed.wait_for(
                requestLock,
                std::chrono::seconds(2),
                [&request]() {
                return request->state == StrictAuthorityStartDispatch::State::Completed ||
                    request->state == StrictAuthorityStartDispatch::State::Cancelled;
                });
            if (published)
            {
                const nlohmann::json result = request->result;
                requestLock.unlock();
                return result;
            }
            requestLock.unlock();
            return nlohmann::json{
                {"accepted", false},
                {"code", "clear_pending"},
                {"status", "cleanup_pending"},
                {"message", "native clear remains owned by the game-thread observer"},
                {"native_cleared", false},
                {"world_teardown_required", true}
            };
        }
        if (cancelResult ==
            StrictAuthorityStartDispatch::CancelResult::CancelledProcessing)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "clear_pending"},
                {"status", "cleanup_pending"},
                {"message", "native clear remains owned by the game-thread observer"},
                {"native_cleared", false},
                {"world_teardown_required", true},
                {"attempt_id", request->arguments.value("attempt_id", "")},
                {"authority_session_id", request->arguments.value("authority_session_id", "")},
                {"world_instance_id", request->arguments.value("world_instance_id", "")},
                {"roster_revision", request->arguments.value("roster_revision", 0)},
                {"route_generation", request->arguments.value("route_generation", 0)}
            };
        }
        if (cancelResult == StrictAuthorityStartDispatch::CancelResult::AlreadyCompleted ||
            cancelResult == StrictAuthorityStartDispatch::CancelResult::NotOwned)
        {
            requestLock.lock();
            if (request->state == StrictAuthorityStartDispatch::State::Completed)
            {
                const nlohmann::json result = request->result;
                requestLock.unlock();
                return result;
            }
            requestLock.unlock();
        }
        return nlohmann::json{
            {"accepted", false},
            {"code", "clear_timeout"},
            {"message", "native clear was not completed within the bounded wait"},
            {"native_cleared", false},
            {"world_teardown_required", true}
        };
    }
}

void PumpStrictRosterClearOnGameThread()
{
    const std::shared_ptr<StrictAuthorityStartDispatch::Request> baseRequest =
        gStrictRosterClearQueue.Claim();
    if (!baseRequest)
        return;
    const std::shared_ptr<StrictRosterClearRequest> request =
        std::static_pointer_cast<StrictRosterClearRequest>(baseRequest);

    nlohmann::json result;
    // Start and clear share the same game-thread boundary. If a start request
    // has not yet been dispatched, leave the allocation untouched and let the
    // caller retry after this explicit pending response.
    if (gStrictAuthorityStartQueue.HasActive())
    {
        result = nlohmann::json{
            {"accepted", false},
            {"code", "clear_pending"},
            {"message", "native authority start is still being dispatched"},
            {"native_cleared", false},
            {"world_teardown_required", true}
        };
    }
    else
    {
        result = ProcessStrictRosterClearMatchAllocationResult(request->arguments);
    }

    bool cancellationRequested = false;
    if (!gStrictRosterClearQueue.BeginFinalize(baseRequest, cancellationRequested))
        return;
    // Once a clear reaches game-thread processing, its native teardown and
    // observer result remain authoritative even if the waiting pipe deadline
    // has elapsed. Do not roll back a cleanup side effect here.
    {
        std::lock_guard<std::mutex> lock(request->mutex);
        request->result = std::move(result);
    }
    (void)cancellationRequested;
    (void)gStrictRosterClearQueue.PublishCompleted(baseRequest);
}

CommandFramework::JoinResult OnJoinFromPipe(
    const std::string& ip,
    const std::string& token,
    const nlohmann::json& expectedScope)
{
    ClientLog("[PIPE] Join request received for target " + ip + ".");
    if (token.empty())
    {
        return CommandFramework::JoinResult{
            false,
            "native_admission_required",
            "online join requires a signed short-lived grant"};
    }
    const AuthorizedJoinResult result =
        QueueConnectToMatchAuthorizedDetailed(ip, token, expectedScope);
    return CommandFramework::JoinResult{
        result.accepted, result.code, result.message, result.operationSequence};
}

nlohmann::json OnInstallMatchAllocation(const nlohmann::json& arguments)
{
    StrictAuthorityMutationLease installMutation(gStrictAuthorityMutationInFlight);
    if (!installMutation)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_start_in_progress"},
            {"message", "allocation installation is serialized with native authority start"},
            {"native_cleared", false},
            {"world_teardown_required", true}
        };
    }
    // A timed-out worker still owns its queue until cleanup registration and
    // terminal publication finish. Check that ownership before cleanup, and
    // never hold the ready-lease mutex while acquiring a queue mutex.
    if (gStrictAuthorityStartQueue.HasActive() || gStrictRosterClearQueue.HasActive())
        return StrictAuthorityStartFailure("authority_active", "a native authority transaction is still active");
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        if (gStrictRosterCleanup.pending)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "cleanup_pending"},
                {"message", "the previous native world has not been cleared"},
                {"native_cleared", false},
                {"world_instance_id", gStrictRosterCleanup.worldInstanceId},
                {"world_teardown_required", true}
            };
        }
    }
    if (gVerifiedExecutableHash != kSupportedExecutableSha256)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "game_binary_unverified"},
            {"message", "the locked game binary has not been verified"}
        };
    }
    const StrictRoster::Decision decision = gStrictRosterPolicy.InstallAllocation(
        arguments.value("allocation", ""),
        arguments.value("admission_key_id", ""),
        arguments.value("admission_public_key_base64", ""),
        EpochSecondsNow());
    if (!decision.accepted)
    {
        ClientLog("[STRICT-ROSTER] Allocation rejected: " + decision.code + ".");
        return PolicyResult(decision);
    }
    ClientLog("[STRICT-ROSTER] Signed allocation installed; native admission remains gated.");
    return nlohmann::json{
        {"accepted", true},
        {"code", "accepted"},
        {"payload_version", kStrictRosterPayloadVersion},
        {"game_binary_sha256", gVerifiedExecutableHash}
    };
}

nlohmann::json OnInstallMatchJoinGrant(const nlohmann::json& arguments)
{
    StrictAuthorityMutationLease grantMutation(gStrictAuthorityMutationInFlight);
    if (!grantMutation || gStrictAuthorityStartQueue.HasActive() ||
        gStrictRosterClearQueue.HasActive())
    {
        return StrictAuthorityStartFailure(
            "authority_start_in_progress", "another native authority transaction is still in progress");
    }
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        if (gStrictRosterCleanup.pending)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "cleanup_pending"},
                {"message", "the previous native world has not been cleared"},
                {"native_cleared", false},
                {"world_instance_id", gStrictRosterCleanup.worldInstanceId},
                {"world_teardown_required", true}
            };
        }
    }
    const StrictRoster::Decision decision = gStrictRosterPolicy.StageJoinGrant(
        arguments.value("join_grant", ""), EpochSecondsNow());
    if (!decision.accepted)
    {
        ClientLog("[STRICT-ROSTER] Authority rejected a staged join grant: " +
            decision.code + ".");
        return PolicyResult(decision);
    }
    ClientLog("[STRICT-ROSTER] Authority staged one identity-bound join grant.");
    return nlohmann::json{{"accepted", true}, {"code", "accepted"}};
}

nlohmann::json OnStartMatchAuthority(const nlohmann::json& arguments)
{
    StrictAuthorityMutationLease startMutation(gStrictAuthorityMutationInFlight);
    if (!startMutation || gStrictAuthorityStartQueue.HasActive() ||
        gStrictRosterClearQueue.HasActive())
    {
        return StrictAuthorityStartFailure(
            "authority_start_in_progress", "another native authority transaction is still in progress");
    }
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        if (gStrictRosterCleanup.pending)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "cleanup_pending"},
                {"message", "the previous native world has not been cleared"},
                {"world_instance_id", gStrictRosterCleanup.worldInstanceId},
                {"world_teardown_required", true}
            };
        }
    }
    if (!amServer)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_mode_unavailable"},
            {"message", "only a native server or listen authority may start a match authority"}
        };
    }
    std::string endpointHost;
    int endpointPort = 0;
    if (!ParseAuthorityTarget(
        arguments.value("transport_target", ""), endpointHost, endpointPort))
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_authority_endpoint"},
            {"message", "the authority transport endpoint is invalid"}
        };
    }
    const int configuredPort = Config.ExternalPort != 0
        ? static_cast<int>(Config.ExternalPort)
        : static_cast<int>(Config.Port);
    if (configuredPort < 1 || endpointPort != configuredPort)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_port_mismatch"},
            {"message", "transport target must use the configured game listen port"}
        };
    }
    // Validate the exact wire target before mutating the policy or queueing
    // native travel.  A formatting failure after StartServer would leave a
    // live world with no accepted authority response and would require an
    // asynchronous teardown path merely to recover from malformed input.
    if (CommandProtocol::FormatMatchTarget(
            endpointHost, static_cast<std::uint16_t>(endpointPort)).empty())
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_authority_endpoint"},
            {"message", "authority endpoint cannot be represented safely"}
        };
    }
    if (amListenServer && !IsClientLoginReadyForTravel())
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "client_login_pending"},
            {"message", "complete the local platform login before starting listen authority"}
        };
    }
    const auto hostingKind = gStrictRosterPolicy.CurrentHostingKind();
    if (!hostingKind)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "allocation_unavailable"},
            {"message", "install the signed match allocation before starting authority"}
        };
    }
    const auto allocationScope = gStrictRosterPolicy.CurrentAllocationScope();
    if (!allocationScope)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "allocation_unavailable"},
            {"message", "the signed match allocation scope is unavailable"}
        };
    }

    // A retried start/ACK for the same live authority is an idempotent
    // observation. Reuse the published nonce/world only when every scoped
    // allocation field and the current native world match; never manufacture
    // a second nonce for an already-listening world.
    std::optional<StrictAuthorityReadyLease> readyLease;
    bool authorityServerStarted = false;
    bool sameWorldRouteRecovery = false;
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        authorityServerStarted = gStrictAuthorityServerStarted.load(
            std::memory_order_acquire);
        if (authorityServerStarted && gStrictAuthorityReadyLease.active &&
            gStrictAuthorityReadyLease.allocationScope &&
            gStrictAuthorityReadyLease.hostingKind == *hostingKind &&
            gStrictAuthorityReadyLease.allocationScope->attemptId ==
                allocationScope->attemptId &&
            gStrictAuthorityReadyLease.allocationScope->authoritySessionId ==
                allocationScope->authoritySessionId &&
            gStrictAuthorityReadyLease.allocationScope->rosterRevision ==
                allocationScope->rosterRevision)
        {
            readyLease = gStrictAuthorityReadyLease;
        }
    }
    if (authorityServerStarted)
    {
        if (!readyLease)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_state_unavailable"},
                {"message", "the live native authority has no matching scoped lease"},
                {"world_teardown_required", true}
            };
        }
        // UWorld/NetDriver are observed by UpdateStrictAuthorityWorldObservation
        // on the engine tick. The pipe callback only consumes that snapshot.
        const bool worldStillListening =
            gStrictAuthorityWorldListening.load(std::memory_order_acquire) &&
            CurrentStrictAuthorityWorldMatches(*readyLease);
        const StrictAuthorityLease::Decision leaseDecision =
            StrictAuthorityLease::Classify(
                readyLease->active,
                readyLease->hostingKind,
                *readyLease->allocationScope,
                *hostingKind,
                *allocationScope,
                gStrictAuthorityWorldListening.load(std::memory_order_acquire),
                worldStillListening);
        if (const auto requestedHost = arguments.find("preserve_host_connection");
            requestedHost != arguments.end())
        {
            if (*hostingKind != "P2P" ||
                (leaseDecision != StrictAuthorityLease::Decision::SameRouteReplay &&
                 leaseDecision != StrictAuthorityLease::Decision::P2PRouteRecovery))
                return StrictAuthorityStartFailure("host_preservation_scope_mismatch",
                    "HOST preservation requires this live P2P world and a contiguous authority route");
            const auto preserved = StrictAuthorityLease::PreservedHost(
                *requestedHost, GetClientMatchStatus(), *allocationScope,
                readyLease->worldInstanceId, readyLease->allocationScope->routeGeneration,
                readyLease->nativeConnectionNonce, readyLease->clientOperationSequence);
            if (!preserved)
                return StrictAuthorityStartFailure("host_preservation_proof_unavailable",
                    "the exact HOST connection has no fresh confirmed native Playable observation");
            std::string localPlatformId;
            if (!TryGetStrictRosterLocalPlatformId(localPlatformId))
                return StrictAuthorityStartFailure("host_identity_unavailable",
                    "the authenticated local Steam user is unavailable");
            const auto liveHost = gStrictRosterPolicy.ValidatePreservedHost(
                preserved->scope.playerId, localPlatformId, preserved->scope.worldInstanceId,
                preserved->scope.routeGeneration, preserved->scope.connectionGeneration,
                preserved->nativeConnectionNonce, EpochSecondsNow());
            if (!liveHost.accepted)
                return PolicyResult(liveHost);
            {
                std::scoped_lock ownerLock(gStrictRosterCleanupMutex, gStrictAuthorityStartMutex);
                if (gStrictRosterCleanup.pending ||
                    gStrictAuthorityAwaitingWorldTeardown.load(std::memory_order_acquire) ||
                    !gStrictAuthorityServerStarted.load(std::memory_order_acquire) ||
                    !gStrictAuthorityWorldListening.load(std::memory_order_acquire) ||
                    !gStrictAuthorityReadyLease.active || !gStrictAuthorityReadyLease.allocationScope ||
                    !CurrentStrictAuthorityWorldMatches(*readyLease) ||
                    gStrictAuthorityReadyLease.worldInstanceId != readyLease->worldInstanceId ||
                    gStrictAuthorityReadyLease.nativeConnectionNonce != preserved->nativeConnectionNonce ||
                    gStrictAuthorityReadyLease.clientOperationSequence != preserved->operationSequence ||
                    !StrictAuthorityLease::SameIdentity(*gStrictAuthorityReadyLease.allocationScope,
                        *readyLease->allocationScope) ||
                    gStrictAuthorityReadyLease.allocationScope->routeGeneration !=
                        readyLease->allocationScope->routeGeneration)
                    return StrictAuthorityStartFailure("host_preservation_owner_changed",
                        "the native authority entered cleanup or changed ownership during route refresh");
                // Advance the authority's admission route only. The client
                // operation, live seat, native nonce and Playable scope remain
                // the original connection; no synthetic disconnect/rejoin.
                gStrictAuthorityReadyLease.allocationScope = *allocationScope;
            }
            return nlohmann::json{
                {"accepted", true}, {"code", "accepted"},
                {"endpoint_host", endpointHost}, {"endpoint_port", endpointPort},
                {"world_instance_id", readyLease->worldInstanceId},
                {"native_connection_nonce", preserved->nativeConnectionNonce},
                {"operation_sequence", preserved->operationSequence},
                {"preserved_host_connection", preserved->ToJson()},
                {"idempotent", leaseDecision == StrictAuthorityLease::Decision::SameRouteReplay}};
        }
        if (leaseDecision == StrictAuthorityLease::Decision::SameRouteReplay)
        {
            if (*hostingKind == "P2P" && readyLease->clientOperationSequence == 0)
            {
                return nlohmann::json{
                    {"accepted", false}, {"code", "native_host_scope_unavailable"},
                    {"message", "the live authority has no staged local HOST operation"},
                    {"world_teardown_required", true}};
            }
            return nlohmann::json{
                {"accepted", true},
                {"code", "accepted"},
                {"endpoint_host", endpointHost},
                {"endpoint_port", endpointPort},
                {"world_instance_id", readyLease->worldInstanceId},
                {"native_connection_nonce", readyLease->nativeConnectionNonce},
                {"operation_sequence", readyLease->clientOperationSequence},
                {"idempotent", true}
            };
        }
        if (leaseDecision == StrictAuthorityLease::Decision::WorldUnavailable)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_world_not_cleared"},
                {"message", "the recorded native authority world is no longer current"},
                {"world_instance_id", readyLease->worldInstanceId},
                {"world_teardown_required", true}
            };
        }
        sameWorldRouteRecovery =
            leaseDecision == StrictAuthorityLease::Decision::P2PRouteRecovery;
        if (!sameWorldRouteRecovery)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", leaseDecision == StrictAuthorityLease::Decision::Unavailable
                    ? "authority_state_unavailable"
                    : "authority_route_generation_conflict"},
                {"message", "the live native authority cannot adopt this allocation route"},
                {"world_teardown_required", true}
            };
        }
        // Continue through the policy's fresh HOST reservation below. It will
        // reject a still-connected old generation and issue a new nonce only
        // after the old HOST has emitted DISCONNECTED.
    }
    else if (arguments.contains("preserve_host_connection"))
    {
        return StrictAuthorityStartFailure("host_preservation_owner_unavailable",
            "a missing native authority cannot preserve an old HOST connection");
    }
    StrictRoster::SeatDecision hostDecision;
    if (*hostingKind == "P2P")
    {
        // The host binding must come from the authenticated native platform
        // identity frozen by the signed allocation. Do not infer it from the
        // endpoint, a display name, or an IPC argument.
        std::string localPlatformId;
        if (!TryGetStrictRosterLocalPlatformId(localPlatformId))
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "host_identity_unavailable"},
                {"message", "the native local host platform identity is unavailable"}
            };
        }
        const auto nativeConnectionNonce =
            GenerateStrictRosterNativeConnectionNonce();
        if (!nativeConnectionNonce)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "native_connection_nonce_unavailable"},
                {"message", "the native host handshake nonce could not be generated"}
            };
        }
        hostDecision = gStrictRosterPolicy.StartAuthorityForAllocatedHost(
            localPlatformId, EpochSecondsNow(), *nativeConnectionNonce);
    }
    else
    {
        const auto nativeConnectionNonce =
            GenerateStrictRosterNativeConnectionNonce();
        if (!nativeConnectionNonce)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "native_connection_nonce_unavailable"},
                {"message", "the native Dedicated authority nonce could not be generated"}
            };
        }
        const StrictRoster::Decision authorityDecision =
            gStrictRosterPolicy.StartAuthority("", EpochSecondsNow());
        if (!authorityDecision.accepted)
        {
            ClientLog("[STRICT-ROSTER] Authority start rejected: " +
                authorityDecision.code + ".");
            return PolicyResult(authorityDecision);
        }
        hostDecision.accepted = true;
        hostDecision.code = "accepted";
        hostDecision.nativeConnectionNonce = *nativeConnectionNonce;
    }
    if (!hostDecision.accepted)
    {
        ClientLog("[STRICT-ROSTER] Authority start rejected: " + hostDecision.code + ".");
        return PolicyResult(hostDecision);
    }
    if (sameWorldRouteRecovery && hostDecision.confirmed)
    {
        return StrictAuthorityStartFailure("host_preservation_proof_required",
            "a live HOST requires its exact preserved connection proof when the authority route advances");
    }
    // StartServer creates the listen host PlayerController and invokes
    // PostLogin before it returns. Publish the signed HOST seat first, so
    // local-host admission can bind without waiting for UniqueId. The actual
    // native travel is queued to the game-thread boundary below.
    if (*hostingKind == "P2P")
        SetStrictRosterLocalHostSeat(hostDecision);
    if (gStrictAuthorityAwaitingWorldTeardown.load(std::memory_order_acquire))
    {
        // Cleanup ownership is released only by the game-thread observer once
        // the old world/driver has disappeared.  Do not clear this flag or
        // probe UWorld from a pipe callback.
        ClearStrictRosterLocalHostSeat();
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_world_not_cleared"},
            {"message", "the previous native authority world is still being torn down"},
            {"world_teardown_required", true}
        };
    }
    for (int wait = 0;
        wait < 1500 && !gStrictAuthorityRuntimeReady.load(std::memory_order_acquire);
        ++wait)
    {
        Sleep(10);
    }
    if (!gStrictAuthorityRuntimeReady.load(std::memory_order_acquire))
    {
        gStrictRosterPolicy.Reset();
        ClearStrictRosterLocalHostSeat();
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_runtime_not_ready"},
            {"message", "the listen authority runtime is not initialized"}
        };
    }

    authorityServerStarted = false;
    std::string authorityWorldInstanceId;
    std::string authorityNonce = hostDecision.nativeConnectionNonce;
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        authorityServerStarted = gStrictAuthorityServerStarted.load(
            std::memory_order_acquire);
    }
    if (!authorityServerStarted)
    {
        // StartServer touches UWorld and must be dispatched by the game-thread
        // boundary.  The listener thread waits for the scoped result instead
        // of performing native travel itself.
        const nlohmann::json nativeStart = QueueStrictAuthorityWorldStart(
            *hostingKind,
            hostDecision.nativeConnectionNonce,
            CurrentStrictAuthorityWorldInstanceId(),
            *allocationScope);
        if (!nativeStart.value("accepted", false))
        {
            // A timed-out/late game-thread request owns a possible native
            // side effect until its scoped cleanup is observed.  Resetting the
            // policy here would let a new allocation race that old world.
            const bool cleanupOwned =
                nativeStart.value("status", std::string{}) == "cleanup_pending" ||
                nativeStart.value("world_teardown_required", false) ||
                gStrictAuthorityStartQueue.HasActive();
            if (!cleanupOwned)
            {
                gStrictRosterPolicy.Reset();
                ClearStrictRosterLocalHostSeat();
            }
            return nativeStart;
        }
        authorityWorldInstanceId = nativeStart.value(
            "world_instance_id", std::string{});
        authorityNonce = nativeStart.value(
            "native_connection_nonce", std::string{});
        if (authorityWorldInstanceId.empty() ||
            authorityNonce != hostDecision.nativeConnectionNonce)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_start_scope_mismatch"},
                {"message", "native authority returned a mismatched world or nonce"},
                {"world_teardown_required", true}
            };
        }
    }
    else if (sameWorldRouteRecovery)
    {
        authorityWorldInstanceId = readyLease->worldInstanceId;
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        if (gStrictAuthorityReadyLease.active &&
            gStrictAuthorityReadyLease.worldInstanceId == authorityWorldInstanceId)
        {
            gStrictAuthorityReadyLease.nativeConnectionNonce = authorityNonce;
            gStrictAuthorityReadyLease.allocationScope = *allocationScope;
        }
    }
    if (authorityWorldInstanceId.empty())
        authorityWorldInstanceId = CurrentStrictAuthorityWorldInstanceId();
    std::uint64_t clientOperationSequence = 0;
    if (*hostingKind == "P2P")
    {
        const nlohmann::json localHostScope{
            {"attempt_id", allocationScope->attemptId},
            {"authority_session_id", allocationScope->authoritySessionId},
            {"world_instance_id", authorityWorldInstanceId},
            {"roster_revision", allocationScope->rosterRevision},
            {"route_generation", allocationScope->routeGeneration},
            {"player_id", hostDecision.playerId},
            {"grant_jti", ""}, {"room_role", "HOST"},
            {"connection_generation", hostDecision.connectionGeneration}};
        const auto staged = StageLocalAuthorityClientScope(localHostScope, authorityNonce);
        clientOperationSequence = staged.value("operation_sequence", 0ULL);
        if (!staged.value("accepted", false) || clientOperationSequence == 0)
        {
            const bool cleanupOwned = QueueFailedHostStageCleanup(
                *allocationScope, authorityWorldInstanceId, authorityNonce);
            return nlohmann::json{
                {"accepted", false},
                {"status", "cleanup_pending"},
                {"cleanup_owner_registered", cleanupOwned},
                {"native_cleared", false},
                {"code", staged.value("code", "native_host_scope_unavailable")},
                {"message", "the native authority world started but its local HOST scope could not be staged"},
                {"world_instance_id", authorityWorldInstanceId},
                {"world_teardown_required", true}};
        }
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        if (gStrictAuthorityReadyLease.active &&
            gStrictAuthorityReadyLease.worldInstanceId == authorityWorldInstanceId)
            gStrictAuthorityReadyLease.clientOperationSequence = clientOperationSequence;
    }
    ClientLog("[STRICT-ROSTER] Authority admission activated.");
    return nlohmann::json{
        {"accepted", true},
        {"code", "accepted"},
        {"endpoint_host", endpointHost},
        {"endpoint_port", endpointPort},
        {"world_instance_id", authorityWorldInstanceId},
        {"native_connection_nonce", authorityNonce},
        {"operation_sequence", clientOperationSequence}
    };
}

nlohmann::json OnMatchConnectionEvents(const nlohmann::json& arguments)
{
    const std::uint64_t afterSequence = arguments.value("after_sequence", 0ULL);
    const auto events = gStrictRosterPolicy.ConnectionEventsAfter(afterSequence);
    nlohmann::json items = nlohmann::json::array();
    std::uint64_t nextSequence = afterSequence;
    for (const auto& event : events)
    {
        nextSequence = event.sequence;
        items.push_back(nlohmann::json{
            {"sequence", event.sequence},
            {"attempt_id", event.attemptId},
            {"authority_session_id", event.authoritySessionId},
            {"roster_revision", event.rosterRevision},
            {"world_instance_id", event.worldInstanceId},
            {"route_generation", event.routeGeneration},
            {"player_id", event.playerId},
            {"grant_jti", event.grantJti},
            {"native_connection_nonce", event.nativeConnectionNonce},
            {"connection_generation", event.connectionGeneration},
            {"state", event.state.empty()
                ? (event.connected ? "CONNECTED" : "DISCONNECTED")
                : event.state}
        });
    }
    return nlohmann::json{
        {"events", std::move(items)},
        {"next_sequence", nextSequence}
    };
}

nlohmann::json ValidateStrictRosterReceiptScope(
    const nlohmann::json& arguments)
{
    try
    {
        const std::string attemptId = arguments.value("attempt_id", "");
        const std::string authoritySession = arguments.value(
            "authority_session_id", "");
        const std::string worldInstanceId = arguments.value(
            "world_instance_id", "");
        const std::string playerId = arguments.value("player_id", "");
        const std::string grantJti = arguments.value("grant_jti", "");
        const std::string nativeConnectionNonce = arguments.value(
            "native_connection_nonce", "");
        const std::int64_t rosterRevision = arguments.value(
            "roster_revision", std::int64_t{0});
        const int routeGeneration = arguments.value("route_generation", 0);
        const int generation = arguments.value("connection_generation", 0);
        if (attemptId.empty() || authoritySession.empty() ||
            worldInstanceId.empty() || playerId.empty() || grantJti.empty() ||
            nativeConnectionNonce.empty() || rosterRevision < 1 ||
            routeGeneration < 1 || generation < 1)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "invalid_request"},
                {"message", "scoped admission fields are required"}
            };
        }
        if (worldInstanceId != CurrentStrictAuthorityWorldInstanceId())
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "world_instance_mismatch"},
                {"message", "the receipt belongs to a different native world"}
            };
        }
        const StrictRoster::Decision scope =
            gStrictRosterPolicy.ValidateConnectionScope(
                attemptId,
                authoritySession,
                rosterRevision,
                routeGeneration,
                playerId,
                generation,
                grantJti,
                nativeConnectionNonce);
        if (!scope.accepted)
            return PolicyResult(scope);
        return nlohmann::json{{"accepted", true}, {"code", "accepted"}};
    }
    catch (...)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_request"},
            {"message", "scoped admission fields have invalid types"}
        };
    }
}

nlohmann::json OnConfirmMatchAdmission(const nlohmann::json& arguments)
{
    const nlohmann::json scope = ValidateStrictRosterReceiptScope(arguments);
    if (!scope.value("accepted", false))
        return scope;
    return QueueStrictRosterAdmissionReservation(arguments);
}

nlohmann::json OnConfirmMatchConnection(const nlohmann::json& arguments)
{
    const nlohmann::json scope = ValidateStrictRosterReceiptScope(arguments);
    if (!scope.value("accepted", false))
        return scope;
    return QueueStrictRosterConnectionConfirmation(arguments);
}

nlohmann::json OnReleaseMatchAdmission(const nlohmann::json& arguments)
{
    const nlohmann::json scope = ValidateStrictRosterReceiptScope(arguments);
    if (!scope.value("accepted", false))
        return scope;
    return QueueStrictRosterAdmissionRelease(arguments);
}

// Hooks.cpp needs the pinned native net-mode read without depending on the
// bootstrap's anonymous-namespace implementation.  Keep the RVA logic in the
// internal helper while exposing this narrow read-only bridge.
int GetNativeNetMode(UWorld* world)
{
    return GetNativeNetModeInternal(world);
}

namespace
{
    nlohmann::json StrictRosterCleanupPendingResult(
        const std::string_view code,
        const std::string_view message,
        const std::string_view attemptId,
        const std::string_view authoritySessionId,
        const std::string_view worldInstanceId,
        const std::int64_t rosterRevision,
        const int routeGeneration,
        const bool teardownRequested = false,
        const bool processExitRequested = false)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", code},
            {"status", "cleanup_pending"},
            {"message", message},
            {"native_cleared", false},
            {"attempt_id", attemptId},
            {"authority_session_id", authoritySessionId},
            {"world_instance_id", worldInstanceId},
            {"roster_revision", rosterRevision},
            {"route_generation", routeGeneration},
            {"world_teardown_required", true},
            {"native_teardown_mode", processExitRequested
                ? "process_exit"
                : (teardownRequested ? "world_return_to_menu" : "not_requested")},
            {"process_exit_manager_required", processExitRequested}
        };
    }

    StrictRosterCleanupReceipt::Scope StrictRosterCleanupReceiptScope(
        const StrictRosterCleanupState& cleanup)
    {
        return StrictRosterCleanupReceipt::Scope{
            cleanup.attemptId,
            cleanup.authoritySessionId,
            cleanup.worldInstanceId,
            cleanup.rosterRevision,
            cleanup.routeGeneration};
    }

    nlohmann::json StrictRosterCleanupReplayResult(
        const StrictRosterCleanupReceipt::Scope& scope)
    {
        // This is deliberately a pure response.  It is only reachable when
        // the exact completed scope is journaled and no newer allocation or
        // cleanup lease exists; it must never repeat Steam/policy/world work.
        return nlohmann::json{
            {"accepted", true},
            {"code", "cleared"},
            {"status", "cleared"},
            {"native_cleared", true},
            {"attempt_id", scope.attemptId},
            {"authority_session_id", scope.authoritySessionId},
            {"world_instance_id", scope.worldInstanceId},
            {"roster_revision", scope.rosterRevision},
            {"route_generation", scope.routeGeneration},
            {"world_teardown_required", false}
        };
    }

    nlohmann::json StrictRosterCleanupClearedResult(
        const StrictRosterCleanupState& cleanup)
    {
        // The final ACK is emitted only after the native world/driver observer
        // proves teardown.  Commit the exact completed scope before releasing
        // the live cleanup state so a lost ACK can be replayed without
        // repeating Steam/policy/world side effects.
        const StrictRosterCleanupReceipt::Scope receiptScope =
            StrictRosterCleanupReceiptScope(cleanup);
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            if (!gStrictRosterCleanupReceiptJournal.RecordCompleted(
                    receiptScope, true))
            {
                return StrictRosterCleanupPendingResult(
                    "cleanup_receipt_unavailable",
                    "native teardown was observed but its scoped receipt could not be recorded",
                    cleanup.attemptId, cleanup.authoritySessionId,
                    cleanup.worldInstanceId, cleanup.rosterRevision,
                    cleanup.routeGeneration);
            }
            gStrictRosterCleanup = StrictRosterCleanupState{};
        }

        // Any policy/Steam proof still cached at this boundary is revoked
        // before the allocation can be reused.  The live-connection path
        // intentionally deferred Reset until the world teardown was observed
        // so real disconnect callbacks could finish against the old scope.
        gStrictRosterPolicy.Reset();
        StrictRosterSteamAuth::ClearAllExpectedProofs();
        ClearStrictAuthorityWorldIdentity(cleanup.retiredWorld);
        ClearStrictRosterLocalHostSeat();
        ClearStrictRosterControllerSeats();
        gStrictAuthorityAwaitingWorldTeardown.store(
            false, std::memory_order_release);
        gStrictAuthorityWorldListening.store(false, std::memory_order_release);
        {
            std::lock_guard<std::mutex> startLock(gStrictAuthorityStartMutex);
            gStrictAuthorityServerStarted.store(false, std::memory_order_release);
            gStrictAuthorityReadyLease = StrictAuthorityReadyLease{};
        }
        ClientLog("[STRICT-ROSTER] Native world and NetDriver teardown observed; "
                  "allocation is cleared.");
        return nlohmann::json{
            {"accepted", true},
            {"code", "cleared"},
            {"status", "cleared"},
            {"native_cleared", true},
            {"attempt_id", cleanup.attemptId},
            {"authority_session_id", cleanup.authoritySessionId},
            {"world_instance_id", cleanup.worldInstanceId},
            {"roster_revision", cleanup.rosterRevision},
            {"route_generation", cleanup.routeGeneration},
            {"world_teardown_required", false}
        };
    }
}

nlohmann::json ProcessStrictRosterClearMatchAllocationResultInternal(
    const nlohmann::json& arguments)
{
    try
    {
        const std::string attemptId = arguments.value("attempt_id", "");
        const std::string authoritySessionId =
            arguments.value("authority_session_id", "");
        const std::string worldInstanceId =
            arguments.value("world_instance_id", "");
        const std::int64_t rosterRevision =
            arguments.value("roster_revision", std::int64_t{0});
        const int routeGeneration = arguments.value("route_generation", 0);
        if (attemptId.empty() || authoritySessionId.empty() ||
            worldInstanceId.empty() || rosterRevision < 1 || routeGeneration < 1)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "invalid_request"},
                {"message", "clear requires the complete allocation/world scope"},
                {"native_cleared", false},
                {"world_teardown_required", true}
            };
        }

        StrictRosterCleanupState cleanup;
        bool alreadyPending = false;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            cleanup = gStrictRosterCleanup;
            alreadyPending = cleanup.pending;
        }

        if (alreadyPending)
        {
            if (!StrictRosterCleanupScopeEquals(
                    cleanup, attemptId, authoritySessionId, worldInstanceId,
                    rosterRevision, routeGeneration))
            {
                return nlohmann::json{
                    {"accepted", false},
                    {"code", "cleanup_scope_mismatch"},
                    {"message", "a delayed clear belongs to another allocation"},
                    {"native_cleared", false},
                    {"world_instance_id", cleanup.worldInstanceId},
                    {"world_teardown_required", true}
                };
            }

            if (!StrictNativeWorldTeardownComplete())
            {
                if (!cleanup.teardownRequested)
                {
                    const StrictRosterNativeTeardownRequestResult teardownResult =
                        RequestScopedStrictRosterWorldTeardown(cleanup);
                    const bool requested = teardownResult !=
                        StrictRosterNativeTeardownRequestResult::NotRequested;
                    const bool processExitRequested = teardownResult ==
                        StrictRosterNativeTeardownRequestResult::DedicatedProcessExitRequested;
                    std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
                    if (gStrictRosterCleanup.pending &&
                        StrictRosterCleanupScopeEquals(
                            gStrictRosterCleanup, attemptId,
                            authoritySessionId, worldInstanceId,
                            rosterRevision, routeGeneration))
                    {
                        gStrictRosterCleanup.teardownRequested = requested;
                        gStrictRosterCleanup.processExitRequested = processExitRequested;
                        cleanup.teardownRequested = requested;
                        cleanup.processExitRequested = processExitRequested;
                    }
                }
                return StrictRosterCleanupPendingResult(
                    "cleanup_pending",
                    "native world and NetDriver teardown is still pending",
                    attemptId, authoritySessionId, worldInstanceId,
                    rosterRevision, routeGeneration,
                    cleanup.teardownRequested,
                    cleanup.processExitRequested);
            }
            return StrictRosterCleanupClearedResult(cleanup);
        }

        const auto allocationScope = gStrictRosterPolicy.CurrentAllocationScope();
        if (!allocationScope)
        {
            const StrictRosterCleanupReceipt::Scope requestedScope{
                attemptId, authoritySessionId, worldInstanceId,
                rosterRevision, routeGeneration};
            bool canReplay = false;
            {
                std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
                canReplay = gStrictRosterCleanupReceiptJournal.CanReplay(
                    requestedScope, false, gStrictRosterCleanup.pending);
            }
            if (canReplay)
                return StrictRosterCleanupReplayResult(requestedScope);
            return nlohmann::json{
                {"accepted", false},
                {"code", "allocation_unavailable"},
                {"message", "no active allocation matches the clear request"},
                {"native_cleared", false},
                {"world_teardown_required", false}
            };
        }
        if (allocationScope->attemptId != attemptId ||
            allocationScope->authoritySessionId != authoritySessionId ||
            allocationScope->rosterRevision != rosterRevision ||
            allocationScope->routeGeneration != routeGeneration)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "clear_scope_mismatch"},
                {"message", "clear scope does not match the active allocation"},
                {"native_cleared", false},
                {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()},
                {"world_teardown_required", true}
            };
        }
        if (worldInstanceId != CurrentStrictAuthorityWorldInstanceId())
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "world_instance_mismatch"},
                {"message", "clear scope does not match the active native world"},
                {"native_cleared", false},
                {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()},
                {"world_teardown_required", true}
            };
        }
        UWorld* retiredWorld = UWorld::GetWorld();
        NetDriverAccess::Snapshot snapshot{};
        if (NetDriverAccess::TryGetSnapshot(snapshot, false))
        {
            if (!retiredWorld)
                retiredWorld = snapshot.World;
        }
        UNetDriver* retiredNetDriver = snapshot.NetDriver;
        if (!retiredNetDriver && retiredWorld)
            retiredNetDriver = retiredWorld->NetDriver;
        const StrictRosterCleanupState capturedCleanup{
            true, false, false, attemptId, authoritySessionId, worldInstanceId,
            rosterRevision, routeGeneration, retiredWorld, retiredNetDriver,
            retiredWorld == nullptr};

        if (gStrictRosterPolicy.HasLiveConnections() ||
            gStrictRosterPolicy.HasPendingAdmissions())
        {
            // A live connection or reservation is the reason teardown has not
            // started yet.  Register the exact cleanup owner first, then ask
            // the native world to return/exit so those connections can produce
            // their real disconnect callbacks.  Do not Reset the policy or
            // fabricate DISCONNECTED here: the pending state must own the
            // still-live world until the observer proves it is gone.
            cleanup = capturedCleanup;
            const StrictAuthorityCleanupPlan plan{
                cleanup,
                "allocation clear deferred while native connections or admissions remain"};
            if (!RegisterStrictAuthorityCleanupOwner(plan))
            {
                std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
                cleanup = gStrictRosterCleanup;
            }
            else
            {
                const StrictRosterNativeTeardownRequestResult teardownResult =
                    RequestScopedStrictRosterWorldTeardown(cleanup);
                const bool requested = teardownResult !=
                    StrictRosterNativeTeardownRequestResult::NotRequested;
                const bool processExitRequested = teardownResult ==
                    StrictRosterNativeTeardownRequestResult::DedicatedProcessExitRequested;
                std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
                if (gStrictRosterCleanup.pending &&
                    StrictRosterCleanupScopeEquals(
                        gStrictRosterCleanup, attemptId, authoritySessionId,
                        worldInstanceId, rosterRevision, routeGeneration))
                {
                    gStrictRosterCleanup.teardownRequested = requested;
                    gStrictRosterCleanup.processExitRequested = processExitRequested;
                    cleanup.teardownRequested = requested;
                    cleanup.processExitRequested = processExitRequested;
                }
            }
            ClientLog("[STRICT-ROSTER] Allocation clear deferred: native connections "
                "or admission reservations remain active; scoped teardown requested.");
            return StrictRosterCleanupPendingResult(
                "cleanup_pending",
                "native connections and admission reservations remain; native teardown is pending",
                attemptId, authoritySessionId, worldInstanceId,
                rosterRevision, routeGeneration,
                cleanup.teardownRequested,
                cleanup.processExitRequested);
        }

        cleanup = capturedCleanup;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            gStrictRosterCleanup = cleanup;
        }
        gStrictAuthorityAwaitingWorldTeardown.store(
            true, std::memory_order_release);
        gStrictAuthorityServerStarted.store(false, std::memory_order_release);

        // Reset only releases the Payload's grant/seat caches.  The separate
        // world/driver observation above remains authoritative for NativeCleared.
        gStrictRosterPolicy.Reset();
        StrictRosterSteamAuth::ClearAllExpectedProofs();
        const StrictRosterNativeTeardownRequestResult teardownResult =
            RequestScopedStrictRosterWorldTeardown(cleanup);
        const bool requested = teardownResult !=
            StrictRosterNativeTeardownRequestResult::NotRequested;
        const bool processExitRequested = teardownResult ==
            StrictRosterNativeTeardownRequestResult::DedicatedProcessExitRequested;
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            if (gStrictRosterCleanup.pending)
            {
                gStrictRosterCleanup.teardownRequested = requested;
                gStrictRosterCleanup.processExitRequested = processExitRequested;
            }
            cleanup = gStrictRosterCleanup;
        }
        ClearStrictRosterLocalHostSeat();
        ClearStrictRosterControllerSeats();
        ClientLog("[STRICT-ROSTER] Match allocation clear entered cleanup_pending; "
                  "waiting for native world teardown.");
        if (StrictNativeWorldTeardownComplete())
            return StrictRosterCleanupClearedResult(cleanup);
        return StrictRosterCleanupPendingResult(
            "cleanup_pending",
            requested
                ? "native return-to-menu requested; world teardown is still pending"
                : "native controller return-to-menu was unavailable; world teardown is still pending",
            attemptId, authoritySessionId, worldInstanceId,
            rosterRevision, routeGeneration,
            cleanup.teardownRequested,
            cleanup.processExitRequested);
    }
    catch (...)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_request"},
            {"message", "clear scope has invalid types"},
            {"native_cleared", false},
            {"world_teardown_required", true}
        };
    }
}

nlohmann::json ProcessStrictRosterClearMatchAllocationResult(
    const nlohmann::json& arguments)
{
    return ProcessStrictRosterClearMatchAllocationResultInternal(arguments);
}

nlohmann::json OnClearMatchAllocationResult(const nlohmann::json& arguments)
{
    // UWorld/NetDriver observation and the native return-to-menu request are
    // game-thread operations. The pipe callback only submits the fully scoped
    // clear request and waits for the observer-backed result.
    return QueueStrictRosterClearOnGameThread(arguments);
}

bool StartServerCommandFramework()
{
    if (MatchPipeName.empty())
        return true;
    {
        std::lock_guard<std::mutex> lock(g_CmdFrameworkMutex);
        if (g_CmdFramework != nullptr)
            return true;
    }

    auto framework = std::make_unique<CommandFramework>();
    framework->SetPipeName(MatchPipeName);
    framework->SetLogCallback([](const std::string& msg) { Log(msg); });
    framework->SetMatchAllocationCallback(OnInstallMatchAllocation);
    framework->SetMatchJoinGrantCallback(OnInstallMatchJoinGrant);
    framework->SetMatchAuthorityCallback(OnStartMatchAuthority);
    framework->SetMatchConnectionEventsCallback(OnMatchConnectionEvents);
    framework->SetMatchClearResultCallback(OnClearMatchAllocationResult);
    framework->SetPayloadStatusCallback(BuildPayloadStatus);
    framework->SetMatchCancelCallback(CancelPendingClientTransition);
    framework->SetMatchAdmissionReservationCallback(OnConfirmMatchAdmission);
    framework->SetMatchConnectionConfirmationCallback(OnConfirmMatchConnection);
    framework->SetMatchAdmissionReleaseCallback(OnReleaseMatchAdmission);
    framework->SetClientMatchConnectionConfirmationCallback(ConfirmClientMatchConnection);
    framework->SetServerStatusCallback([]()
        {
            const nlohmann::json current = BuildServerStatusPayload();
            const int reportedPlayerCount = current.value("playerCount", 0);
            const int playerCount = reportedPlayerCount < 0 ? 0 : reportedPlayerCount;
            std::string authorityDetail;
            const bool authorityReady = IsAuthoritativeListeningWorld(
                UWorld::GetWorld(), authorityDetail);
            const bool clientLoginReady =
                !amListenServer || IsClientLoginReadyForTravel();
            const std::string lifecycle =
                current.value("lifecycleState", "Disabled");
            std::string state = playerCount > 0 ? "RUNNING" : "READY";
            if (lifecycle == "Traveling" || lifecycle == "LoadingNext")
                state = "TRANSITIONING";
            else if (lifecycle == "Voting" || lifecycle == "WaitingToTravel")
                state = "VOTING";
            else if (lifecycle == "FallbackExit")
                state = "RESTARTING";
            return nlohmann::json{
                {"state", state},
                {"player_count", playerCount},
                {"round_state", current.value("serverState", "Unknown")},
                {"lifecycle_state", lifecycle},
                {"active_map", current.value("activeMap", current.value("map", ""))},
                {"next_map", current.value("nextMap", "")},
                {"match_generation", current.value("matchGeneration", 0ULL)},
                {"authority_ready", authorityReady},
                {"client_login_ready", clientLoginReady},
                {"vote", current.value("vote", nlohmann::json::object())}
            };
        });

    if (!framework->Start())
    {
        Log("[PIPE] Toolbox command framework failed to start.");
        return false;
    }
    std::lock_guard<std::mutex> lock(g_CmdFrameworkMutex);
    g_CmdFramework = framework.release();
    return true;
}

// Explicit DLL unloaders must call this outside DllMain before unloading the
// module. Process termination itself is left to Windows, avoiding a blocking
// join while the loader lock is held.
extern "C" __declspec(dllexport) void ShutdownPayloadCommandFramework()
{
    CommandFramework* framework = nullptr;
    bool calledFromListener = false;
    {
        std::lock_guard<std::mutex> lock(g_CmdFrameworkMutex);
        if (g_CmdFramework != nullptr && g_CmdFramework->IsListenerThread())
        {
            calledFromListener = true;
        }
        else
        {
            framework = g_CmdFramework;
            g_CmdFramework = nullptr;
        }
    }

    if (calledFromListener)
    {
        ClientLog("[PIPE] Shutdown must be requested by an external owner thread.");
        return;
    }
    if (framework != nullptr)
    {
        framework->Stop();
        delete framework;
    }
    ShutdownStrictRosterPlatformAuth();

    // Explicit unloaders invoke this outside the loader lock, so this is also
    // the safe place to join the loadout HTTP worker.
    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        LoadoutManager* loadoutManager = gLoadoutManager;
        gLoadoutManager = nullptr;
        if (loadoutManager)
        {
            loadoutManager->StopServer();
            delete loadoutManager;
        }
    }
}

// ======================================================
//  SECTION 15 — MAIN THREAD (ENTRY LOGIC)
// ======================================================

void MainThread()
{
    ClientLog("[BOOT] DLL injected, starting...");
    ClientLog("[BOOT] Build profile: BattleLog extraction; server loadout bridge enabled when configured.");
    try
    {
        // Calms down the ui font missing panic
        InitMessageBoxHook();

        BaseAddress = (uintptr_t)GetModuleHandleA(nullptr);

        const std::string commandLine = GetCommandLineA();
        const bool serverBootstrap =
            CommandLinePolicy::HasExactSwitch(commandLine, "-server");
        const bool strictAuthorityBootstrap =
            CommandLinePolicy::HasExactSwitch(commandLine, "-StrictRosterAuthority");
        const bool roomAuthorityBootstrap =
            CommandLinePolicy::HasExactSwitch(commandLine, "-RoomAuthority");
        const bool explicitOfflinePveBootstrap =
            StrictRosterAdmissionGate::IsExplicitOfflinePve(commandLine);
        std::string executableHash;
        if (!VerifySupportedExecutable(BaseAddress, executableHash))
        {
            ClientLog("[BOOT] Refusing initialization: executable build guard failed.");
            return;
        }
        ClientLog("[BOOT] Pinned executable SHA-256=" + executableHash);
        gVerifiedExecutableHash = executableHash;
        if (!serverBootstrap && !ApplyLocalPveQosRedirect(commandLine))
        {
            ClientLog("[BOOT] Refusing client initialization: local PvE QoS guard failed.");
            return;
        }
        if (!serverBootstrap && !ApplyNativeRpcFrameLimitPatch(BaseAddress))
        {
            ClientLog("[BOOT] Refusing client initialization: executable build guard failed.");
            return;
        }

        UC::FMemory::Init((void*)(BaseAddress + 0x18f4350));

        if (serverBootstrap)
        {
            amServer = true;
        }

        // Initialize DebugTool (shared between client and server)
        if (!gDebugTool)
        {
            gDebugTool = new DebugTool();
        }

        while (!UWorld::GetWorld())
        {
            if (amServer)
            {
                *(__int8*)(BaseAddress + 0x5ce2404) = 0;
                *(__int8*)(BaseAddress + 0x5ce2405) = 1;
            }
            Sleep(1);
        }

        UWorld* const initialWorld = UWorld::GetWorld();
        const int initialNetMode = GetNativeNetMode(initialWorld);
        // A dedicated bootstrap has to create/travel the authoritative world
        // later in StartServer. The temporary startup world can still report
        // standalone/client even though the exact -server token requested the
        // dedicated path. Treat that one transition as provisional, then
        // verify the post-travel world below.
        const bool listenAuthorityBootstrap =
            StrictRosterAdmissionGate::IsListenAuthorityBootstrap(
                serverBootstrap, strictAuthorityBootstrap, roomAuthorityBootstrap);
        const bool dedicatedAuthorityBootstrap =
            StrictRosterAdmissionGate::IsDedicatedAuthorityBootstrap(
                serverBootstrap, strictAuthorityBootstrap);
        const int nativeNetMode = listenAuthorityBootstrap &&
            (initialNetMode == 0 || initialNetMode == 3)
                ? 2
                : (serverBootstrap && (initialNetMode == 0 || initialNetMode == 3)
                    ? 1 : initialNetMode);
        const bool runServer = nativeNetMode == 1 || nativeNetMode == 2;
        const bool runClient = nativeNetMode == 0 || nativeNetMode == 2 || nativeNetMode == 3;
        ClientLog(std::string("[BOOT] bootstrap=") +
            (serverBootstrap ? "server" : "client") +
            " initial_net_mode=" + NetModeName(initialNetMode) +
            " routed_net_mode=" + NetModeName(nativeNetMode));
        if (dedicatedAuthorityBootstrap && nativeNetMode == 2)
        {
            ClientLog("[BOOT] Refusing strict dedicated bootstrap: routed to listen mode.");
            return;
        }
        if (nativeNetMode < 0 || nativeNetMode > 3 ||
            (!serverBootstrap && !listenAuthorityBootstrap && nativeNetMode == 1))
        {
            ClientLog("[BOOT] Refusing initialization: bootstrap/native NetMode conflict.");
            return;
        }
        amServer = runServer;
        amListenServer = nativeNetMode == 2;
        if (runClient && serverBootstrap && !ApplyNativeRpcFrameLimitPatch(BaseAddress))
        {
            ClientLog("[BOOT] Refusing listen-client initialization: frame patch failed.");
            return;
        }

        bool clientFrontendRuntimeInitialized = false;
        bool clientArchiveHooksInitialized = false;
        const auto initializeClientFrontendRuntime = [&]()
        {
            if (clientFrontendRuntimeInitialized)
                return;
            LoadClientConfig();
            if (ClientDebugLogEnabled)
            {
                std::filesystem::create_directory("clientlogs");
                const std::string path =
                    "clientlogs/clientlog-" + CurrentTimestamp() + ".txt";
                clientLogFile.open(path, std::ios::app);
                std::cout << "[CLIENT] Debug logging enabled: " << path << std::endl;
            }
            InitDebugConsole();
            EnableUnrealConsole();
            clientFrontendRuntimeInitialized = true;
        };
        if (runServer && runClient)
        {
            // Listen authority must observe the native frontend login before
            // it travels. Bring up its client logging and archive completions
            // before the server worker begins waiting for that signal.
            initializeClientFrontendRuntime();
            InitClientArchiveHooks();
            clientArchiveHooksInitialized = true;
        }

        // DebugLocateSubsystems();
        // DebugDumpSubsystemsToFile();

        if (runServer)
        {
            InitServerHooks(serverBootstrap || nativeNetMode == 1);
            const bool strictNativeHooksReady =
                InitStrictRosterAdmissionHooks(
                    &gStrictRosterPolicy, strictAuthorityBootstrap);
            gStrictNativeHooksReady.store(strictNativeHooksReady, std::memory_order_release);
            gStrictRosterPolicy.SetNativeAdmissionPathReady(strictNativeHooksReady);
            Log("[SERVER] Hooks installed.");

            // Any server process outside the explicitly isolated local-PVE
            // launcher is an online authority.  Do not leave the native
            // listener reachable when the pinned PreLogin/Team/Camp gates did
            // not install, and do not allow a bare -server bootstrap to fall
            // back to the original unrestricted listener.
            const bool onlineAuthorityBootstrap =
                runServer && !explicitOfflinePveBootstrap;
            if (onlineAuthorityBootstrap && !strictAuthorityBootstrap)
            {
                Log("[STRICT-ROSTER] Refusing online authority bootstrap: "
                    "use the signed StrictRosterAuthority path.");
                return;
            }
            if (!StrictRosterAdmissionGate::MayStartOnlineAuthority(
                    runServer, explicitOfflinePveBootstrap,
                    strictAuthorityBootstrap, strictNativeHooksReady))
            {
                Log("[STRICT-ROSTER] Refusing online authority bootstrap: "
                    "pinned native admission hooks are not ready.");
                return;
            }

            // The room/tunnel identifiers are required before constructing
            // LoadoutManager; StartServer also reloads them before map travel.
            LoadConfig();
            DedicatedMultiMatch::Initialize();

            // Wait for world
            Log("[SERVER] Waiting for UWorld...");
            while (!UWorld::GetWorld())
                Sleep(10);
            Log("[SERVER] UWorld is ready.");

            // Initialize LibReplicate exactly like original code
            libReplicate = new LibReplicate(
                LibReplicate::EReplicationMode::Minimal,
                (void*)(BaseAddress + 0x91AEB0),
                (void*)(BaseAddress + 0x33A66D0),
                (void*)(BaseAddress + 0x31F44F0),
                (void*)(BaseAddress + 0x31F0070),
                (void*)(BaseAddress + 0x18F1810),
                (void*)(BaseAddress + 0x18E5490),
                (void*)(BaseAddress + 0x36CDCE0),
                (void*)(BaseAddress + 0x366ADB0),
                (void*)(BaseAddress + 0x31DA270),
                (void*)(BaseAddress + 0x33DF330),
                (void*)(BaseAddress + 0x2fefbd0),
                (void*)(BaseAddress + 0x3506320));
            Log("[SERVER] LibReplicate initialized.");

            // Initialize LateJoinManager
            gLateJoinManager = new LateJoinManager(
                DidProcStartMatch,
                DidBroadcastRoleSelection,
                PlayerRespawnAllowedMap,
                ReportRoomStartedIfNeeded,
                [](APBPlayerController* playerController)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    return !gLoadoutManager ||
                        gLoadoutManager->CanReleaseRoleSpawn(playerController);
                },
                [](APBPlayerController* playerController)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    if (gLoadoutManager)
                        gLoadoutManager->BeginSpawnDispatch(playerController);
                },
                [](APBPlayerController* playerController)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    if (gLoadoutManager)
                        gLoadoutManager->CompleteSpawnDispatch(playerController);
                },
                [](APBPlayerController* playerController)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    if (gLoadoutManager)
                        gLoadoutManager->FinalizeSpawnRequest(playerController);
                },
                [](APBPlayerController* playerController)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    if (gLoadoutManager)
                        gLoadoutManager->AbandonSpawnRequest(playerController);
                }
            );
            Log("[SERVER] LateJoinManager initialized.");

            const std::string logicServerUrl = GetCmdValue("-LogicServerURL=");
            const bool localPveLoadout = serverBootstrap && Config.IsPvE &&
                CommandLinePolicy::HasExactSwitch(commandLine, "-LocalPveLoadout");
            if (localPveLoadout && !logicServerUrl.empty())
            {
                auto manager = std::make_unique<LoadoutManager>();
                if (manager->StartLocalPveServer(
                    logicServerUrl, GetLoadoutBridgeOptions()))
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    gLoadoutManager = manager.release();
                    Log("[LOADOUT] Local PVE current-user loadout bridge initialized.");
                }
                else
                {
                    Log("[LOADOUT] Local PVE bridge disabled; native defaults remain authoritative.");
                }
            }
            else if (!strictAuthorityBootstrap && !roomAuthorityBootstrap &&
                !HostRoomId.empty() && !logicServerUrl.empty())
            {
                auto manager = std::make_unique<LoadoutManager>();
                if (manager->StartServer(
                    logicServerUrl, HostRoomId, GetLoadoutBridgeOptions()))
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    gLoadoutManager = manager.release();
                    Log("[LOADOUT] Community-room loadout bridge initialized.");
                }
                else
                {
                    Log("[LOADOUT] Bridge disabled; native defaults remain authoritative.");
                }
            }
            else
            {
                Log("[LOADOUT] Missing a valid room bridge or explicit local-PVE bridge; using native defaults.");
            }

            // Listen authorities expose their per-launch status pipe while the
            // local player is still on the frontend. Toolbox can then report
            // login_pending without allowing any map travel to overtake login.
            if (listenAuthorityBootstrap && !StartServerCommandFramework())
                return;

            if (roomAuthorityBootstrap)
            {
                Log("[SERVER] Waiting for local platform login before listen travel.");
                while (!IsClientLoginReadyForTravel())
                    Sleep(50);
                Log("[SERVER] Local platform login settled; starting listen travel.");
            }

            if (!strictAuthorityBootstrap)
            {
                // Publish the loadout bridge before the listen socket begins
                // accepting players so PostLogin cannot race manager creation.
                ::StartServer();
                UWorld* authoritativeWorld = nullptr;
                int postTravelNetMode = -1;
                std::string authorityDetail;
                bool authoritativeListeningWorld = false;
                for (int attempt = 0; attempt < 100; ++attempt)
                {
                    authoritativeWorld = UWorld::GetWorld();
                    postTravelNetMode = GetNativeNetMode(authoritativeWorld);
                    authoritativeListeningWorld =
                        IsAuthoritativeListeningWorld(authoritativeWorld, authorityDetail);
                    if (authoritativeListeningWorld)
                        break;
                    Sleep(10);
                }
                Log(std::string("[SERVER] Post-travel authority: net_mode=") +
                    NetModeName(postTravelNetMode) + " " + authorityDetail +
                    " result=" + (authoritativeListeningWorld ? "ready" : "invalid"));
                if (!authoritativeListeningWorld)
                {
                    std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                    if (gLoadoutManager)
                    {
                        gLoadoutManager->StopServer();
                        delete gLoadoutManager;
                        gLoadoutManager = nullptr;
                    }
                    Log("[LOADOUT] Bridge disabled: post-travel world is not authoritative.");
                }
            }

            // Dedicated bootstraps start the status pipe after travel. Listen
            // bootstraps already started it above so this call is idempotent.
            // A strict Dedicated process never enters runClient below. Its
            // runtime is ready once the server hooks and managers above are
            // initialized; no client frontend is required for this role.
            if (strictAuthorityBootstrap && !runClient)
            {
                gStrictAuthorityRuntimeReady.store(
                    gStrictNativeHooksReady.load(std::memory_order_acquire),
                    std::memory_order_release);
            }
            if (!StartServerCommandFramework())
                return;

            // The retired room heartbeat path is isolated to local PVE. An
            // online authority reports through the scoped Toolbox/backend
            // contract after strict allocation, never through /rooms/*.
            if (explicitOfflinePveBootstrap)
                StartHeartbeatThread();
        }
        if (runClient)
        {
            // We're client
            initializeClientFrontendRuntime();

            if (runServer)
            {
                if (!clientArchiveHooksInitialized)
                    InitClientArchiveHooks();
            }
            else
                InitClientHook();

            //*(const wchar_t***)(BaseAddress + 0x5C63C88) = &LocalURL;
            // auto dump below
            // std::thread(ClientAutoDumpThread).detach();
            // Init Hotkey Check
            // Only start the hotkey thread if the -debug flag is present
            if (CommandLinePolicy::HasExactSwitch(GetCommandLineA(), "-debug"))
            {
                std::thread(HotkeyThreadWithDebugTool).detach();
            }

            if (!runServer && !MatchIP.empty())
            {
                AutoConnectToMatchFromCmdline();
            }

            // Start CommandFramework if a pipe name was provided
            if (!runServer && !MatchPipeName.empty())
            {
                auto framework = std::make_unique<CommandFramework>();
                framework->SetPipeName(MatchPipeName);
                framework->SetJoinCallback(OnJoinFromPipe);
                framework->SetMatchAllocationCallback(OnInstallMatchAllocation);
                framework->SetMatchJoinGrantCallback(OnInstallMatchJoinGrant);
                framework->SetMatchAuthorityCallback(OnStartMatchAuthority);
                framework->SetMatchClearResultCallback(OnClearMatchAllocationResult);
                framework->SetPayloadStatusCallback(BuildPayloadStatus);
                framework->SetMatchCancelCallback(CancelPendingClientTransition);
                framework->SetMatchAdmissionReservationCallback(OnConfirmMatchAdmission);
                framework->SetMatchConnectionConfirmationCallback(OnConfirmMatchConnection);
                framework->SetMatchAdmissionReleaseCallback(OnReleaseMatchAdmission);
                framework->SetClientMatchConnectionConfirmationCallback(ConfirmClientMatchConnection);
                framework->SetLogCallback([](const std::string& msg) { ClientLog(msg); });
                framework->SetDebugCallback([](const nlohmann::json& args) {
                    if (gDebugTool)
                        return gDebugTool->ExecuteJson(args);
                    return nlohmann::json{{"ok", false}, {"error", "DebugTool not initialized"}};
                });

                if (framework->Start())
                {
                    std::lock_guard<std::mutex> lock(g_CmdFrameworkMutex);
                    g_CmdFramework = framework.release();
                }
                else
                {
                    ClientLog("[PIPE] Command framework failed to start.");
                }
            }
            if (strictAuthorityBootstrap)
            {
                gStrictAuthorityRuntimeReady.store(
                    gStrictNativeHooksReady.load(std::memory_order_acquire),
                    std::memory_order_release);
            }
            /*
            Sleep(10 * 1000);

            UCommonActivatableWidget* widget = nullptr;
            reinterpret_cast<UPBMainMenuManager_BP_C*>(getObjectsOfClass(UPBMainMenuManager_BP_C::StaticClass(), false).back())->GetTopMenuWidget(&widget);
            widget->SetVisibility(ESlateVisibility::Hidden);
            widget->DeactivateWidget();

            UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), L"open 73.130.167.222", nullptr);
            */

            // UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), L"open 127.0.0.1", nullptr);
        }
    }
    catch (...)
    {
        std::cout << "[ERROR] Unhandled exception in MainThread!" << std::endl;
        std::cout << "Press ENTER to exit..." << std::endl;
        std::cin.get();
    }
}

// ======================================================
//  SECTION 16 — DLL ENTRY POINT
// ======================================================

BOOL APIENTRY DllMain(HMODULE hModule,
    DWORD ul_reason_for_call,
    LPVOID lpReserved)
{
    if (ul_reason_for_call == DLL_PROCESS_ATTACH)
    {
        gPayloadModule = hModule;
        DisableThreadLibraryCalls(hModule);
        std::thread t(MainThread);

        t.detach();
    }

    return TRUE;
}
