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
#include "Communication/CommandFramework.h"
#include "Loadout/LoadoutManager.h"

#include "Config/Config.h"
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

bool ApplyNativeRpcFrameLimitPatch(uintptr_t moduleBase)
{
    const auto dosHeader = reinterpret_cast<const IMAGE_DOS_HEADER*>(moduleBase);
    if (dosHeader->e_magic != IMAGE_DOS_SIGNATURE)
    {
        ClientLog("[NATIVE-RPC] Refusing frame patch: invalid DOS header.");
        return false;
    }
    const auto ntHeaders = reinterpret_cast<const IMAGE_NT_HEADERS64*>(
        moduleBase + dosHeader->e_lfanew);
    if (ntHeaders->Signature != IMAGE_NT_SIGNATURE ||
        ntHeaders->OptionalHeader.SizeOfImage != kSupportedExecutableImageSize)
    {
        ClientLog("[NATIVE-RPC] Refusing frame patch: unsupported executable image.");
        return false;
    }

    std::string executableHash;
    if (!HashExecutable(executableHash) || executableHash != kSupportedExecutableSha256)
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
    const std::string disabled = key + "=0";
    const std::string falseValue = key + "=false";
    return commandLine.find(disabled) == std::string::npos &&
        commandLine.find(falseValue) == std::string::npos;
}

LoadoutBridgeOptions GetLoadoutBridgeOptions()
{
    const std::string commandLine = GetCommandLineA();
    if (commandLine.find("-NativeArchiveOnly") != std::string::npos)
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
        const bool serverProcess = commandLine.find("-server") != std::string::npos;
        if (!serverProcess && !ApplyNativeRpcFrameLimitPatch(BaseAddress))
        {
            ClientLog("[BOOT] Refusing client initialization: executable build guard failed.");
            return;
        }

        UC::FMemory::Init((void*)(BaseAddress + 0x18f4350));

        if (serverProcess)
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
        }

        // DebugLocateSubsystems();
        // DebugDumpSubsystemsToFile();

        if (amServer)
        {
            InitServerHooks();
            Log("[SERVER] Hooks installed.");

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
            if (!HostRoomId.empty() && !logicServerUrl.empty())
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
                Log("[LOADOUT] Missing -LogicServerURL or -roomid; using native defaults.");
            }

            // Publish the loadout bridge before the listen socket begins
            // accepting players so PostLogin cannot race manager creation.
            ::StartServer();

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
                        return nlohmann::json{
                            {"state", playerCount > 0 ? "RUNNING" : "READY"},
                            {"player_count", playerCount},
                            {"round_state", current.value("serverState", "Unknown")}
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
        else
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

            InitClientHook();

            //*(const wchar_t***)(BaseAddress + 0x5C63C88) = &LocalURL;
            // auto dump below
            // std::thread(ClientAutoDumpThread).detach();
            // Init Hotkey Check
            // Only start the hotkey thread if the -debug flag is present
            if (std::string(GetCommandLineA()).find("-debug") != std::string::npos)
            {
                std::thread(HotkeyThreadWithDebugTool).detach();
            }

            if (!MatchIP.empty())
            {
                AutoConnectToMatchFromCmdline();
            }

            // Start CommandFramework if a pipe name was provided
            if (!MatchPipeName.empty())
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
