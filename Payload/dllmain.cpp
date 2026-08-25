// Main.cpp
#include <Windows.h>
#include <wincrypt.h>
#include <array>
#include <cstdint>
#include <cstring>
#include <thread>
#include <fstream>
#include <filesystem>
#include <iostream>
#include <iomanip>
#include <memory>
#include <mutex>
#include <sstream>

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
#include "Loadout/LoadoutManager.h"

#include "Config/Config.h"
#include "Config/CommandLinePolicy.h"
#include "Debug/Debug.h"
#include "Debug/DebugTool.h"
#include "ServerLogic/ServerLogic.h"
#include "ClientLogic/ClientLogic.h"
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

namespace
{
constexpr char kSupportedExecutableSha256[] =
    "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843";
constexpr DWORD kSupportedExecutableImageSize = 105431040;
constexpr uintptr_t kGetNetModeRva = 0x036CC300;
constexpr uintptr_t kRpcFramePatchPageOffset = 0x009C3000;
constexpr SIZE_T kRpcFramePatchPageSize = 0x1000;

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

bool OnJoinFromPipe(const std::string& ip, const std::string& token)
{
    (void)token;
    ClientLog("[PIPE] Join request received: " + ip);
    return QueueConnectToMatch(ip);
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
        std::string executableHash;
        if (!VerifySupportedExecutable(BaseAddress, executableHash))
        {
            ClientLog("[BOOT] Refusing initialization: executable build guard failed.");
            return;
        }
        ClientLog("[BOOT] Pinned executable SHA-256=" + executableHash);
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
        const int nativeNetMode = serverBootstrap &&
            (initialNetMode == 0 || initialNetMode == 3)
                ? 1
                : initialNetMode;
        const bool runServer = nativeNetMode == 1 || nativeNetMode == 2;
        const bool runClient = nativeNetMode == 0 || nativeNetMode == 2 || nativeNetMode == 3;
        ClientLog(std::string("[BOOT] bootstrap=") +
            (serverBootstrap ? "server" : "client") +
            " initial_net_mode=" + NetModeName(initialNetMode) +
            " routed_net_mode=" + NetModeName(nativeNetMode));
        if (nativeNetMode < 0 || nativeNetMode > 3 ||
            (!serverBootstrap && nativeNetMode == 1))
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

        // DebugLocateSubsystems();
        // DebugDumpSubsystemsToFile();

        if (runServer)
        {
            InitServerHooks(serverBootstrap || nativeNetMode == 1);
            Log("[SERVER] Hooks installed.");

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
            else if (!HostRoomId.empty() && !logicServerUrl.empty())
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

            // Toolbox owns enrollment and long-lived node credentials. The
            // in-process server exposes only non-secret runtime status over
            // the per-launch, same-user named pipe.
            if (!MatchPipeName.empty())
            {
                auto framework = std::make_unique<CommandFramework>();
                framework->SetPipeName(MatchPipeName);
                framework->SetLogCallback([](const std::string& msg) { Log(msg); });
                framework->SetServerStatusCallback([]()
                    {
                        const nlohmann::json current = BuildServerStatusPayload();
                        const int reportedPlayerCount = current.value("playerCount", 0);
                        const int playerCount = reportedPlayerCount < 0 ? 0 : reportedPlayerCount;
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
                            {"vote", current.value("vote", nlohmann::json::object())}
                        };
                    });

                if (framework->Start())
                {
                    std::lock_guard<std::mutex> lock(g_CmdFrameworkMutex);
                    g_CmdFramework = framework.release();
                }
                else
                {
                    Log("[PIPE] Toolbox command framework failed to start.");
                }
            }

            // Heartbeat thread (game + backend) – now wrapped in Network
            StartHeartbeatThread();
        }
        if (runClient)
        {
            // We're client
            LoadClientConfig();
            // Initialize client debug log
            if (ClientDebugLogEnabled)
            {
                std::filesystem::create_directory("clientlogs");

                std::string path = "clientlogs/clientlog-" + CurrentTimestamp() + ".txt";
                clientLogFile.open(path, std::ios::app);

                std::cout << "[CLIENT] Debug logging enabled: " << path << std::endl;
            }
            InitDebugConsole();
            EnableUnrealConsole();

            if (runServer)
                InitClientArchiveHooks();
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
