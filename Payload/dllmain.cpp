// Main.cpp
#include <Windows.h>
#include <wincrypt.h>
#include <array>
#include <atomic>
#include <chrono>
#include <charconv>
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
bool gStrictAuthorityServerStarted = false;
bool gStrictAuthorityAwaitingWorldTeardown = false;
std::mutex gStrictAuthorityWorldMutex;
UWorld* gStrictAuthorityWorld = nullptr;
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
};

std::mutex gStrictRosterCleanupMutex;
StrictRosterCleanupState gStrictRosterCleanup;

std::int64_t EpochSecondsNow() noexcept
{
    return std::chrono::duration_cast<std::chrono::seconds>(
        std::chrono::system_clock::now().time_since_epoch()).count();
}

std::string ObserveStrictAuthorityWorld(UWorld* const world)
{
    if (!world)
        return {};
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    if (gStrictAuthorityWorld == world && !gStrictAuthorityWorldInstanceId.empty())
        return gStrictAuthorityWorldInstanceId;
    gStrictAuthorityWorld = world;
    ++gStrictAuthorityWorldSequence;
    std::ostringstream id;
    id << "world_" << std::hex << gStrictAuthorityWorldSequence << "_"
       << reinterpret_cast<std::uintptr_t>(world);
    gStrictAuthorityWorldInstanceId = id.str();
    return gStrictAuthorityWorldInstanceId;
}

std::string CurrentStrictAuthorityWorldInstanceId()
{
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    return gStrictAuthorityWorldInstanceId;
}

void ClearStrictAuthorityWorldIdentity(const UWorld* expectedWorld)
{
    std::lock_guard<std::mutex> lock(gStrictAuthorityWorldMutex);
    if (!expectedWorld || gStrictAuthorityWorld == expectedWorld)
    {
        gStrictAuthorityWorld = nullptr;
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

bool StrictNativeWorldTeardownComplete()
{
    StrictRosterCleanupState cleanup;
    {
        std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
        cleanup = gStrictRosterCleanup;
    }
    if (!cleanup.pending)
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

int GetNativeNetMode(UWorld* world);
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
    constexpr bool nativeClientGrantInjection = false;
    // This is deliberately a separate locked-build capability bit.  Hook
    // installation and an active policy are prerequisites for admission, but
    // this binary has not yet proved the native Team/Camp admission path and
    // therefore must keep the capability false.  It is independent of a
    // current allocation/world so an idle authority can be checked without a
    // scheduler self-lock.
    constexpr bool nativeAuthorityAdmissionVerified = false;
    const bool strictReady = StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        executableVerified, offlinePve, nativeAuthorityPath,
        nativeAuthorityAdmissionVerified, nativeClientGrantInjection);
    const bool payloadReady = StrictRosterAdmissionGate::CanReportPayloadReady(
        executableVerified, strictReady, offlinePve);
    const UWorld* const world = UWorld::GetWorld();
    const int netMode = GetNativeNetMode(const_cast<UWorld*>(world));
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
        {"native_client_grant_injection_ready", nativeClientGrantInjection},
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

int GetNativeNetMode(UWorld* world)
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

CommandFramework::JoinResult OnJoinFromPipe(
    const std::string& ip,
    const std::string& token)
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
        QueueConnectToMatchAuthorizedDetailed(ip, token);
    return CommandFramework::JoinResult{
        result.accepted, result.code, result.message};
}

nlohmann::json OnInstallMatchAllocation(const nlohmann::json& arguments)
{
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
    }
    if (!hostDecision.accepted)
    {
        ClientLog("[STRICT-ROSTER] Authority start rejected: " + hostDecision.code + ".");
        return PolicyResult(hostDecision);
    }
    // StartServer synchronously creates the listen host PlayerController and
    // invokes PostLogin before it returns. Publish the signed HOST seat first,
    // so that local-host admission can bind without waiting for UniqueId.
    if (*hostingKind == "P2P")
        SetStrictRosterLocalHostSeat(hostDecision);
    if (gStrictAuthorityAwaitingWorldTeardown)
    {
        std::string detail;
        if (IsAuthoritativeListeningWorld(UWorld::GetWorld(), detail))
        {
            ClearStrictRosterLocalHostSeat();
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_world_not_cleared"},
                {"message", "the previous native authority world is still listening"}
            };
        }
        gStrictAuthorityAwaitingWorldTeardown = false;
    }
    for (int wait = 0;
        wait < 1500 && !gStrictAuthorityRuntimeReady.load(std::memory_order_acquire);
        ++wait)
    {
        Sleep(10);
    }
    {
        std::lock_guard<std::mutex> lock(gStrictAuthorityStartMutex);
        if (!gStrictAuthorityRuntimeReady.load(std::memory_order_acquire))
        {
            ClearStrictRosterLocalHostSeat();
            return nlohmann::json{
                {"accepted", false},
                {"code", "authority_runtime_not_ready"},
                {"message", "the listen authority runtime is not initialized"}
            };
        }
        if (!gStrictAuthorityServerStarted)
        {
            ::StartServer();
            UWorld* authoritativeWorld = nullptr;
            int postTravelNetMode = -1;
            std::string authorityDetail;
            bool authoritativeListeningWorld = false;
            for (int attempt = 0; attempt < 200; ++attempt)
            {
                authoritativeWorld = UWorld::GetWorld();
                postTravelNetMode = GetNativeNetMode(authoritativeWorld);
                authoritativeListeningWorld =
                    IsAuthoritativeListeningWorld(authoritativeWorld, authorityDetail);
                if (authoritativeListeningWorld)
                    break;
                Sleep(10);
            }
            ClientLog(std::string("[STRICT-ROSTER] Post-travel authority: net_mode=") +
                NetModeName(postTravelNetMode) + " " + authorityDetail +
                " result=" + (authoritativeListeningWorld ? "ready" : "invalid"));
            if (!authoritativeListeningWorld)
            {
                gStrictRosterPolicy.Reset();
                ClearStrictRosterLocalHostSeat();
                ClearStrictRosterControllerSeats();
                return nlohmann::json{
                    {"accepted", false},
                    {"code", "listen_authority_unavailable"},
                    {"message", "the pinned listen world did not become authoritative"}
                };
            }
            gStrictAuthorityServerStarted = true;
            gStrictAuthorityAwaitingWorldTeardown = false;
            ObserveStrictAuthorityWorld(authoritativeWorld);
        }
    }
    ClientLog("[STRICT-ROSTER] Authority admission activated.");
    const std::string formattedEndpoint = CommandProtocol::FormatMatchTarget(
        endpointHost, static_cast<std::uint16_t>(endpointPort));
    if (formattedEndpoint.empty())
    {
        ClearStrictRosterLocalHostSeat();
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_authority_endpoint"},
            {"message", "authority endpoint cannot be represented safely"}
        };
    }
    return nlohmann::json{
        {"accepted", true},
        {"code", "accepted"},
        {"endpoint_host", endpointHost},
        {"endpoint_port", endpointPort},
        {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()},
        {"native_connection_nonce", hostDecision.nativeConnectionNonce}
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
            {"world_instance_id", CurrentStrictAuthorityWorldInstanceId()},
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

    nlohmann::json StrictRosterCleanupClearedResult(
        const StrictRosterCleanupState& cleanup)
    {
        ClearStrictAuthorityWorldIdentity(cleanup.retiredWorld);
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            gStrictRosterCleanup = StrictRosterCleanupState{};
        }
        gStrictAuthorityAwaitingWorldTeardown = false;
        gStrictAuthorityServerStarted = false;
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

nlohmann::json OnClearMatchAllocationResult(const nlohmann::json& arguments)
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
                        RequestStrictRosterNativeWorldTeardown();
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
        if (gStrictRosterPolicy.HasLiveConnections() ||
            gStrictRosterPolicy.HasPendingAdmissions())
        {
            ClientLog("[STRICT-ROSTER] Allocation clear deferred: native connections "
                "or admission reservations remain active.");
            return StrictRosterCleanupPendingResult(
                "cleanup_pending",
                "native connections and admission reservations must clear before teardown",
                attemptId, authoritySessionId, worldInstanceId,
                rosterRevision, routeGeneration);
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
        cleanup = StrictRosterCleanupState{
            true, false, false, attemptId, authoritySessionId, worldInstanceId,
            rosterRevision, routeGeneration, retiredWorld, retiredNetDriver};
        {
            std::lock_guard<std::mutex> lock(gStrictRosterCleanupMutex);
            gStrictRosterCleanup = cleanup;
        }
        gStrictAuthorityAwaitingWorldTeardown = true;
        gStrictAuthorityServerStarted = false;

        // Reset only releases the Payload's grant/seat caches.  The separate
        // world/driver observation above remains authoritative for NativeCleared.
        gStrictRosterPolicy.Reset();
        const StrictRosterNativeTeardownRequestResult teardownResult =
            RequestStrictRosterNativeWorldTeardown();
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
                InitStrictRosterAdmissionHooks(&gStrictRosterPolicy);
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
