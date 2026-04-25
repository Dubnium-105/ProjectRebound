// ======================================================
//  SECTION 1 — INCLUDES & HEADERS
// ======================================================

#include <thread>
#include <Windows.h>
#include "SDK.hpp"
#include "SDK/Engine_parameters.hpp"
#include "SDK/ProjectBoundary_parameters.hpp"
#include "safetyhook/safetyhook.hpp"
#include "json.hpp"
#include <iostream>
#include <fstream>
#include "libreplicate.h"
#include "LateJoinManager.h"
#include <mutex>
#include <unordered_map>
#include <unordered_set>
#include <chrono>
#include <algorithm>
#include <sstream>
#include <iomanip>
#include <filesystem>
#include <random>
#include <cstdlib>
#include <vector>
#include <winhttp.h>
#pragma comment(lib, "winhttp.lib")

using namespace SDK;


// ======================================================
//  SECTION 2 — LOGGING SYSTEM
// ======================================================

// Helper function to get current timestamp for log file naming
std::string CurrentTimestamp()
{
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);

    std::tm tm{};
    localtime_s(&tm, &t);

    std::ostringstream oss;
    oss << std::put_time(&tm, "%Y%m%d_%H%M%S");
    return oss.str();
}

std::string LogFilePath;
std::mutex LogMutex;

// Initializes the logging system and output to wrapper
void Log(const std::string& msg)
{
    std::cout << msg << std::endl;
}

// ======================================================
//  SECTION 3 — GLOBAL VARIABLES AND FORWARD DECLARATIONS
// ======================================================

SafetyHookInline MessageBoxWHook;

uintptr_t BaseAddress = 0x0;

static LibReplicate* libReplicate;

bool listening = false;
bool amServer = false;

//Client logging to file
bool ClientDebugLogEnabled = false;
std::ofstream clientLogFile;

struct ServerConfig {
    std::wstring MapName;
    std::wstring FullModePath;
    unsigned int ExternalPort;
    unsigned int Port;
    bool IsPvE;
    int MinPlayersToStart;
    std::string ServerName;
    std::string ServerRegion;
};

//Central server ip
std::string OnlineBackendAddress = "";

//Room heartbeat credentials from the desktop browser/match server
std::string HostRoomId = "";
std::string HostToken = "";

//IP from the server browser
std::string MatchIP = "";

//Auto connect checks
bool LoginCompleted = false;
bool ReadyToAutoconnect = false;
int MatchReconnectAttempts = 0;

bool RoomStartReportSucceeded = false;
bool RoomStartReportInFlight = false;
std::mutex RoomStartReportMutex;
void ReportRoomStartedIfNeeded();
bool DisableBackendRoomStartPromotion = true;

static ServerConfig Config{};

void DebugLocateSubsystems();
void InitDebugConsole();
void DebugDumpSubsystemsToFile();
void ClientAutoDumpThread();
void HotkeyThread();

namespace LoadoutExportManager
{
    void NotifyMenuConstructed();
    void TriggerAsyncExport(const std::string& reason, int delayMs = 750);
    void WorkerTick();
    void ServerTick();
    void PreloadSnapshot();
    void RememberMenuSelectedRole(const FName& roleId);
    bool BeginProcessEventWeaponDefinitionOverride(UObject* object, const std::string& functionName, void* parms);
    void EndProcessEventWeaponDefinitionOverride();
    void MaybeOverrideInitWeaponConfig(UObject* object, const std::string& functionName, void* parms);
    void MaybeOverrideProcessEventResult(UObject* object, const std::string& functionName, void* parms);
}

// ======================================================
//  SECTION 4 — UTILITY HELPERS
// ======================================================

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault) {
    std::vector<UObject*> ret = std::vector<UObject*>();

    for (int i = 0; i < SDK::UObject::GObjects->Num(); i++)
    {
        SDK::UObject* Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject() && !includeDefault)
            continue;

        if (Obj->IsA(theClass))
        {
            ret.push_back(Obj);
        }
    }

    return ret;
}

UObject* GetLastOfType(UClass* theClass, bool includeDefault) {
    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
    {
        SDK::UObject* Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject() && !includeDefault)
            continue;

        if (Obj->IsA(theClass))
        {
            return Obj;
        }
    }

    return nullptr;
}
// Client log write
void ClientLog(const std::string& msg)
{
    // Always print to console
    std::cout << msg << std::endl;

    // If debug logging enabled, write to file
    if (ClientDebugLogEnabled && clientLogFile.is_open())
    {
        clientLogFile << msg << std::endl;
        clientLogFile.flush();
    }
}

//Force press space when autoconnect so it wont stuck to wait for player to press
void PressSpace()
{
    INPUT input{};
    input.type = INPUT_KEYBOARD;
    input.ki.wVk = VK_SPACE;

    SendInput(1, &input, sizeof(INPUT));

    // Key up
    input.ki.dwFlags = KEYEVENTF_KEYUP;
    SendInput(1, &input, sizeof(INPUT));
}

//Get PlayerCount helper
int GetCurrentPlayerCount()
{
    UWorld* World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return -1;

    APBGameState* GS = (APBGameState*)World->AuthorityGameMode->GameState;
    if (!GS)
        return -1;

    return GS->PlayerArray.Num();
}

nlohmann::json BuildServerStatusPayload()
{
    int playerCount = GetCurrentPlayerCount();

    std::string map = std::string(Config.MapName.begin(), Config.MapName.end());
    std::string mode = std::string(Config.FullModePath.begin(), Config.FullModePath.end());

    std::string state = "Unknown";

    // FIXED: Add proper null checks before dereferencing
    UWorld* World = UWorld::GetWorld();
    if (World && World->AuthorityGameMode && World->AuthorityGameMode->GameState)
    {
        APBGameState* GS = (APBGameState*)World->AuthorityGameMode->GameState;
        state = GS->RoundState.ToString();
    }

    nlohmann::json payload = {
        { "name",         Config.ServerName },
        { "region",       Config.ServerRegion },
        { "mode",         mode },
        { "map",          map },
        { "port",         Config.ExternalPort },
        { "playerCount",  playerCount },
        { "serverState",  state }
    };

    return payload;
}

std::string StripHttpScheme(const std::string& backend)
{
    const std::string http = "http://";
    const std::string https = "https://";

    if (backend.rfind(http, 0) == 0)
        return backend.substr(http.length());

    if (backend.rfind(https, 0) == 0)
        return backend.substr(https.length());

    return backend;
}

nlohmann::json BuildRoomHeartbeatPayload()
{
    int playerCount = GetCurrentPlayerCount();
    std::string state = "Unknown";

    UWorld* World = UWorld::GetWorld();
    if (World && World->AuthorityGameMode && World->AuthorityGameMode->GameState)
    {
        APBGameState* GS = (APBGameState*)World->AuthorityGameMode->GameState;
        state = GS->RoundState.ToString();
    }

    nlohmann::json payload = {
        { "hostToken",   HostToken },
        { "playerCount", playerCount },
        { "serverState", state }
    };

    return payload;
}

bool PostJsonToBackend(const std::string& backend, const std::string& path, const nlohmann::json& payload)
{
    std::string body = payload.dump();
    std::string cleanBackend = StripHttpScheme(backend);

    size_t slash = cleanBackend.find('/');
    if (slash != std::string::npos)
        cleanBackend = cleanBackend.substr(0, slash);

    size_t colon = cleanBackend.find(':');
    if (colon == std::string::npos)
    {
        std::cout << "[ONLINE] Invalid backend address format." << std::endl;
        return false;
    }

    std::string host = cleanBackend.substr(0, colon);
    std::string port = cleanBackend.substr(colon + 1);

    HINTERNET hSession = WinHttpOpen(L"BoundaryDLL/1.0",
        WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
        WINHTTP_NO_PROXY_NAME,
        WINHTTP_NO_PROXY_BYPASS, 0);

    if (!hSession)
        return false;

    std::wstring whost(host.begin(), host.end());
    INTERNET_PORT wport = (INTERNET_PORT)std::stoi(port);

    HINTERNET hConnect = WinHttpConnect(hSession, whost.c_str(), wport, 0);
    if (!hConnect)
    {
        WinHttpCloseHandle(hSession);
        return false;
    }

    HINTERNET hRequest = WinHttpOpenRequest(
        hConnect,
        L"POST",
        std::wstring(path.begin(), path.end()).c_str(),
        NULL,
        WINHTTP_NO_REFERER,
        WINHTTP_DEFAULT_ACCEPT_TYPES,
        0);

    if (!hRequest)
    {
        WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);
        return false;
    }

    BOOL bResults = WinHttpSendRequest(
        hRequest,
        L"Content-Type: application/json",
        -1,
        (LPVOID)body.c_str(),
        (DWORD)body.size(),
        (DWORD)body.size(),
        0);

    bool ok = false;
    if (bResults && WinHttpReceiveResponse(hRequest, NULL))
    {
        DWORD statusCode = 0;
        DWORD statusCodeSize = sizeof(statusCode);
        if (WinHttpQueryHeaders(
            hRequest,
            WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
            WINHTTP_HEADER_NAME_BY_INDEX,
            &statusCode,
            &statusCodeSize,
            WINHTTP_NO_HEADER_INDEX))
        {
            ok = statusCode >= 200 && statusCode < 300;
        }
    }

    WinHttpCloseHandle(hRequest);
    WinHttpCloseHandle(hConnect);
    WinHttpCloseHandle(hSession);

    std::cout << "[ONLINE] Sent " << path << ": " << body << " ok=" << ok << std::endl;
    return ok;
}

//Send Message to Backend HTTP Helper
void SendServerStatus(const std::string& backend)
{
    bool useRoomHeartbeat = !HostRoomId.empty() && !HostToken.empty();
    nlohmann::json payload = useRoomHeartbeat ? BuildRoomHeartbeatPayload() : BuildServerStatusPayload();
    if (!useRoomHeartbeat && !HostRoomId.empty())
    {
        payload["roomId"] = HostRoomId;
        payload["hostToken"] = HostToken;
    }

    std::string path = useRoomHeartbeat
        ? "/v1/rooms/" + HostRoomId + "/heartbeat"
        : "/server/status";

    PostJsonToBackend(backend, path, payload);
}

bool SendRoomLifecycleStart(const std::string& backend)
{
    if (HostRoomId.empty() || HostToken.empty())
        return false;

    nlohmann::json payload = {
        { "hostToken", HostToken }
    };
    return PostJsonToBackend(backend, "/v1/rooms/" + HostRoomId + "/start", payload);
}

void ReportRoomStartedIfNeeded()
{
    if (DisableBackendRoomStartPromotion)
    {
        static bool loggedSkip = false;
        if (!loggedSkip)
        {
            std::cout << "[ONLINE] Skipping /start lifecycle promotion for backend compatibility." << std::endl;
            loggedSkip = true;
        }
        return;
    }

    if (OnlineBackendAddress.empty() || HostRoomId.empty() || HostToken.empty())
        return;

    {
        std::lock_guard<std::mutex> lock(RoomStartReportMutex);
        if (RoomStartReportSucceeded || RoomStartReportInFlight)
            return;

        RoomStartReportInFlight = true;
    }

    std::string backend = OnlineBackendAddress;
    std::thread([backend]()
        {
            bool ok = SendRoomLifecycleStart(backend);
            std::lock_guard<std::mutex> lock(RoomStartReportMutex);
            RoomStartReportSucceeded = ok;
            RoomStartReportInFlight = false;
        }).detach();
}

// ======================================================
//  SECTION 5 — UNREAL ENGINE HELPERS
// ======================================================

void EnableUnrealConsole() {
    SDK::UInputSettings::GetDefaultObj()->ConsoleKeys[0].KeyName =
        SDK::UKismetStringLibrary::Conv_StringToName(L"F2");

    /* Creates a new UObject of class-type specified by Engine->ConsoleClass */
    SDK::UObject* NewObject =
        SDK::UGameplayStatics::SpawnObject(
            UEngine::GetEngine()->ConsoleClass,
            UEngine::GetEngine()->GameViewport
        );

    /* The Object we created is a subclass of UConsole, so this cast is **safe**. */
    UEngine::GetEngine()->GameViewport->ViewportConsole =
        static_cast<SDK::UConsole*>(NewObject);

    ClientLog("[DEBUG] Unreal Console => F2");
}

void ConnectToMatch() {
    if (MatchIP.empty())
    {
        ClientLog("[CLIENT] Reconnect requested but no -match target is configured.");
        return;
    }

    UPBGameInstance* GameInstance =
        (UPBGameInstance*)UWorld::GetWorld()->OwningGameInstance;

    GameInstance->ShowLoadingScreen(false, true);

    UPBLocalPlayer* LocalPlayer =
        (UPBLocalPlayer*)(UWorld::GetWorld()->OwningGameInstance->LocalPlayers[0]);

    LocalPlayer->GoToRange(0.0f);

    std::wstring travelCmd = L"travel " + std::wstring(MatchIP.begin(), MatchIP.end());
    ClientLog("[CLIENT] Reconnecting to match: " + MatchIP);

    UKismetSystemLibrary::ExecuteConsoleCommand(
        UWorld::GetWorld(), travelCmd.c_str(), nullptr
    );

    GameInstance->ShowLoadingScreen(true, true);
}


// ======================================================
//  SECTION 6 — REPLICATION SYSTEM GLOBALS
// ======================================================

std::vector<APlayerController*> playerControllersPossessed = std::vector<APlayerController*>();

int NumPlayersJoined = 0;
float PlayerJoinTimerSelectFuck = -1.0f;
bool DidProcFlow = false;
float StartMatchTimer = -1.0f;
int NumPlayersSelectedRole = 0;
bool DidProcStartMatch = false;
bool canStartMatch = false;
int NumExpectedPlayers = -1;
float MatchStartCountdown = -1.0f;
float ReplicationFlushAccumulator = 0.0f;

std::unordered_map<APBPlayerController*, bool> PlayerRespawnAllowedMap{};
std::unordered_set<APBPlayerController*> PlayersConfirmedRole{};

// LateJoinManager instance
// Constructed later in MainThread after dependencies are ready
static LateJoinManager* gLateJoinManager = nullptr;

// Helpers used by TickFlushHook and other non-LateJoin logic

APBGameState* GetPBGameState()
{
    UWorld* World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode || !World->AuthorityGameMode->GameState)
        return nullptr;

    return (APBGameState*)World->AuthorityGameMode->GameState;
}

APBGameMode* GetPBGameMode()
{
    UWorld* World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return nullptr;

    return (APBGameMode*)World->AuthorityGameMode;
}

bool IsRoundCurrentlyInProgress()
{
    APBGameState* GameState = GetPBGameState();
    return GameState && GameState->IsRoundInProgress();
}
// ======================================================
//  SECTION 7 — HOOK DETOURS (ENGINE HOOKS)
// ======================================================

SafetyHookInline TickFlush = {};

void TickFlushHook(UNetDriver* NetDriver, float DeltaTime) {
    if (listening && NetDriver && UWorld::GetWorld()) {

        if (PlayerJoinTimerSelectFuck > 0.0f) {
            PlayerJoinTimerSelectFuck -= DeltaTime;

            if (PlayerJoinTimerSelectFuck <= 0.0f) {

                for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
                {
                    SDK::UObject* Obj = SDK::UObject::GObjects->GetByIndex(i);

                    if (!Obj)
                        continue;

                    if (Obj->IsDefaultObject())
                        continue;

                    if (Obj->IsA(APBPlayerController::StaticClass()))
                    {
                        if (((APBPlayerController*)Obj)->CanSelectRole()) {
                            std::cout << "Selecting role..." << std::endl;
                            ((APBPlayerController*)Obj)->ClientSelectRole();
                        }
                        else {
                            std::cout << "CANT SELECT ROLE WEE WOO WEE WOO" << std::endl;
                        }
                    }
                }

            }
        }

        UWorld::GetWorld()->NetDriver = NetDriver;
        NetDriver->World = UWorld::GetWorld();

        LoadoutExportManager::ServerTick();

        ReplicationFlushAccumulator += DeltaTime;
        const bool shouldFlushReplication = ReplicationFlushAccumulator >= 0.05f;
        if (shouldFlushReplication)
        {
            ReplicationFlushAccumulator = 0.0f;

            std::vector<LibReplicate::FActorInfo> ActorInfos = std::vector<LibReplicate::FActorInfo>();
            std::vector<UNetConnection*> Connections = std::vector<UNetConnection*>();
            std::vector<void*> PlayerControllers = std::vector<void*>();

            for (UNetConnection* Connection : NetDriver->ClientConnections) {
                if (Connection->OwningActor) {
                    Connection->ViewTarget = Connection->PlayerController ? Connection->PlayerController->GetViewTarget() : Connection->OwningActor;
                    Connections.push_back(Connection);
                }
            }

            for (int i = 0; i < UWorld::GetWorld()->Levels.Num(); i++) {
                ULevel* Level = UWorld::GetWorld()->Levels[i];

                if (Level) {
                    for (int j = 0; j < Level->Actors.Num(); j++) {
                        AActor* actor = Level->Actors[j];

                        if (!actor)
                            continue;

                        if (actor->RemoteRole == ENetRole::ROLE_None)
                            continue;

                        if (!actor->bReplicates)
                            continue;

                        if (actor->bActorIsBeingDestroyed)
                            continue;

                        if (actor->Class == APlayerController_BP_C::StaticClass()) {
                            PlayerControllers.push_back((void*)actor);
                            if (((APlayerController*)actor)->Character && ((APlayerController*)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())) {
                                ((UCharacterMovementComponent*)(((APlayerController*)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bIgnoreClientMovementErrorChecksAndCorrection = true;
                                ((UCharacterMovementComponent*)(((APlayerController*)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bServerAcceptClientAuthoritativePosition = true;
                            }
                            continue;
                        }

                        ActorInfos.push_back(LibReplicate::FActorInfo(actor, actor->bNetTemporary));
                    }
                }
            }

            std::vector<LibReplicate::FPlayerControllerInfo> PlayerControllerInfos = std::vector<LibReplicate::FPlayerControllerInfo>();

            for (void* PlayerController : PlayerControllers) {
                for (UNetConnection* Connection : Connections) {
                    if (Connection->PlayerController == PlayerController) {
                        PlayerControllerInfos.push_back(LibReplicate::FPlayerControllerInfo(Connection, PlayerController));
                        break;
                    }
                }
            }

            std::vector<void*> CastConnections = std::vector<void*>();

            for (UNetConnection* Connection : Connections) {
                CastConnections.push_back((void*)Connection);
            }

            static FName* ActorName = nullptr;

            if (!ActorName) {
                ActorName = new FName();
                ActorName->ComparisonIndex = UKismetStringLibrary::Conv_StringToName(L"Actor").ComparisonIndex;
                ActorName->Number = UKismetStringLibrary::Conv_StringToName(L"Actor").Number;
            }

            if (ActorInfos.size() > 0 && CastConnections.size() > 0) {
                if (NetDriver) {
                    libReplicate->CallFromTickFlushHook(ActorInfos, PlayerControllerInfos, CastConnections, ActorName, NetDriver);

                    int* counter = reinterpret_cast<int*>(reinterpret_cast<char*>(NetDriver) + 0x420);
                    *counter = *counter + 1;
                }
            }
        }

        // Drive LateJoin state machine
        if (gLateJoinManager)
            gLateJoinManager->Tick(DeltaTime);
    }

    APBGameState* CurrentGameState = GetPBGameState();
    if (CurrentGameState && !CurrentGameState->IsRoundInProgress()) {
        if (CurrentGameState->RoundState.ToString().contains("InvalidState")) {

            if (NumPlayersJoined >= Config.MinPlayersToStart) {
                if (!DidProcFlow) {
                    if (MatchStartCountdown == -1.0f) {
                        MatchStartCountdown = 30.0f;

                        NumExpectedPlayers = NumPlayersJoined;
                    }
                    else {
                        MatchStartCountdown -= DeltaTime;

                        if (NumExpectedPlayers > NumPlayersJoined) {
                            NumExpectedPlayers = NumPlayersJoined;

                            MatchStartCountdown += 15.0f;
                        }

                        if (MatchStartCountdown <= 0.0f) {
                            DidProcFlow = true;

                            std::cout << "All players connected, beginning role selection flow!" << std::endl;

                            PlayerJoinTimerSelectFuck = 5.0f;

                            NumExpectedPlayers = NumPlayersJoined;
                            NumPlayersSelectedRole = 0;
                            canStartMatch = false;
                            StartMatchTimer = -1.0f;
                            PlayersConfirmedRole.clear();
                        }
                    }
                }
            }
        }

        if (CurrentGameState->RoundState.ToString().contains("CountdownToStart")) {

            for (UNetConnection* pc : NetDriver->ClientConnections) {
                if (pc->PlayerController && pc->PlayerController->Pawn)
                    pc->PlayerController->Possess(pc->PlayerController->Pawn);
            }
        }
    }

    if (canStartMatch && !DidProcStartMatch) {
        if (StartMatchTimer < 0.0f) {
            StartMatchTimer = 1.5f;
            std::cout << "[MATCH] All expected roles confirmed; delaying StartMatch briefly to drain role-selection reliable RPCs." << std::endl;
        }
        else {
            StartMatchTimer -= DeltaTime;
            if (StartMatchTimer <= 0.0f) {
                DidProcStartMatch = true;
                std::cout << "[MATCH] Starting match after role confirmations: "
                    << NumPlayersSelectedRole << "/" << NumExpectedPlayers << std::endl;

                ((APBGameMode*)UWorld::GetWorld()->AuthorityGameMode)->StartMatch();
                ReportRoomStartedIfNeeded();
            }
        }
    }

    if (GetAsyncKeyState(VK_F8) && amServer) {
        for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
        {
            SDK::UObject* Obj = SDK::UObject::GObjects->GetByIndex(i);

            if (!Obj)
                continue;

            if (Obj->IsDefaultObject())
                continue;

            if (Obj->IsA(APBPlayerController::StaticClass()))
            {
                ((APBPlayerController*)Obj)->ServerSuicide(0);
            }
        }

        while (GetAsyncKeyState(VK_F8)) {

        }
    }

    return TickFlush.call(NetDriver, DeltaTime);
}


// ======================================================
//  SECTION 8 — HOOK DETOURS (GAMEPLAY HOOKS)
// ======================================================

SafetyHookInline NotifyActorDestroyed = {};

bool NotifyActorDestroyedHook(UWorld* World, AActor* Actor, bool SomeShit, bool SomeShit2) {
    bool ret = NotifyActorDestroyed.call<bool>(World, Actor, SomeShit, SomeShit2);

    if (listening) {
        LibReplicate::FActorInfo ActorInfo = LibReplicate::FActorInfo((void*)Actor, Actor->bNetTemporary);

        libReplicate->CallWhenActorDestroyed(ActorInfo);
    }

    return ret;
}

SafetyHookInline NotifyAcceptingConnection = {};

__int64 NotifyAcceptingConnectionHook(UObject* obj) {
    return 1;
}

SafetyHookInline NotifyControlMessage = {};

char NotifyControlMessageHook(unsigned __int64 ScuffedShit, __int64 a2, uint8_t a3, __int64 a4) {
    UWorld::GetWorld()->NetDriver = (UIpNetDriver*)GetLastOfType(UIpNetDriver::StaticClass(), false);

    return NotifyControlMessage.call<char>(ScuffedShit, a2, a3, a4);
}

SafetyHookInline ProcessEvent;

void ProcessEventHook(UObject* Object, UFunction* Function, void* Parms) {
    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";

    LoadoutExportManager::MaybeOverrideInitWeaponConfig(Object, functionName, Parms);

    if (functionName.contains("QuickRespawn")) {
        APBPlayerController* PBPlayerController = (APBPlayerController*)Object;

        PlayerRespawnAllowedMap[PBPlayerController] = true;
    }

    if (functionName.contains("ServerRestartPlayer")) {
        APBPlayerController* PBPlayerController = (APBPlayerController*)Object;

        if (PlayerRespawnAllowedMap.contains(PBPlayerController) && PlayerRespawnAllowedMap[PBPlayerController] == false) {
            std::cout << "Denied restart!" << std::endl;
            return;
        }
    }

    // LateJoin: role-selection interception (CanPlayerSelectRole / CanSelectRole)
    if (gLateJoinManager && gLateJoinManager->OnProcessEvent(Object, functionName, Parms))
    {
        // Already handled by LateJoinManager
        return;
    }

    // LateJoin: ServerConfirmRoleSelection
    // Must call original ProcessEvent first, then advance LateJoin state
    if (functionName.contains("ServerConfirmRoleSelection")) {
        APBPlayerController* PBPlayerController = Object && Object->IsA(APBPlayerController::StaticClass())
            ? (APBPlayerController*)Object
            : nullptr;

        if (gLateJoinManager && gLateJoinManager->IsLateJoinPlayer(PBPlayerController)) {
            // Execute original function first
            ProcessEvent.call(Object, Function, Parms);
            LoadoutExportManager::MaybeOverrideProcessEventResult(Object, functionName, Parms);
            // Advance LateJoin state to RoleConfirmed
            gLateJoinManager->OnRoleConfirmed(PBPlayerController);
            return;
        }

        if (PBPlayerController) {
            const auto [_, inserted] = PlayersConfirmedRole.insert(PBPlayerController);
            if (inserted) {
                NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
                std::cout << "[MATCH] Role confirmed by "
                    << PBPlayerController->GetFullName()
                    << " (" << NumPlayersSelectedRole << "/" << NumExpectedPlayers << ")" << std::endl;
            }
            else {
                std::cout << "[MATCH] Ignoring duplicate role confirmation from "
                    << PBPlayerController->GetFullName() << std::endl;
            }

            if (!canStartMatch && NumExpectedPlayers > 0 && NumPlayersSelectedRole >= NumExpectedPlayers) {
                canStartMatch = true;
                StartMatchTimer = -1.0f;
            }
        }
    }

    if (functionName.contains("ReadyToMatchIntro_WaitingToStart")) {
        if (!canStartMatch) {
            return;
        }
    }

    if (functionName.contains("ServerHaveNoInput")) {
        std::cout << "[INPUT] Server flagged no input on " << Object->GetFullName() << std::endl;
    }

    if (functionName.contains("ServerHasInput")) {
        std::cout << "[INPUT] Server received input from " << Object->GetFullName() << std::endl;
    }

    if (functionName.contains("ClientBeKilled")) {
        std::cout << "Intercepted Player Kill!" << std::endl;

        APBPlayerController* PBPlayerController = (APBPlayerController*)Object;

        PlayerRespawnAllowedMap[PBPlayerController] = false;
    }

    if (functionName.contains("PlayerCanRestart")) {
        ((Params::GameModeBase_PlayerCanRestart*)Parms)->ReturnValue =
            ((AGameModeBase*)Object)->HasMatchStarted();
        return;
    }

    const bool definitionOverrideActive =
        LoadoutExportManager::BeginProcessEventWeaponDefinitionOverride(Object, functionName, Parms);

    ProcessEvent.call(Object, Function, Parms);
    LoadoutExportManager::MaybeOverrideProcessEventResult(Object, functionName, Parms);
    if (definitionOverrideActive)
    {
        LoadoutExportManager::EndProcessEventWeaponDefinitionOverride();
    }
    return;
}

SafetyHookInline PostLoginHook;

void* PostLogin(AGameMode* GameMode, APBPlayerController* PC)
{
    void* Ret = PostLoginHook.call<void*>(GameMode, PC);

    NumPlayersJoined++;

    std::cout << "Player Connected!" << std::endl;

    // LateJoin detection
    if (gLateJoinManager && gLateJoinManager->OnPostLogin(GameMode, PC))
    {
        // Handled as LateJoin player; skip normal first-life flow
        return Ret;
    }

    // Force first-life respawn fix
    if (PC && PC->Pawn)
    {
        PC->ServerSuicide(0);   // triggers respawn
    }

    return Ret;
}

SafetyHookInline OnFireWeaponHook;

void* OnFireWeapon(APBWeapon* Weapon) {
    if ((uintptr_t)_ReturnAddress() - BaseAddress != 0x1608B31) {
        return nullptr;
    }
    else {
        return OnFireWeaponHook.call<void*>(Weapon);
    }
}


// ======================================================
//  SECTION 9 — HOOK DETOURS (CLIENT HOOKS)
// ======================================================

SafetyHookInline ProcessEventClient;

void ProcessEventHookClient(UObject* Object, UFunction* Function, void* Parms) {
    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";
    const std::string objectName = Object ? std::string(Object->GetFullName()) : "";

    LoadoutExportManager::MaybeOverrideInitWeaponConfig(Object, functionName, Parms);

    if (functionName.contains("PBFieldModManager.SelectCharacter"))
    {
        auto* selectParms = static_cast<Params::PBFieldModManager_SelectCharacter*>(Parms);
        if (selectParms)
        {
            LoadoutExportManager::RememberMenuSelectedRole(selectParms->InCharacterID);
        }
    }
    else if (functionName.contains("PBFieldModWidget.OnSelectCharacter"))
    {
        auto* selectParms = static_cast<Params::PBFieldModWidget_OnSelectCharacter*>(Parms);
        if (selectParms)
        {
            LoadoutExportManager::RememberMenuSelectedRole(selectParms->InCharacter);
        }
    }
    else if (functionName.contains("PBItemFieldModWidget_Character.RefreshOnSelectCharacter"))
    {
        auto* refreshParms = static_cast<Params::PBItemFieldModWidget_Character_RefreshOnSelectCharacter*>(Parms);
        if (refreshParms)
        {
            LoadoutExportManager::RememberMenuSelectedRole(refreshParms->InCharacterID);
        }
    }

    // TEMP LOGIN DEBUG DUMP (GameInstance only)
    //if (Object && Object->IsA(UPBGameInstance::StaticClass()))
    //{
    //    std::string fn = Function->GetFullName();
    //        std::cout << "[LOGIN-DUMP] GI :: " << fn << std::endl;
    //}
    //Froce space to login
    if (functionName.contains("UMG_EnterGame_C.Construct"))
    {
        ClientLog("[LOGIN] EnterGame Construct forcing SPACE");

        std::thread([]()
            {
                Sleep(1000); // small delay so widget is fully active
                PressSpace();
            }).detach();
    }
    if (functionName.contains("UMG_EnterGame_C.BP_OnActivated"))
    {
        ClientLog("[LOGIN] EnterGame Activated forcing SPACE");

        std::thread([]()
            {
                Sleep(1000);
                PressSpace();
            }).detach();
    }
    // Detect login complete via MainMenuBase Construct
    if (functionName.contains("UMG_MainMenuBase_C.Construct"))
    {
        LoginCompleted = true;
        LoadoutExportManager::NotifyMenuConstructed();
    }
    if (functionName.contains("OnConnectMatchServerTimeOut")) {
        ClientLog("[PE] " + objectName + " - " + functionName);

        if (MatchReconnectAttempts < 2)
        {
            MatchReconnectAttempts++;
            ClientLog("[CLIENT] Match connect timeout; retry " + std::to_string(MatchReconnectAttempts) + "/2.");
            ConnectToMatch();
        }
        else
        {
            ClientLog("[CLIENT] Match connect timeout retry limit reached; keeping current screen.");
        }
    }

    const bool definitionOverrideActive =
        LoadoutExportManager::BeginProcessEventWeaponDefinitionOverride(Object, functionName, Parms);

    ProcessEventClient.call(Object, Function, Parms);
    LoadoutExportManager::MaybeOverrideProcessEventResult(Object, functionName, Parms);
    if (definitionOverrideActive)
    {
        LoadoutExportManager::EndProcessEventWeaponDefinitionOverride();
    }

    if (functionName.contains("PBPlayerController.SaveGameData"))
    {
        LoadoutExportManager::TriggerAsyncExport("player-save", 0);
    }
    else if (functionName.contains("PBFieldModManager.SavePreOrderGameSaved"))
    {
        LoadoutExportManager::TriggerAsyncExport("preorder-save", 0);
    }
    else if (functionName.contains("PBFieldModManager.SelectInventoryItem"))
    {
        LoadoutExportManager::TriggerAsyncExport("inventory-select", 0);
    }
    else if (functionName.contains("OnEquipComplete") ||
        functionName.contains("OnEquipPartCompleted") ||
        functionName.contains("OnEquipWeaponOrnamentComplete") ||
        functionName.contains("K2_OnEquipPartCompleted"))
    {
        LoadoutExportManager::TriggerAsyncExport("equip-complete", 0);
    }
    else if (functionName.contains("ExitEditWeaponPart") ||
        functionName.contains("ExitWeaponOrnamentPanel") ||
        functionName.contains("K2_ExitEditWeaponPart") ||
        functionName.contains("K2_CaptureSelectRoleWeapons"))
    {
        LoadoutExportManager::TriggerAsyncExport("customize-exit", 0);
    }

    LoadoutExportManager::WorkerTick();
}

SafetyHookInline ClientDeathCrash;

__int64 ClientDeathCrashHook(__int64 a1) {
    return 0;
}


// ======================================================
//  SECTION 10 — HOOK DETOURS (MISC HOOKS)
// ======================================================

SafetyHookInline ObjectNeedsLoad;

char ObjectNeedsLoadHook(UObject* a1) {
    return 1;
}

SafetyHookInline ActorNeedsLoad;

char ActorNeedsLoadHook(UObject* a1) {
    return 1;
}

int WINAPI MessageBoxW_Detour(HWND hWnd, LPCWSTR lpText, LPCWSTR lpCaption, UINT uType)
{
    if (lpText && wcsstr(lpText, L"Roboto"))
    {
        return IDOK;
    }
    return MessageBoxWHook.call<int>(hWnd, lpText, lpCaption, uType);
}

SafetyHookInline HudFunctionThatCrashesTheGame;

__int64 HudFunctionThatCrashesTheGameHook(__int64 a1, __int64 a2) {
    return 0;
}

SafetyHookInline GameEngineTick;

__int64 GameEngineTickHook(APlayerController* a1,
    float a2,
    __int64 a3,
    __int64 a4) {

    static bool flip = true;

    flip = !flip;

    if (flip) {
        std::cout << "NO TICKY" << std::endl;
        return 0;
    }

    return GameEngineTick.call<__int64>(a1, a2, a3, a4);
}

SafetyHookInline IsDedicatedServerHook;

bool IsDedicatedServer(void* WorldContextOrSomething) {
    return true;
}

SafetyHookInline IsServerHook;

bool IsServer(void* WorldContextOrSomething) {
    return true;
}

SafetyHookInline IsStandaloneHook;

bool IsStandalone(void* WorldContextOrSomething) {
    return false;
}
// ======================================================
//  SECTION 11 — HOOK INITIALIZATION
// ======================================================

void InitMessageBoxHook()
{
    HMODULE user32 = GetModuleHandleA("user32.dll");
    if (!user32) return;

    void* addr = GetProcAddress(user32, "MessageBoxW");
    if (!addr) return;

    MessageBoxWHook = safetyhook::create_inline(addr, MessageBoxW_Detour);
}

void InitServerHooks() {
    NotifyActorDestroyed = safetyhook::create_inline((void*)(BaseAddress + 0x33403E0), NotifyActorDestroyedHook);
    NotifyAcceptingConnection = safetyhook::create_inline((void*)(BaseAddress + 0x36CDC90), NotifyAcceptingConnectionHook);
    NotifyControlMessage = safetyhook::create_inline((void*)(BaseAddress + 0x36CDCE0), NotifyControlMessageHook);
    TickFlush = safetyhook::create_inline((void*)(BaseAddress + 0x33E05F0), TickFlushHook);
    ProcessEvent = safetyhook::create_inline((void*)(BaseAddress + 0x1BCBE40), ProcessEventHook);
    ObjectNeedsLoad = safetyhook::create_inline((void*)(BaseAddress + 0x1B7B710), ObjectNeedsLoadHook);
    ActorNeedsLoad = safetyhook::create_inline((void*)(BaseAddress + 0x3124E70), ActorNeedsLoadHook);
    OnFireWeaponHook = safetyhook::create_inline((void*)(BaseAddress + 0x1610500), OnFireWeapon);
    PostLoginHook = safetyhook::create_inline((void*)(BaseAddress + 0x32903B0), PostLogin);
    IsDedicatedServerHook = safetyhook::create_inline((void*)(BaseAddress + 0x33266F0), IsDedicatedServer);
    IsServerHook = safetyhook::create_inline((void*)(BaseAddress + 0x3326C60), IsServer);
    IsStandaloneHook = safetyhook::create_inline((void*)(BaseAddress + 0x3326CE0), IsStandalone);
}

void InitClientHook() {
    ProcessEventClient = safetyhook::create_inline((void*)(BaseAddress + 0x1BCBE40), ProcessEventHookClient);
    ClientDeathCrash = safetyhook::create_inline((void*)(BaseAddress + 0x16abe10), ClientDeathCrashHook);
}


// ======================================================
//  SECTION 12 — SERVER CONFIGURATION
// ======================================================
// Set up the dll to get values from the wrapper
std::string GetCmdValue(const std::string& key)
{
    std::string cmd = GetCommandLineA();
    size_t pos = cmd.find(key);
    if (pos == std::string::npos)
        return "";

    pos += key.length();
    size_t end = cmd.find(" ", pos);
    if (end == std::string::npos)
        end = cmd.length();

    return cmd.substr(pos, end - pos);
}

void LoadConfig()
{
    std::string cmd = GetCommandLineA();

    // PvE flag
    Config.IsPvE = cmd.find("-pve") != std::string::npos;

    // Map
    std::string mapArg = GetCmdValue("-map=");
    if (!mapArg.empty())
    {
        Config.MapName = std::wstring(mapArg.begin(), mapArg.end());
    }
    else
    {
        // fallback to something safe
        Config.MapName = L"Warehouse";
    }

    // Mode
    std::string modeArg = GetCmdValue("-mode=");
    if (!modeArg.empty())
    {
        Config.FullModePath = std::wstring(modeArg.begin(), modeArg.end());
    }
    else
    {
        // fallback based on PvE
        Config.FullModePath = Config.IsPvE
            ? L"/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Hard.BP_PBGameMode_Rush_PVE_Hard_C"
            : L"/Game/Online/GameMode/PBGameMode_Rush_BP.PBGameMode_Rush_BP_C";
    }

    // Port
    std::string portArg = GetCmdValue("-port=");
    if (!portArg.empty())
    {
        Config.Port = std::stoi(portArg);
    }
    else
    {
        Config.Port = 7777;
    }

    // External port
    std::string externalArg = GetCmdValue("-external=");
    if (!externalArg.empty())
    {
        Config.ExternalPort = std::stoi(externalArg);
    }
    else
    {
        Config.ExternalPort = Config.Port;  // default same as internal
    }

    Log("[SERVER] External port: " + std::to_string(Config.ExternalPort));


    //Name
    std::string serverNameArg = GetCmdValue("-servername=");
    if (!serverNameArg.empty())
    {
        Config.ServerName = serverNameArg;
        Log("[SERVER] Server name: " + serverNameArg);
    }
    //Region
    std::string serverRegionArg = GetCmdValue("-serverregion=");
    if (!serverRegionArg.empty())
    {
        Config.ServerRegion = serverRegionArg;
        Log("[SERVER] Server region: " + serverRegionArg);
    }
    // Min players (still used in TickFlush)
    Config.MinPlayersToStart = Config.IsPvE ? 1 : 2;

    // Online check if contact central server
    std::string onlineArg = GetCmdValue("-online=");
    if (!onlineArg.empty())
    {
        OnlineBackendAddress = onlineArg;
        std::cout << "[SERVER] Online backend: " << OnlineBackendAddress << std::endl;
    }

    std::string roomIdArg = GetCmdValue("-roomid=");
    if (!roomIdArg.empty())
    {
        HostRoomId = roomIdArg;
        Log("[SERVER] Host room id: " + HostRoomId);
    }

    std::string hostTokenArg = GetCmdValue("-hosttoken=");
    if (!hostTokenArg.empty())
    {
        HostToken = hostTokenArg;
        Log("[SERVER] Host token received.");
    }
}

void LoadClientConfig()
{
    std::string matchArg = GetCmdValue("-match=");
    if (!matchArg.empty())
    {
        MatchIP = matchArg;
        ClientLog("[CLIENT] Auto-match target: " + MatchIP);
    }

    // NEW: debug log flag
    if (std::string(GetCommandLineA()).find("-debuglog") != std::string::npos)
    {
        ClientDebugLogEnabled = true;
    }
}

// ======================================================
//  SECTION 13 — SERVER STARTUP AND COMMAND RELATED LOGIC
// ======================================================

namespace LoadoutExportManager
{
    using json = nlohmann::json;

    struct State
    {
        std::mutex Mutex;
        json Snapshot;
        bool SnapshotAvailable = false;
        bool SnapshotLoadAttempted = false;
        bool MenuApplyComplete = false;
        bool PendingLiveApply = false;
        bool MenuSeen = false;
        bool InitialMenuCaptureComplete = false;
        bool InGameThreadTick = false;
        bool ExportScheduled = false;
        int LiveApplyAttempts = 0;
        std::chrono::steady_clock::time_point MenuSeenAt{};
        std::chrono::steady_clock::time_point NextWorkerTickAt{};
        std::chrono::steady_clock::time_point ExportDueAt{};
        std::chrono::steady_clock::time_point NextLiveApplyAt{};
        std::chrono::steady_clock::time_point NextServerTickAt{};
        std::string PendingExportReason;
        APBCharacter* LastObservedCharacter = nullptr;
        std::unordered_map<APBCharacter*, int> ServerLiveApplyAttempts;
        std::unordered_set<APBCharacter*> ServerLiveApplyComplete;
        std::unordered_map<APBWeapon*, std::string> WeaponVisualRefreshSignatures;
        std::unordered_map<APBWeapon*, std::string> WeaponInitOverrideSignatures;
        std::unordered_set<std::string> WeaponQueryOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideFailureLogs;
        std::unordered_set<std::string> WeaponProcessEventProbeLogs;
        std::string LastMenuSelectedRoleId;
    };

    State& GetState()
    {
        static State StateInstance;
        return StateInstance;
    }

    std::wstring ToWide(const std::string& value)
    {
        return std::wstring(value.begin(), value.end());
    }

    std::string NameToString(const FName& value)
    {
        return value.ToString();
    }

    bool IsBlankText(const std::string& text)
    {
        return text.empty() || text == "None";
    }

    bool IsBlankName(const FName& value)
    {
        return value.ComparisonIndex <= 0;
    }

    bool CanSafelyResolveLoadedObjectName(const FName& value)
    {
        return value.ComparisonIndex > 0 && value.ComparisonIndex < 1000000 && value.Number < 1024;
    }

    std::string NormalizeRoleId(const std::string& roleId)
    {
        return IsBlankText(roleId) ? std::string() : roleId;
    }

    FName NameFromString(const std::string& value)
    {
        if (value.empty())
        {
            return FName();
        }

        const std::wstring wideValue = ToWide(value);
        return UKismetStringLibrary::Conv_StringToName(wideValue.c_str());
    }

    void RememberMenuSelectedRole(const std::string& roleId)
    {
        const std::string normalizedRoleId = NormalizeRoleId(roleId);
        if (normalizedRoleId.empty())
        {
            return;
        }

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.LastMenuSelectedRoleId = normalizedRoleId;
    }

    void RememberMenuSelectedRole(const FName& roleId)
    {
        if (IsBlankName(roleId))
        {
            return;
        }

        RememberMenuSelectedRole(NameToString(roleId));
    }

    std::string GetRememberedMenuSelectedRoleId()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.LastMenuSelectedRoleId;
    }

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outResolvedRoleId, FPBWeaponNetworkConfig& outConfig);
    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config);
    std::string ResolveCharacterRoleId(APBCharacter* character);

    std::string GetSnapshotSelectedRoleId(const json& snapshot)
    {
        if (!snapshot.is_object())
        {
            return "";
        }

        return snapshot.value("selectedRoleId", "");
    }

    bool IsRoleAllowedBySnapshotSelection(const json& snapshot, const std::string& roleId)
    {
        if (roleId.empty())
        {
            return false;
        }

        const std::string selectedRoleId = GetSnapshotSelectedRoleId(snapshot);
        return selectedRoleId.empty() || roleId == selectedRoleId;
    }

    std::filesystem::path GetAppDataRoot()
    {
        char* appData = nullptr;
        size_t appDataLength = 0;
        if (_dupenv_s(&appData, &appDataLength, "APPDATA") == 0 && appData && *appData)
        {
            const std::filesystem::path result = std::filesystem::path(appData) / "ProjectReboundBrowser";
            free(appData);
            return result;
        }

        free(appData);

        return std::filesystem::current_path() / "ProjectReboundBrowser";
    }

    std::filesystem::path GetExportSnapshotPath()
    {
        return GetAppDataRoot() / "loadout-export-v1.json";
    }

    std::filesystem::path GetLaunchSnapshotPath()
    {
        return GetAppDataRoot() / "launchers" / "loadout-launch-v1.json";
    }

    std::string BuildUtcTimestamp()
    {
        const auto now = std::chrono::system_clock::now();
        const std::time_t timeValue = std::chrono::system_clock::to_time_t(now);

        std::tm utcTime{};
        gmtime_s(&utcTime, &timeValue);

        std::ostringstream stream;
        stream << std::put_time(&utcTime, "%Y-%m-%dT%H:%M:%SZ");
        return stream.str();
    }

    UPBFieldModManager* GetFieldModManager()
    {
        UObject* object = GetLastOfType(UPBFieldModManager::StaticClass(), false);
        return object ? static_cast<UPBFieldModManager*>(object) : nullptr;
    }

    APBPlayerController* GetLocalPlayerController()
    {
        UWorld* world = UWorld::GetWorld();
        if (!world || !world->OwningGameInstance)
        {
            return nullptr;
        }

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            if (playerController && playerController->PBGameInstance == world->OwningGameInstance)
            {
                return playerController;
            }
        }

        return nullptr;
    }

    APBCharacter* GetLocalCharacter()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController && playerController->PBCharacter)
        {
            return playerController->PBCharacter;
        }

        return nullptr;
    }

    std::string GetLocalPlayerPreferredRoleId()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (!playerController)
        {
            return "";
        }

        if (playerController->PBCharacter)
        {
            const std::string roleId = ResolveCharacterRoleId(playerController->PBCharacter);
            if (!roleId.empty())
            {
                return roleId;
            }
        }

        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState)
        {
            return "";
        }

        if (!IsBlankName(playerState->SelectedCharacterID))
        {
            return NameToString(playerState->SelectedCharacterID);
        }

        if (!IsBlankName(playerState->UsageCharacterID))
        {
            return NameToString(playerState->UsageCharacterID);
        }

        if (!IsBlankName(playerState->PossessedCharacterId))
        {
            return NameToString(playerState->PossessedCharacterId);
        }

        return "";
    }

    APBCharacter* GetControllerCharacter(APBPlayerController* playerController)
    {
        if (!playerController)
        {
            return nullptr;
        }

        if (playerController->PBCharacter)
        {
            return playerController->PBCharacter;
        }

        if (playerController->Pawn && playerController->Pawn->IsA(APBCharacter::StaticClass()))
        {
            return static_cast<APBCharacter*>(playerController->Pawn);
        }

        return nullptr;
    }

    std::vector<APBCharacter*> GetServerPlayerCharacters()
    {
        std::vector<APBCharacter*> characters;
        std::unordered_set<APBCharacter*> seen;

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            APBCharacter* character = GetControllerCharacter(playerController);
            if (!character || seen.contains(character))
            {
                continue;
            }

            seen.insert(character);
            characters.push_back(character);
        }

        return characters;
    }

    bool HasArchiveLoaded()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController)
        {
            return playerController->bHasLoadedArchive;
        }

        return LoginCompleted && GetFieldModManager() != nullptr;
    }

    bool IsUnsafeMenuApplyEnabled()
    {
        static const bool enabled = []() -> bool
        {
            const std::string commandLine = GetCommandLineA();
            if (commandLine.find("-unsafeMenuLoadoutApply") != std::string::npos)
            {
                return true;
            }

            char* envValue = nullptr;
            size_t envValueLength = 0;
            const bool envEnabled =
                _dupenv_s(&envValue, &envValueLength, "PROJECTREBOUND_ENABLE_UNSAFE_MENU_APPLY") == 0 &&
                envValue &&
                std::string(envValue) == "1";
            free(envValue);
            return envEnabled;
        }();

        return enabled;
    }

    bool HasDisplayCharactersReady()
    {
        for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
        {
            APBDisplayCharacter* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            if (displayCharacter && !IsBlankName(displayCharacter->RoleConfig.CharacterID))
            {
                return true;
            }
        }

        return false;
    }

    bool IsMenuApplyReady()
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return false;
        }

        if (fieldModManager->CharacterPreOrderingInventoryConfigs.Num() <= 0)
        {
            return false;
        }

        return HasDisplayCharactersReady();
    }

    json EmptyInventoryJson()
    {
        return json{
            { "slots", json::array() }
        };
    }

    json EmptyWeaponJson()
    {
        return json{
            { "weaponId", "" },
            { "weaponClassId", "" },
            { "ornamentId", "" },
            { "parts", json::array() }
        };
    }

    json EmptyCharacterJson()
    {
        return json{
            { "skinClassArray", json::array() },
            { "skinIdArray", json::array() },
            { "skinPaintingId", "" }
        };
    }

    json EmptyLauncherJson()
    {
        return json{
            { "id", "" },
            { "skinId", "" }
        };
    }

    json EmptyMeleeJson()
    {
        return json{
            { "id", "" },
            { "skinId", "" }
        };
    }

    json EmptyMobilityJson()
    {
        return json{
            { "mobilityModuleId", "" }
        };
    }

    json EmptyRoleJson(const std::string& roleId)
    {
        return json{
            { "roleId", roleId },
            { "inventory", EmptyInventoryJson() },
            { "characterData", EmptyCharacterJson() },
            { "firstWeapon", EmptyWeaponJson() },
            { "secondWeapon", EmptyWeaponJson() },
            { "meleeWeapon", EmptyMeleeJson() },
            { "leftLauncher", EmptyLauncherJson() },
            { "rightLauncher", EmptyLauncherJson() },
            { "mobilityModule", EmptyMobilityJson() }
        };
    }

    bool ReadJsonFile(const std::filesystem::path& path, json& outJson)
    {
        try
        {
            if (!std::filesystem::exists(path))
            {
                return false;
            }

            std::ifstream file(path);
            if (!file.is_open())
            {
                return false;
            }

            outJson = json::parse(file, nullptr, false);
            return !outJson.is_discarded() && outJson.is_object();
        }
        catch (...)
        {
            return false;
        }
    }

    bool WriteJsonFile(const std::filesystem::path& path, const json& value)
    {
        try
        {
            std::filesystem::create_directories(path.parent_path());
            std::ofstream file(path, std::ios::trunc);
            if (!file.is_open())
            {
                return false;
            }

            file << value.dump(2);
            return true;
        }
        catch (...)
        {
            return false;
        }
    }

    json WeaponPartToJson(const FPBWeaponPartNetworkConfig& config, EPBPartSlotType slotType)
    {
        return json{
            { "slotType", static_cast<int>(slotType) },
            { "weaponPartId", NameToString(config.WeaponPartID) },
            { "weaponPartSkinId", NameToString(config.WeaponPartSkinID) },
            { "weaponPartSpecialSkinId", NameToString(config.WeaponPartSpecialSkinID) },
            { "weaponPartSkinPaintingId", NameToString(config.WeaponPartSkinPaintingID) }
        };
    }

    json WeaponToJson(const FPBWeaponNetworkConfig& config)
    {
        json parts = json::array();
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            parts.push_back(WeaponPartToJson(config.WeaponPartConfigs[index], config.WeaponPartSlotTypeArray[index]));
        }

        return json{
            { "weaponId", NameToString(config.WeaponID) },
            { "weaponClassId", NameToString(config.WeaponClassID) },
            { "ornamentId", NameToString(config.OrnamentID) },
            { "parts", parts }
        };
    }

    json CharacterToJson(const FPBCharacterNetworkConfig& config)
    {
        json skinClasses = json::array();
        json skinIds = json::array();

        for (int index = 0; index < config.SkinClassArray.Num(); ++index)
        {
            skinClasses.push_back(static_cast<int>(config.SkinClassArray[index]));
        }

        for (int index = 0; index < config.SkinIDArray.Num(); ++index)
        {
            skinIds.push_back(NameToString(config.SkinIDArray[index]));
        }

        return json{
            { "skinClassArray", skinClasses },
            { "skinIdArray", skinIds },
            { "skinPaintingId", NameToString(config.SkinPaintingID) }
        };
    }

    json LauncherToJson(const FPBLauncherNetworkConfig& config)
    {
        return json{
            { "id", NameToString(config.ID) },
            { "skinId", NameToString(config.SkinID) }
        };
    }

    json MeleeToJson(const FPBMeleeWeaponNetworkConfig& config)
    {
        return json{
            { "id", NameToString(config.ID) },
            { "skinId", NameToString(config.SkinID) }
        };
    }

    json MobilityToJson(const FPBMobilityModuleNetworkConfig& config)
    {
        return json{
            { "mobilityModuleId", NameToString(config.MobilityModuleID) }
        };
    }

    json InventoryToJson(const FPBInventoryNetworkConfig& config)
    {
        json slots = json::array();
        const int count = (std::min)(config.CharacterSlots.Num(), config.InventoryItems.Num());
        for (int index = 0; index < count; ++index)
        {
            slots.push_back(json{
                { "slotType", static_cast<int>(config.CharacterSlots[index]) },
                { "itemId", NameToString(config.InventoryItems[index]) }
            });
        }

        return json{
            { "slots", slots }
        };
    }

    bool InventoryFromJson(const json& value, FPBInventoryNetworkConfig& outConfig)
    {
        outConfig.CharacterSlots.Clear();
        outConfig.InventoryItems.Clear();

        const json* slots = value.is_object() && value.contains("slots") ? &value["slots"] : nullptr;
        if (!slots || !slots->is_array())
        {
            return true;
        }

        for (const auto& entry : *slots)
        {
            if (!entry.is_object())
            {
                continue;
            }

            outConfig.CharacterSlots.Add(static_cast<EPBCharacterSlotType>(entry.value("slotType", 0)));
            outConfig.InventoryItems.Add(NameFromString(entry.value("itemId", "")));
        }

        return true;
    }

    bool CharacterFromJson(const json& value, FPBCharacterNetworkConfig& outConfig)
    {
        outConfig.SkinClassArray.Clear();
        outConfig.SkinIDArray.Clear();
        outConfig.SkinPaintingID = NameFromString(value.value("skinPaintingId", ""));

        if (value.contains("skinClassArray") && value["skinClassArray"].is_array())
        {
            for (const auto& skinClass : value["skinClassArray"])
            {
                outConfig.SkinClassArray.Add(static_cast<EPBSkinClass>(skinClass.get<int>()));
            }
        }

        if (value.contains("skinIdArray") && value["skinIdArray"].is_array())
        {
            for (const auto& skinId : value["skinIdArray"])
            {
                outConfig.SkinIDArray.Add(NameFromString(skinId.get<std::string>()));
            }
        }

        return true;
    }

    bool WeaponFromJson(const json& value, FPBWeaponNetworkConfig& outConfig)
    {
        outConfig.WeaponPartSlotTypeArray.Clear();
        outConfig.WeaponPartConfigs.Clear();
        outConfig.OrnamentID = NameFromString(value.value("ornamentId", ""));
        outConfig.WeaponID = NameFromString(value.value("weaponId", ""));
        outConfig.WeaponClassID = NameFromString(value.value("weaponClassId", ""));

        if (value.contains("parts") && value["parts"].is_array())
        {
            for (const auto& entry : value["parts"])
            {
                if (!entry.is_object())
                {
                    continue;
                }

                FPBWeaponPartNetworkConfig partConfig{};
                partConfig.WeaponPartID = NameFromString(entry.value("weaponPartId", ""));
                partConfig.WeaponPartSkinID = NameFromString(entry.value("weaponPartSkinId", ""));
                partConfig.WeaponPartSpecialSkinID = NameFromString(entry.value("weaponPartSpecialSkinId", ""));
                partConfig.WeaponPartSkinPaintingID = NameFromString(entry.value("weaponPartSkinPaintingId", ""));

                outConfig.WeaponPartSlotTypeArray.Add(static_cast<EPBPartSlotType>(entry.value("slotType", 0)));
                outConfig.WeaponPartConfigs.Add(partConfig);
            }
        }

        return true;
    }

    bool LauncherFromJson(const json& value, FPBLauncherNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MeleeFromJson(const json& value, FPBMeleeWeaponNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MobilityFromJson(const json& value, FPBMobilityModuleNetworkConfig& outConfig)
    {
        outConfig.MobilityModuleID = NameFromString(value.value("mobilityModuleId", ""));
        return true;
    }

    json RoleToJson(const FPBRoleNetworkConfig& config, const FPBInventoryNetworkConfig* inventoryOverride)
    {
        return json{
            { "roleId", NameToString(config.CharacterID) },
            { "inventory", InventoryToJson(inventoryOverride ? *inventoryOverride : config.InventoryData) },
            { "characterData", CharacterToJson(config.CharacterData) },
            { "firstWeapon", WeaponToJson(config.FirstWeaponPartData) },
            { "secondWeapon", WeaponToJson(config.SecondWeaponPartData) },
            { "meleeWeapon", MeleeToJson(config.MeleeWeaponData) },
            { "leftLauncher", LauncherToJson(config.LeftLauncherData) },
            { "rightLauncher", LauncherToJson(config.RightLauncherData) },
            { "mobilityModule", MobilityToJson(config.MobilityModuleData) }
        };
    }

    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponID) || !IsBlankName(config.WeaponClassID) || !IsBlankName(config.OrnamentID) || config.WeaponPartConfigs.Num() > 0;
    }

    bool TryGetInventoryItemForSlot(const FPBInventoryNetworkConfig& inventory, EPBCharacterSlotType slotType, FName& outItemId)
    {
        const int count = (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
        for (int index = 0; index < count; ++index)
        {
            if (inventory.CharacterSlots[index] == slotType)
            {
                outItemId = inventory.InventoryItems[index];
                return !IsBlankName(outItemId);
            }
        }

        return false;
    }

    bool RoleWeaponJsonHasConfig(const json& role, const char* key)
    {
        if (!role.contains(key) || !role[key].is_object())
        {
            return false;
        }

        const json& weapon = role[key];
        const bool hasIdentity =
            !weapon.value("weaponId", "").empty() ||
            !weapon.value("weaponClassId", "").empty() ||
            !weapon.value("ornamentId", "").empty();
        const bool hasParts = weapon.contains("parts") && weapon["parts"].is_array() && !weapon["parts"].empty();
        return hasIdentity || hasParts;
    }

    bool HasWeaponPartConfigData(const FPBWeaponPartNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponPartID) ||
            !IsBlankName(config.WeaponPartSkinID) ||
            !IsBlankName(config.WeaponPartSpecialSkinID) ||
            !IsBlankName(config.WeaponPartSkinPaintingID);
    }

    bool TryGetWeaponPartConfigForSlotLocal(const FPBWeaponNetworkConfig& config, EPBPartSlotType slotType, FPBWeaponPartNetworkConfig& outConfig)
    {
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            if (config.WeaponPartSlotTypeArray[index] == slotType)
            {
                outConfig = config.WeaponPartConfigs[index];
                return true;
            }
        }

        return false;
    }

    void FillBlankWeaponPartFieldsFromFallback(FPBWeaponPartNetworkConfig& target, const FPBWeaponPartNetworkConfig& fallback)
    {
        const bool canBorrowCosmetics =
            IsBlankName(target.WeaponPartID) ||
            IsBlankName(fallback.WeaponPartID) ||
            NameToString(target.WeaponPartID) == NameToString(fallback.WeaponPartID);

        if (IsBlankName(target.WeaponPartID))
        {
            target.WeaponPartID = fallback.WeaponPartID;
        }

        if (IsBlankName(target.WeaponPartSkinID))
        {
            target.WeaponPartSkinID = fallback.WeaponPartSkinID;
        }

        if (canBorrowCosmetics && IsBlankName(target.WeaponPartSpecialSkinID))
        {
            target.WeaponPartSpecialSkinID = fallback.WeaponPartSpecialSkinID;
        }

        if (canBorrowCosmetics && IsBlankName(target.WeaponPartSkinPaintingID))
        {
            target.WeaponPartSkinPaintingID = fallback.WeaponPartSkinPaintingID;
        }
    }

    FPBWeaponNetworkConfig MergeWeaponConfigPreferPrimary(const FPBWeaponNetworkConfig& primary, const FPBWeaponNetworkConfig& fallback)
    {
        FPBWeaponNetworkConfig merged = primary;

        if (IsBlankName(merged.WeaponID))
        {
            merged.WeaponID = fallback.WeaponID;
        }

        if (IsBlankName(merged.WeaponClassID))
        {
            merged.WeaponClassID = fallback.WeaponClassID;
        }

        if (IsBlankName(merged.OrnamentID))
        {
            merged.OrnamentID = fallback.OrnamentID;
        }

        std::vector<EPBPartSlotType> slotOrder;
        auto appendSlots = [&](const FPBWeaponNetworkConfig& config)
        {
            const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
            for (int index = 0; index < count; ++index)
            {
                const EPBPartSlotType slotType = config.WeaponPartSlotTypeArray[index];
                if (std::find(slotOrder.begin(), slotOrder.end(), slotType) == slotOrder.end())
                {
                    slotOrder.push_back(slotType);
                }
            }
        };

        appendSlots(primary);
        appendSlots(fallback);

        merged.WeaponPartSlotTypeArray.Clear();
        merged.WeaponPartConfigs.Clear();

        for (const EPBPartSlotType slotType : slotOrder)
        {
            FPBWeaponPartNetworkConfig primaryPart{};
            FPBWeaponPartNetworkConfig fallbackPart{};
            const bool hasPrimaryPart = TryGetWeaponPartConfigForSlotLocal(primary, slotType, primaryPart);
            const bool hasFallbackPart = TryGetWeaponPartConfigForSlotLocal(fallback, slotType, fallbackPart);
            if (!hasPrimaryPart && !hasFallbackPart)
            {
                continue;
            }

            FPBWeaponPartNetworkConfig mergedPart = hasPrimaryPart ? primaryPart : fallbackPart;
            if (hasPrimaryPart && hasFallbackPart)
            {
                FillBlankWeaponPartFieldsFromFallback(mergedPart, fallbackPart);
            }

            if (!HasWeaponPartConfigData(mergedPart))
            {
                continue;
            }

            merged.WeaponPartSlotTypeArray.Add(slotType);
            merged.WeaponPartConfigs.Add(mergedPart);
        }

        return merged;
    }

    void SetRoleWeaponJson(json& role, const char* key, const FPBWeaponNetworkConfig& config, bool preferIncoming = true)
    {
        FPBWeaponNetworkConfig existingConfig{};
        const bool hasExistingConfig =
            role.contains(key) &&
            role[key].is_object() &&
            WeaponFromJson(role[key], existingConfig) &&
            HasWeaponConfig(existingConfig);

        FPBWeaponNetworkConfig mergedConfig{};
        if (HasWeaponConfig(config))
        {
            mergedConfig = hasExistingConfig
                ? (preferIncoming ? MergeWeaponConfigPreferPrimary(config, existingConfig) : MergeWeaponConfigPreferPrimary(existingConfig, config))
                : config;
        }
        else if (hasExistingConfig)
        {
            mergedConfig = existingConfig;
        }

        if (HasWeaponConfig(mergedConfig))
        {
            role[key] = WeaponToJson(mergedConfig);
        }
    }

    bool TryGetInventoryForRole(UPBFieldModManager* fieldModManager, const FName& roleId, FPBInventoryNetworkConfig& outInventory)
    {
        if (!fieldModManager)
        {
            return false;
        }

        for (auto& pair : fieldModManager->CharacterPreOrderingInventoryConfigs)
        {
            if (pair.Key() == roleId)
            {
                outInventory = pair.Value();
                return true;
            }
        }

        return false;
    }

    std::string ResolvePreferredSelectedRoleId(UPBFieldModManager* fieldModManager, const json& existingSnapshot, const std::unordered_map<std::string, json>& rolesById)
    {
        auto resolveCandidate = [&](const std::string& candidate) -> std::string
        {
            const std::string normalizedRoleId = NormalizeRoleId(candidate);
            if (normalizedRoleId.empty())
            {
                return "";
            }

            if (!rolesById.empty() && !rolesById.contains(normalizedRoleId))
            {
                return "";
            }

            return normalizedRoleId;
        };

        if (fieldModManager)
        {
            if (const std::string selectedRoleId = resolveCandidate(NameToString(fieldModManager->GetSelectCharacterID())); !selectedRoleId.empty())
            {
                return selectedRoleId;
            }
        }

        if (const std::string localPlayerRoleId = resolveCandidate(GetLocalPlayerPreferredRoleId()); !localPlayerRoleId.empty())
        {
            return localPlayerRoleId;
        }

        if (const std::string rememberedRoleId = resolveCandidate(GetRememberedMenuSelectedRoleId()); !rememberedRoleId.empty())
        {
            return rememberedRoleId;
        }

        if (existingSnapshot.is_object())
        {
            if (const std::string snapshotRoleId = resolveCandidate(existingSnapshot.value("selectedRoleId", "")); !snapshotRoleId.empty())
            {
                return snapshotRoleId;
            }
        }

        if (rolesById.size() == 1)
        {
            return rolesById.begin()->first;
        }

        return "";
    }

    json CaptureSnapshotFromMenu()
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return {};
        }

        json existingSnapshot;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (state.SnapshotAvailable)
            {
                existingSnapshot = state.Snapshot;
            }
        }

        std::unordered_map<std::string, json> rolesById;
        std::unordered_map<std::string, bool> rolesCapturedFromDisplay;
        if (existingSnapshot.contains("roles") && existingSnapshot["roles"].is_array())
        {
            for (const auto& role : existingSnapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                const std::string roleId = role.value("roleId", "");
                if (!roleId.empty())
                {
                    rolesById[roleId] = role;
                }
            }
        }

        for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
        {
            APBDisplayCharacter* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            if (!displayCharacter || IsBlankName(displayCharacter->RoleConfig.CharacterID))
            {
                continue;
            }

            FPBInventoryNetworkConfig inventoryOverride{};
            FPBInventoryNetworkConfig* inventoryPtr = nullptr;
            if (TryGetInventoryForRole(fieldModManager, displayCharacter->RoleConfig.CharacterID, inventoryOverride))
            {
                inventoryPtr = &inventoryOverride;
            }

            const std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
            json roleJson = RoleToJson(displayCharacter->RoleConfig, inventoryPtr);
            if (displayCharacter->DisplayFirstWeapon)
            {
                SetRoleWeaponJson(roleJson, "firstWeapon", displayCharacter->DisplayFirstWeapon->WeaponPartConfig, true);
            }
            if (displayCharacter->DisplaySecondWeapon)
            {
                SetRoleWeaponJson(roleJson, "secondWeapon", displayCharacter->DisplaySecondWeapon->WeaponPartConfig, true);
            }

            rolesById[roleId] = roleJson;
            rolesCapturedFromDisplay[roleId] = true;
        }

        for (auto& pair : fieldModManager->CharacterPreOrderingInventoryConfigs)
        {
            const std::string roleId = NameToString(pair.Key());
            if (roleId.empty())
            {
                continue;
            }

            if (!rolesById.contains(roleId))
            {
                rolesById[roleId] = EmptyRoleJson(roleId);
            }

            rolesById[roleId]["inventory"] = InventoryToJson(pair.Value());

            const bool capturedFromDisplay = rolesCapturedFromDisplay.contains(roleId) && rolesCapturedFromDisplay[roleId];
            FName firstWeaponId{};
            FName secondWeaponId{};
            if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::FirstWeapon, firstWeaponId) &&
                (!capturedFromDisplay || !RoleWeaponJsonHasConfig(rolesById[roleId], "firstWeapon")))
            {
                SetRoleWeaponJson(rolesById[roleId], "firstWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), firstWeaponId), false);
            }
            else if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::FirstWeapon, firstWeaponId))
            {
                SetRoleWeaponJson(rolesById[roleId], "firstWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), firstWeaponId), false);
            }

            if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::SecondWeapon, secondWeaponId) &&
                (!capturedFromDisplay || !RoleWeaponJsonHasConfig(rolesById[roleId], "secondWeapon")))
            {
                SetRoleWeaponJson(rolesById[roleId], "secondWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), secondWeaponId), false);
            }
            else if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::SecondWeapon, secondWeaponId))
            {
                SetRoleWeaponJson(rolesById[roleId], "secondWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), secondWeaponId), false);
            }
        }

        json roles = json::array();
        for (auto& [_, role] : rolesById)
        {
            roles.push_back(role);
        }

        if (roles.empty())
        {
            return {};
        }

        const std::string selectedRoleId = ResolvePreferredSelectedRoleId(fieldModManager, existingSnapshot, rolesById);

        return json{
            { "schemaVersion", 1 },
            { "savedAtUtc", BuildUtcTimestamp() },
            { "gameVersion", "unknown" },
            { "source", "payload" },
            { "selectedRoleId", selectedRoleId },
            { "roles", roles }
        };
    }

    bool ExportSnapshotNow(const std::string& reason)
    {
        json snapshot = CaptureSnapshotFromMenu();
        if (!snapshot.is_object())
        {
            return false;
        }

        const std::filesystem::path exportPath = GetExportSnapshotPath();
        if (!WriteJsonFile(exportPath, snapshot))
        {
            ClientLog("[LOADOUT] Failed to write local snapshot: " + exportPath.string());
            return false;
        }

        State& state = GetState();
        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = snapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
            const std::string selectedRoleId = NormalizeRoleId(snapshot.value("selectedRoleId", ""));
            if (!selectedRoleId.empty())
            {
                state.LastMenuSelectedRoleId = selectedRoleId;
            }
        }

        ClientLog("[LOADOUT] Exported local snapshot (" + reason + ") -> " + exportPath.string());
        return true;
    }

    void EnsureSnapshotLoaded()
    {
        State& state = GetState();
        {
            std::scoped_lock lock(state.Mutex);
            if (state.SnapshotLoadAttempted)
            {
                return;
            }
            state.SnapshotLoadAttempted = true;
        }

        json loadedSnapshot;
        std::string source;
        const std::filesystem::path launchPath = GetLaunchSnapshotPath();
        const std::filesystem::path exportPath = GetExportSnapshotPath();

        if (ReadJsonFile(launchPath, loadedSnapshot))
        {
            source = launchPath.string();
            try
            {
                std::filesystem::remove(launchPath);
            }
            catch (...)
            {
            }
        }
        else if (ReadJsonFile(exportPath, loadedSnapshot))
        {
            source = exportPath.string();
        }

        if (!loadedSnapshot.is_object())
        {
            return;
        }

        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = loadedSnapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
            const std::string selectedRoleId = NormalizeRoleId(loadedSnapshot.value("selectedRoleId", ""));
            if (!selectedRoleId.empty())
            {
                state.LastMenuSelectedRoleId = selectedRoleId;
            }
        }

        ClientLog("[LOADOUT] Loaded snapshot from " + source);
    }

    void PreloadSnapshot()
    {
        EnsureSnapshotLoaded();
    }

    std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> BuildInventoryListFromSnapshot(const json& snapshot)
    {
        std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> inventories;
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array())
        {
            return inventories;
        }

        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object())
            {
                continue;
            }

            const std::string roleId = role.value("roleId", "");
            if (roleId.empty())
            {
                continue;
            }

            FPBInventoryNetworkConfig inventory{};
            InventoryFromJson(role.value("inventory", EmptyInventoryJson()), inventory);
            inventories.push_back({ roleId, inventory });
        }

        return inventories;
    }

    bool ApplySnapshotToFieldModManager(const json& snapshot)
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return false;
        }

        const auto inventories = BuildInventoryListFromSnapshot(snapshot);
        if (inventories.empty())
        {
            return false;
        }

        const std::string originalRoleId = NameToString(fieldModManager->GetSelectCharacterID());
        APBPlayerController* playerController = GetLocalPlayerController();

        for (const auto& [roleId, inventory] : inventories)
        {
            const FName roleName = NameFromString(roleId);
            fieldModManager->SelectCharacter(roleName);

            const int count = (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
            for (int index = 0; index < count; ++index)
            {
                if (IsBlankName(inventory.InventoryItems[index]))
                {
                    continue;
                }

                fieldModManager->SelectCharacterSlot(inventory.CharacterSlots[index]);
                fieldModManager->SelectInventoryItem(inventory.InventoryItems[index]);
            }

            if (playerController)
            {
                playerController->ServerPreOrderInventory(roleName, inventory);
                if (playerController->PBPlayerState)
                {
                    playerController->PBPlayerState->ClientRefreshRolePreOrderingInventory(roleName, inventory);
                    playerController->PBPlayerState->ClientRefreshRoleEquippingInventory(roleName, inventory);
                }
            }
        }

        std::string targetRoleId = NormalizeRoleId(snapshot.value("selectedRoleId", ""));
        if (targetRoleId.empty())
        {
            targetRoleId = NormalizeRoleId(originalRoleId);
        }
        if (targetRoleId.empty())
        {
            targetRoleId = GetRememberedMenuSelectedRoleId();
        }
        if (!targetRoleId.empty())
        {
            fieldModManager->SelectCharacter(NameFromString(targetRoleId));
        }

        ClientLog("[LOADOUT] Applied snapshot inventory to FieldModManager. Live actors will receive remaining cosmetic fields once spawned.");
        return true;
    }

    bool HasLauncherConfig(const FPBLauncherNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMeleeConfig(const FPBMeleeWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMobilityConfig(const FPBMobilityModuleNetworkConfig& config)
    {
        return !IsBlankName(config.MobilityModuleID);
    }

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig)
    {
        json roleJson;
        if (snapshot.contains("roles") && snapshot["roles"].is_array())
        {
            for (const auto& role : snapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                if (!roleId.empty() && role.value("roleId", "") == roleId)
                {
                    roleJson = role;
                    break;
                }
            }

            if (!roleJson.is_object())
            {
                const std::string selectedRoleId = snapshot.value("selectedRoleId", "");
                for (const auto& role : snapshot["roles"])
                {
                    if (role.is_object() && role.value("roleId", "") == selectedRoleId)
                    {
                        roleJson = role;
                        break;
                    }
                }
            }

            if (!roleJson.is_object() && !snapshot["roles"].empty())
            {
                roleJson = snapshot["roles"][0];
            }
        }

        if (!roleJson.is_object())
        {
            return false;
        }

        outConfig = {};
        outConfig.CharacterID = NameFromString(roleJson.value("roleId", roleId));
        CharacterFromJson(roleJson.value("characterData", EmptyCharacterJson()), outConfig.CharacterData);
        WeaponFromJson(roleJson.value("firstWeapon", EmptyWeaponJson()), outConfig.FirstWeaponPartData);
        WeaponFromJson(roleJson.value("secondWeapon", EmptyWeaponJson()), outConfig.SecondWeaponPartData);
        MeleeFromJson(roleJson.value("meleeWeapon", EmptyMeleeJson()), outConfig.MeleeWeaponData);
        LauncherFromJson(roleJson.value("leftLauncher", EmptyLauncherJson()), outConfig.LeftLauncherData);
        LauncherFromJson(roleJson.value("rightLauncher", EmptyLauncherJson()), outConfig.RightLauncherData);
        MobilityFromJson(roleJson.value("mobilityModule", EmptyMobilityJson()), outConfig.MobilityModuleData);
        InventoryFromJson(roleJson.value("inventory", EmptyInventoryJson()), outConfig.InventoryData);
        return true;
    }

    bool WeaponPartConfigEquals(const FPBWeaponPartNetworkConfig& left, const FPBWeaponPartNetworkConfig& right)
    {
        return NameToString(left.WeaponPartID) == NameToString(right.WeaponPartID) &&
            NameToString(left.WeaponPartSkinID) == NameToString(right.WeaponPartSkinID) &&
            NameToString(left.WeaponPartSpecialSkinID) == NameToString(right.WeaponPartSpecialSkinID) &&
            NameToString(left.WeaponPartSkinPaintingID) == NameToString(right.WeaponPartSkinPaintingID);
    }

    bool WeaponConfigEquals(const FPBWeaponNetworkConfig& left, const FPBWeaponNetworkConfig& right)
    {
        if (NameToString(left.OrnamentID) != NameToString(right.OrnamentID) ||
            NameToString(left.WeaponID) != NameToString(right.WeaponID) ||
            NameToString(left.WeaponClassID) != NameToString(right.WeaponClassID) ||
            left.WeaponPartSlotTypeArray.Num() != right.WeaponPartSlotTypeArray.Num() ||
            left.WeaponPartConfigs.Num() != right.WeaponPartConfigs.Num())
        {
            return false;
        }

        const int slotCount = left.WeaponPartSlotTypeArray.Num();
        for (int index = 0; index < slotCount; ++index)
        {
            if (left.WeaponPartSlotTypeArray[index] != right.WeaponPartSlotTypeArray[index])
            {
                return false;
            }
        }

        const int partCount = left.WeaponPartConfigs.Num();
        for (int index = 0; index < partCount; ++index)
        {
            if (!WeaponPartConfigEquals(left.WeaponPartConfigs[index], right.WeaponPartConfigs[index]))
            {
                return false;
            }
        }

        return true;
    }

    bool CharacterConfigEquals(const FPBCharacterNetworkConfig& left, const FPBCharacterNetworkConfig& right)
    {
        if (NameToString(left.SkinPaintingID) != NameToString(right.SkinPaintingID) ||
            left.SkinClassArray.Num() != right.SkinClassArray.Num() ||
            left.SkinIDArray.Num() != right.SkinIDArray.Num())
        {
            return false;
        }

        for (int index = 0; index < left.SkinClassArray.Num(); ++index)
        {
            if (left.SkinClassArray[index] != right.SkinClassArray[index])
            {
                return false;
            }
        }

        for (int index = 0; index < left.SkinIDArray.Num(); ++index)
        {
            if (NameToString(left.SkinIDArray[index]) != NameToString(right.SkinIDArray[index]))
            {
                return false;
            }
        }

        return true;
    }

    bool LauncherConfigEquals(const FPBLauncherNetworkConfig& left, const FPBLauncherNetworkConfig& right)
    {
        return NameToString(left.ID) == NameToString(right.ID) &&
            NameToString(left.SkinID) == NameToString(right.SkinID);
    }

    bool MeleeConfigEquals(const FPBMeleeWeaponNetworkConfig& left, const FPBMeleeWeaponNetworkConfig& right)
    {
        return NameToString(left.ID) == NameToString(right.ID) &&
            NameToString(left.SkinID) == NameToString(right.SkinID);
    }

    bool MobilityConfigEquals(const FPBMobilityModuleNetworkConfig& left, const FPBMobilityModuleNetworkConfig& right)
    {
        return NameToString(left.MobilityModuleID) == NameToString(right.MobilityModuleID);
    }

    void MarkActorForReplication(AActor* actor)
    {
        if (!actor)
        {
            return;
        }

        actor->FlushNetDormancy();
        actor->ForceNetUpdate();
    }

    std::string ResolveCharacterRoleId(APBCharacter* character)
    {
        if (!character)
        {
            return "";
        }

        if (!IsBlankName(character->CharacterID))
        {
            return NameToString(character->CharacterID);
        }

        APBPlayerState* playerState = character->PBPlayerState;
        if (playerState)
        {
            if (!IsBlankName(playerState->PossessedCharacterId))
            {
                return NameToString(playerState->PossessedCharacterId);
            }

            if (!IsBlankName(playerState->SelectedCharacterID))
            {
                return NameToString(playerState->SelectedCharacterID);
            }

            if (!IsBlankName(playerState->UsageCharacterID))
            {
                return NameToString(playerState->UsageCharacterID);
            }
        }

        return "";
    }

    std::vector<APBWeapon*> GetCharacterWeaponList(APBCharacter* character)
    {
        std::vector<APBWeapon*> weapons;
        if (!character)
        {
            return weapons;
        }

        for (int index = 0; index < character->Inventory.Num(); ++index)
        {
            APBWeapon* weapon = character->Inventory[index];
            if (!weapon)
            {
                continue;
            }

            APBCharacter* owner = nullptr;
            try
            {
                owner = weapon->GetPawnOwner();
            }
            catch (...)
            {
                owner = nullptr;
            }

            if (!owner || owner == character)
            {
                weapons.push_back(weapon);
            }
        }

        return weapons;
    }

    std::string DescribeWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        stream << "weaponId=" << NameToString(config.WeaponID)
            << ", classId=" << NameToString(config.WeaponClassID)
            << ", ornament=" << NameToString(config.OrnamentID)
            << ", parts=" << config.WeaponPartConfigs.Num();
        return stream.str();
    }

    std::string DescribeWeaponSlotMap(APBWeapon* weapon);
    bool RefreshWeaponRuntimeVisualsOnce(APBWeapon* weapon, const FPBWeaponNetworkConfig& config);

    bool TryGetWeaponPartConfigForSlot(const FPBWeaponNetworkConfig& config, EPBPartSlotType slotType, FPBWeaponPartNetworkConfig& outConfig)
    {
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            if (config.WeaponPartSlotTypeArray[index] == slotType)
            {
                outConfig = config.WeaponPartConfigs[index];
                return true;
            }
        }

        return false;
    }

    FPBWeaponPartLoadout BuildWeaponPartLoadout(const FPBWeaponNetworkConfig& weaponConfig, const FPBWeaponPartNetworkConfig& partConfig)
    {
        FPBWeaponPartLoadout loadout{};
        loadout.ID = partConfig.WeaponPartID;
        loadout.SkinID = partConfig.WeaponPartSkinID;
        loadout.SkinPaintingID = partConfig.WeaponPartSkinPaintingID;
        loadout.SpecileSlotID = partConfig.WeaponPartSpecialSkinID;
        loadout.OrnamentID = weaponConfig.OrnamentID;
        return loadout;
    }

    bool WeaponPartLoadoutEquals(const FPBWeaponPartLoadout& loadout, const FPBWeaponNetworkConfig& weaponConfig, const FPBWeaponPartNetworkConfig& targetPart)
    {
        return NameToString(loadout.ID) == NameToString(targetPart.WeaponPartID) &&
            NameToString(loadout.SkinID) == NameToString(targetPart.WeaponPartSkinID) &&
            NameToString(loadout.SkinPaintingID) == NameToString(targetPart.WeaponPartSkinPaintingID) &&
            NameToString(loadout.SpecileSlotID) == NameToString(targetPart.WeaponPartSpecialSkinID) &&
            NameToString(loadout.OrnamentID) == NameToString(weaponConfig.OrnamentID);
    }

    std::string DescribeWeaponPartLoadout(const FPBWeaponPartLoadout& loadout)
    {
        std::ostringstream stream;
        stream << NameToString(loadout.ID)
            << "/" << NameToString(loadout.SkinID)
            << "/" << NameToString(loadout.SkinPaintingID)
            << "/" << NameToString(loadout.SpecileSlotID)
            << "/" << NameToString(loadout.OrnamentID);
        return stream.str();
    }

    std::string DescribeWeaponParts(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            const FPBWeaponPartNetworkConfig& part = config.WeaponPartConfigs[index];
            if (index > 0)
            {
                stream << " | ";
            }

            stream << static_cast<int>(config.WeaponPartSlotTypeArray[index])
                << ":" << NameToString(part.WeaponPartID)
                << "/" << NameToString(part.WeaponPartSkinID)
                << "/" << NameToString(part.WeaponPartSkinPaintingID)
                << "/" << NameToString(part.WeaponPartSpecialSkinID);
        }

        return stream.str();
    }

    std::string BuildWeaponConfigSignature(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        stream << NameToString(config.WeaponID)
            << "|" << NameToString(config.WeaponClassID)
            << "|" << NameToString(config.OrnamentID)
            << "|" << DescribeWeaponParts(config);
        return stream.str();
    }

    std::vector<std::string> BuildWeaponDefinitionCandidateIds(const FPBWeaponNetworkConfig& config)
    {
        std::vector<std::string> candidates;
        auto pushUnique = [&](const std::string& candidate)
        {
            if (IsBlankText(candidate))
            {
                return;
            }

            if (std::find(candidates.begin(), candidates.end(), candidate) == candidates.end())
            {
                candidates.push_back(candidate);
            }
        };

        pushUnique(NameToString(config.WeaponClassID));

        const std::string weaponId = NameToString(config.WeaponID);
        pushUnique(weaponId);

        const size_t prefixSplit = weaponId.find('_');
        if (prefixSplit != std::string::npos && prefixSplit + 1 < weaponId.size())
        {
            pushUnique(weaponId.substr(prefixSplit + 1));
        }

        return candidates;
    }

    template<typename TObjectType>
    TObjectType* LoadSoftObjectPtrBlocking(const TSoftObjectPtr<TObjectType>& softPtr)
    {
        if (TObjectType* loaded = softPtr.Get())
        {
            return loaded;
        }

        const TSoftObjectPtr<UObject>& genericPtr =
            reinterpret_cast<const TSoftObjectPtr<UObject>&>(softPtr);
        return static_cast<TObjectType*>(UKismetSystemLibrary::LoadAsset_Blocking(genericPtr));
    }

    std::string JoinStrings(const std::vector<std::string>& values, const std::string& delimiter)
    {
        std::ostringstream stream;
        for (size_t index = 0; index < values.size(); ++index)
        {
            if (index > 0)
            {
                stream << delimiter;
            }

            stream << values[index];
        }

        return stream.str();
    }

    UPBDataTableManager* GetDataTableManager(std::string* outSource = nullptr)
    {
        if (UObject* object = GetLastOfType(UPBDataTableManager::StaticClass(), false))
        {
            if (outSource)
            {
                *outSource = "runtime-last";
            }
            return static_cast<UPBDataTableManager*>(object);
        }

        if (UObject* object = GetLastOfType(UPBDataTableManager::StaticClass(), true))
        {
            if (outSource)
            {
                *outSource = "default-last";
            }
            return static_cast<UPBDataTableManager*>(object);
        }

        if (UPBDataTableManager* defaultObject = UPBDataTableManager::GetDefaultObj())
        {
            if (outSource)
            {
                *outSource = "class-default-object";
            }
            return defaultObject;
        }

        return nullptr;
    }

    template<typename TRow>
    TRow* FindDataTableRowPtrRecursive(UDataTable* table, const FName& rowId, std::unordered_set<UDataTable*>& visitedTables)
    {
        if (!table || IsBlankName(rowId) || !visitedTables.insert(table).second)
        {
            return nullptr;
        }

        for (auto& pair : table->RowMap)
        {
            if (pair.Key() == rowId && pair.Value())
            {
                return reinterpret_cast<TRow*>(pair.Value());
            }
        }

        if (table->IsA(UCompositeDataTable::StaticClass()))
        {
            UCompositeDataTable* composite = static_cast<UCompositeDataTable*>(table);
            for (UDataTable* parentTable : composite->ParentTables)
            {
                if (TRow* row = FindDataTableRowPtrRecursive<TRow>(parentTable, rowId, visitedTables))
                {
                    return row;
                }
            }
        }

        return nullptr;
    }

    template<typename TRow>
    TRow* FindDataTableRowPtr(UDataTable* table, const FName& rowId)
    {
        std::unordered_set<UDataTable*> visitedTables;
        return FindDataTableRowPtrRecursive<TRow>(table, rowId, visitedTables);
    }

    std::vector<std::string> ExpandWeaponDefinitionRedirectCandidates(
        UPBDataTableManager* dataTableManager,
        const std::vector<std::string>& initialCandidates)
    {
        std::vector<std::string> expandedCandidates;
        auto pushUnique = [&](const std::string& candidate)
        {
            if (IsBlankText(candidate))
            {
                return;
            }

            if (std::find(expandedCandidates.begin(), expandedCandidates.end(), candidate) == expandedCandidates.end())
            {
                expandedCandidates.push_back(candidate);
            }
        };

        for (const std::string& candidate : initialCandidates)
        {
            pushUnique(candidate);
        }

        if (!dataTableManager)
        {
            return expandedCandidates;
        }

        UDataTable* redirectTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponRedirectionDataTable);
        if (!redirectTable)
        {
            return expandedCandidates;
        }

        for (size_t index = 0; index < expandedCandidates.size(); ++index)
        {
            const std::string& candidate = expandedCandidates[index];

            if (FPBWeaponDefinitionRedirectionRow* redirectRow =
                FindDataTableRowPtr<FPBWeaponDefinitionRedirectionRow>(redirectTable, NameFromString(candidate)))
            {
                pushUnique(NameToString(redirectRow->WeaponDefinitionID));
            }

            for (auto& pair : redirectTable->RowMap)
            {
                if (!pair.Value())
                {
                    continue;
                }

                FPBWeaponDefinitionRedirectionRow* redirectRow =
                    reinterpret_cast<FPBWeaponDefinitionRedirectionRow*>(pair.Value());
                if (NameToString(redirectRow->WeaponDefinitionID) != candidate)
                {
                    continue;
                }

                pushUnique(NameToString(pair.Key()));
            }
        }

        return expandedCandidates;
    }

    FPBWeaponDefinitionRow* FindWeaponDefinitionRow(
        UPBDataTableManager* dataTableManager,
        const FPBWeaponNetworkConfig& targetConfig,
        std::string* outResolvedRowId,
        std::vector<std::string>* outCandidatesTried = nullptr,
        std::string* outFailureReason = nullptr)
    {
        if (outResolvedRowId)
        {
            outResolvedRowId->clear();
        }

        if (outCandidatesTried)
        {
            outCandidatesTried->clear();
        }

        if (outFailureReason)
        {
            outFailureReason->clear();
        }

        if (!dataTableManager)
        {
            if (outFailureReason)
            {
                *outFailureReason = "missing-data-table-manager";
            }
            return nullptr;
        }

        UDataTable* weaponDefinitionTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponDefinitionDataTable);
        if (!weaponDefinitionTable)
        {
            if (outFailureReason)
            {
                *outFailureReason = "missing-weapon-definition-table";
            }
            return nullptr;
        }

        const std::vector<std::string> candidates =
            ExpandWeaponDefinitionRedirectCandidates(dataTableManager, BuildWeaponDefinitionCandidateIds(targetConfig));
        if (outCandidatesTried)
        {
            *outCandidatesTried = candidates;
        }

        for (const std::string& candidate : candidates)
        {
            if (FPBWeaponDefinitionRow* row = FindDataTableRowPtr<FPBWeaponDefinitionRow>(weaponDefinitionTable, NameFromString(candidate)))
            {
                if (outResolvedRowId)
                {
                    *outResolvedRowId = candidate;
                }
                return row;
            }
        }

        if (outFailureReason)
        {
            *outFailureReason = "weapon-definition-row-not-found";
        }

        return nullptr;
    }

    template<typename TObjectType>
    TObjectType* FindLoadedNamedObject(const std::string& expectedName)
    {
        if (expectedName.empty())
        {
            return nullptr;
        }

        TObjectType* fuzzyMatch = nullptr;
        for (UObject* object : getObjectsOfClass(TObjectType::StaticClass(), false))
        {
            if (!object)
            {
                continue;
            }

            if (object->GetName() == expectedName)
            {
                return static_cast<TObjectType*>(object);
            }

            if (!fuzzyMatch && object->GetFullName().find(expectedName) != std::string::npos)
            {
                fuzzyMatch = static_cast<TObjectType*>(object);
            }
        }

        return fuzzyMatch;
    }

    bool UpdatePartSlotNameMapEntry(TMap<EPBPartSlotType, FName>& map, EPBPartSlotType slotType, const FName& targetValue, std::string* outPreviousValue = nullptr)
    {
        if (IsBlankName(targetValue))
        {
            return false;
        }

        for (auto& pair : map)
        {
            if (pair.Key() != slotType)
            {
                continue;
            }

            const std::string previousValue = NameToString(pair.Value());
            if (outPreviousValue)
            {
                *outPreviousValue = previousValue;
            }

            if (previousValue == NameToString(targetValue))
            {
                return false;
            }

            pair.Value() = targetValue;
            return true;
        }

        return false;
    }

    struct PartSlotNameBackup
    {
        EPBPartSlotType SlotType{};
        FName PreviousValue{};
    };

    bool UpdatePartSlotNameMapEntryWithBackup(
        TMap<EPBPartSlotType, FName>& map,
        EPBPartSlotType slotType,
        const FName& targetValue,
        std::vector<PartSlotNameBackup>* backups,
        std::string* outPreviousValue = nullptr)
    {
        if (IsBlankName(targetValue))
        {
            return false;
        }

        for (auto& pair : map)
        {
            if (pair.Key() != slotType)
            {
                continue;
            }

            const std::string previousValue = NameToString(pair.Value());
            if (outPreviousValue)
            {
                *outPreviousValue = previousValue;
            }

            if (previousValue == NameToString(targetValue))
            {
                return false;
            }

            if (backups)
            {
                backups->push_back({ slotType, pair.Value() });
            }

            pair.Value() = targetValue;
            return true;
        }

        return false;
    }

    void RestorePartSlotNameMapEntries(TMap<EPBPartSlotType, FName>& map, const std::vector<PartSlotNameBackup>& backups)
    {
        for (auto it = backups.rbegin(); it != backups.rend(); ++it)
        {
            for (auto& pair : map)
            {
                if (pair.Key() != it->SlotType)
                {
                    continue;
                }

                pair.Value() = it->PreviousValue;
                break;
            }
        }
    }

    struct WeaponDefinitionOverrideContext
    {
        UPBWeaponDefaultConfig* DefaultConfig = nullptr;
        std::vector<PartSlotNameBackup> DefaultConfigEquippedPartIDMapBackups;
        bool DefaultConfigWeaponOrnamentChanged = false;
        FName DefaultConfigWeaponOrnament{};

        FPBWeaponSuiteDefinitionRow* SuitRow = nullptr;
        std::vector<PartSlotNameBackup> SuitRowPartSlotAndIDMapBackups;
        std::vector<PartSlotNameBackup> SuitRowPartSlotAndSkinIDMapBackups;
        bool SuitRowWeaponOrnamentChanged = false;
        FName SuitRowWeaponOrnamentId{};
        bool SuitRowCrossHairChanged = false;
        FName SuitRowCrossHairId{};

        FPBWeaponSuitePaintingDefinitionRow* SuitPaintingRow = nullptr;
        std::vector<PartSlotNameBackup> SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups;

        UPBWeaponSuit* SuitAsset = nullptr;
        std::vector<PartSlotNameBackup> SuitAssetPartSlotAndIDMapBackups;
        std::vector<PartSlotNameBackup> SuitAssetPartSlotAndSkinIDMapBackups;
        bool SuitAssetWeaponOrnamentChanged = false;
        FName SuitAssetWeaponOrnamentId{};
        bool SuitAssetCrossHairChanged = false;
        FName SuitAssetCrossHairId{};

        UPBWeaponSuitPaintingCustom* SuitPaintingAsset = nullptr;
        std::vector<PartSlotNameBackup> SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups;
    };

    thread_local std::vector<WeaponDefinitionOverrideContext> ScopedWeaponDefinitionOverrides;

    bool ShouldLogWeaponDefinitionOverride(const std::string& source, const FPBWeaponNetworkConfig& config)
    {
        const std::string key = source + "|" + BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponDefinitionOverrideLogs.insert(key).second;
    }

    bool ShouldLogWeaponDefinitionOverrideFailure(
        const std::string& source,
        const FPBWeaponNetworkConfig& config,
        const std::string& reason)
    {
        const std::string key = source + "|" + BuildWeaponConfigSignature(config) + "|" + reason;
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponDefinitionOverrideFailureLogs.insert(key).second;
    }

    bool ShouldLogWeaponProcessEventProbe(const std::string& key)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponProcessEventProbeLogs.insert(key).second;
    }

    bool TryResolveSnapshotWeaponConfigForLiveWeapon(
        const json& snapshot,
        APBWeapon* weapon,
        std::string& outRoleId,
        FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();
        if (!weapon)
        {
            return false;
        }

        if (TryResolveSnapshotWeaponConfigForInit(snapshot, weapon, weapon->PartNetworkConfig, outRoleId, outConfig))
        {
            return true;
        }

        const FPBWeaponNetworkConfig nativeSaveConfig = weapon->GetPBWeaponPartSaveConfig();
        return TryResolveSnapshotWeaponConfigForInit(snapshot, weapon, nativeSaveConfig, outRoleId, outConfig);
    }

    void MaybeLogWeaponProcessEventProbe(UObject* object, const std::string& functionName, void* parms, const json* snapshot)
    {
        if (!object || functionName.empty())
        {
            return;
        }

        auto containsAny = [&](std::initializer_list<const char*> patterns) -> bool
        {
            for (const char* pattern : patterns)
            {
                if (functionName.contains(pattern))
                {
                    return true;
                }
            }

            return false;
        };

        APBWeapon* probedWeapon = nullptr;
        std::string channel;

        if (object->IsA(APBWeapon::StaticClass()) &&
            containsAny({ "InitWeapon", "OnRep_PartNetworkConfig", "K2_InitSimulatedPartsComplete", "NotifyEquipFinished", "K2_RefreshSkin" }))
        {
            probedWeapon = static_cast<APBWeapon*>(object);
            channel = "weapon";
        }
        else if (object->IsA(APBCharacter::StaticClass()) &&
            containsAny({ "AddWeapon", "EquipPendingWeapon", "OnRep_PendingWeapon" }))
        {
            if (functionName.contains("PBCharacter.AddWeapon") && parms)
            {
                probedWeapon = static_cast<Params::PBCharacter_AddWeapon*>(parms)->Weapon;
            }
            else if (functionName.contains("PBCharacter.EquipPendingWeapon") && parms)
            {
                probedWeapon = static_cast<Params::PBCharacter_EquipPendingWeapon*>(parms)->NewWeapon;
            }

            channel = "character";
        }
        else if ((object->IsA(UPBFieldModManager::StaticClass()) || object->IsA(UPBCustomizeManager::StaticClass())) &&
            containsAny({ "GetWeaponNetworkConfig", "SpawnWeapon" }))
        {
            channel = "manager";
        }
        else
        {
            return;
        }

        const std::string key = channel + "|" + functionName;
        if (!ShouldLogWeaponProcessEventProbe(key))
        {
            return;
        }

        std::ostringstream message;
        message << "[LOADOUT] Observed weapon-related ProcessEvent (" << channel << "): fn=" << functionName
            << " object=" << object->GetFullName();

        if (probedWeapon)
        {
            message << " live={" << DescribeWeaponConfig(probedWeapon->PartNetworkConfig) << "}"
                << " liveParts=[" << DescribeWeaponParts(probedWeapon->PartNetworkConfig) << "]"
                << " nativeSaveParts=[" << DescribeWeaponParts(probedWeapon->GetPBWeaponPartSaveConfig()) << "]";

            if (snapshot)
            {
                FPBWeaponNetworkConfig targetConfig{};
                std::string resolvedRoleId;
                if (TryResolveSnapshotWeaponConfigForLiveWeapon(*snapshot, probedWeapon, resolvedRoleId, targetConfig))
                {
                    message << " snapshotRole=" << (resolvedRoleId.empty() ? "<unknown>" : resolvedRoleId)
                        << " snapshotTarget={" << DescribeWeaponConfig(targetConfig) << "}"
                        << " snapshotParts=[" << DescribeWeaponParts(targetConfig) << "]";
                }
            }
        }

        ClientLog(message.str());
    }

    bool TryApplyWeaponDefinitionOverrideContext(
        const FPBWeaponNetworkConfig& targetConfig,
        WeaponDefinitionOverrideContext& outContext,
        std::string& outSummary,
        std::string& outFailureReason)
    {
        outSummary.clear();
        outFailureReason.clear();
        if (!HasWeaponConfig(targetConfig))
        {
            outFailureReason = "missing-weapon-config";
            return false;
        }

        std::string dataTableManagerSource;
        UPBDataTableManager* dataTableManager = GetDataTableManager(&dataTableManagerSource);
        if (!dataTableManager)
        {
            outFailureReason = "missing-data-table-manager";
            return false;
        }

        std::string resolvedWeaponDefinitionRowId;
        std::vector<std::string> candidatesTried;
        std::string findRowFailureReason;
        FPBWeaponDefinitionRow* weaponDefinitionRow =
            FindWeaponDefinitionRow(
                dataTableManager,
                targetConfig,
                &resolvedWeaponDefinitionRowId,
                &candidatesTried,
                &findRowFailureReason);
        if (!weaponDefinitionRow)
        {
            outFailureReason = findRowFailureReason;
            if (!candidatesTried.empty())
            {
                outFailureReason += " candidates=[" + JoinStrings(candidatesTried, " | ") + "]";
            }
            if (!dataTableManagerSource.empty())
            {
                outFailureReason += " dataTableManager=" + dataTableManagerSource;
            }
            return false;
        }

        UPBWeaponDefaultConfig* defaultConfig = LoadSoftObjectPtrBlocking(weaponDefinitionRow->DefaultConfig);
        if (!defaultConfig)
        {
            outFailureReason = "missing-weapon-default-config row=" + resolvedWeaponDefinitionRowId;
            return false;
        }

        bool mutated = false;
        std::ostringstream summary;
        summary << "weaponDef=" << resolvedWeaponDefinitionRowId;

        auto appendSection = [&](const std::string& label, const std::vector<std::string>& entries)
        {
            if (entries.empty())
            {
                return;
            }

            summary << " " << label << "=[";
            for (size_t index = 0; index < entries.size(); ++index)
            {
                if (index > 0)
                {
                    summary << " | ";
                }
                summary << entries[index];
            }
            summary << "]";
        };

        std::vector<std::string> defaultConfigChanges;
        outContext.DefaultConfig = defaultConfig;
        outContext.DefaultConfigWeaponOrnament = defaultConfig->WeaponOrnament;

        const int targetPartCount = (std::min)(targetConfig.WeaponPartSlotTypeArray.Num(), targetConfig.WeaponPartConfigs.Num());
        for (int index = 0; index < targetPartCount; ++index)
        {
            const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

            std::string previousPartId;
            if (UpdatePartSlotNameMapEntryWithBackup(
                defaultConfig->EquippedPartIDMap,
                slotType,
                targetPart.WeaponPartID,
                &outContext.DefaultConfigEquippedPartIDMapBackups,
                &previousPartId))
            {
                mutated = true;
                defaultConfigChanges.push_back(
                    std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
            }
        }

        if (!IsBlankName(targetConfig.OrnamentID) && NameToString(defaultConfig->WeaponOrnament) != NameToString(targetConfig.OrnamentID))
        {
            defaultConfigChanges.push_back("ornament:" + NameToString(defaultConfig->WeaponOrnament) + "->" + NameToString(targetConfig.OrnamentID));
            defaultConfig->WeaponOrnament = targetConfig.OrnamentID;
            outContext.DefaultConfigWeaponOrnamentChanged = true;
            mutated = true;
        }

        appendSection("defaultConfig", defaultConfigChanges);

        const FName suitSkinId = defaultConfig->SuitSkin;
        const FName suitPaintingId = defaultConfig->SuitPainting;

        FPBWeaponPartNetworkConfig sightConfig{};
        const bool hasSightConfig = TryGetWeaponPartConfigForSlot(targetConfig, EPBPartSlotType::Sight_Optical, sightConfig);

        UDataTable* weaponSuitTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponSuitDefinitionDataTable);
        if (weaponSuitTable && !IsBlankName(suitSkinId))
        {
            if (FPBWeaponSuiteDefinitionRow* suitRow = FindDataTableRowPtr<FPBWeaponSuiteDefinitionRow>(weaponSuitTable, suitSkinId))
            {
                outContext.SuitRow = suitRow;
                outContext.SuitRowWeaponOrnamentId = suitRow->WeaponOrnamentId;
                outContext.SuitRowCrossHairId = suitRow->CrossHairId;

                std::vector<std::string> suitRowChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPartId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitRow->PartSlotAndIDMap,
                        slotType,
                        targetPart.WeaponPartID,
                        &outContext.SuitRowPartSlotAndIDMapBackups,
                        &previousPartId))
                    {
                        mutated = true;
                        suitRowChanges.push_back(
                            "part" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
                    }

                    std::string previousSkinId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitRow->PartSlotAndSkinIDMap,
                        slotType,
                        targetPart.WeaponPartSkinID,
                        &outContext.SuitRowPartSlotAndSkinIDMapBackups,
                        &previousSkinId))
                    {
                        mutated = true;
                        suitRowChanges.push_back(
                            "skin" + std::to_string(static_cast<int>(slotType)) + ":" + previousSkinId + "->" + NameToString(targetPart.WeaponPartSkinID));
                    }
                }

                if (!IsBlankName(targetConfig.OrnamentID) && NameToString(suitRow->WeaponOrnamentId) != NameToString(targetConfig.OrnamentID))
                {
                    suitRowChanges.push_back("ornament:" + NameToString(suitRow->WeaponOrnamentId) + "->" + NameToString(targetConfig.OrnamentID));
                    suitRow->WeaponOrnamentId = targetConfig.OrnamentID;
                    outContext.SuitRowWeaponOrnamentChanged = true;
                    mutated = true;
                }

                if (hasSightConfig &&
                    !IsBlankName(sightConfig.WeaponPartSpecialSkinID) &&
                    NameToString(suitRow->CrossHairId) != NameToString(sightConfig.WeaponPartSpecialSkinID))
                {
                    suitRowChanges.push_back("crosshair:" + NameToString(suitRow->CrossHairId) + "->" + NameToString(sightConfig.WeaponPartSpecialSkinID));
                    suitRow->CrossHairId = sightConfig.WeaponPartSpecialSkinID;
                    outContext.SuitRowCrossHairChanged = true;
                    mutated = true;
                }

                appendSection("suitRow", suitRowChanges);
            }
        }

        UDataTable* weaponSuitPaintingTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponSuitPaintingDefinitionDataTable);
        if (weaponSuitPaintingTable && !IsBlankName(suitPaintingId))
        {
            if (FPBWeaponSuitePaintingDefinitionRow* suitPaintingRow =
                FindDataTableRowPtr<FPBWeaponSuitePaintingDefinitionRow>(weaponSuitPaintingTable, suitPaintingId))
            {
                outContext.SuitPaintingRow = suitPaintingRow;

                std::vector<std::string> suitPaintingChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPaintingId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitPaintingRow->SlotAndWeaponPartSkinPaintingIDMap,
                        slotType,
                        targetPart.WeaponPartSkinPaintingID,
                        &outContext.SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups,
                        &previousPaintingId))
                    {
                        mutated = true;
                        suitPaintingChanges.push_back(
                            std::to_string(static_cast<int>(slotType)) + ":" + previousPaintingId + "->" + NameToString(targetPart.WeaponPartSkinPaintingID));
                    }
                }

                appendSection("suitPaintRow", suitPaintingChanges);
            }
        }

        if (!IsBlankName(suitSkinId) && CanSafelyResolveLoadedObjectName(suitSkinId))
        {
            if (UPBWeaponSuit* suitAsset = FindLoadedNamedObject<UPBWeaponSuit>(NameToString(suitSkinId)))
            {
                outContext.SuitAsset = suitAsset;
                outContext.SuitAssetWeaponOrnamentId = suitAsset->WeaponOrnamentId;
                outContext.SuitAssetCrossHairId = suitAsset->CrossHairId;

                std::vector<std::string> suitAssetChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPartId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitAsset->PartSlotAndIDMap,
                        slotType,
                        targetPart.WeaponPartID,
                        &outContext.SuitAssetPartSlotAndIDMapBackups,
                        &previousPartId))
                    {
                        mutated = true;
                        suitAssetChanges.push_back(
                            "part" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
                    }

                    std::string previousSkinId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitAsset->PartSlotAndSkinIDMap,
                        slotType,
                        targetPart.WeaponPartSkinID,
                        &outContext.SuitAssetPartSlotAndSkinIDMapBackups,
                        &previousSkinId))
                    {
                        mutated = true;
                        suitAssetChanges.push_back(
                            "skin" + std::to_string(static_cast<int>(slotType)) + ":" + previousSkinId + "->" + NameToString(targetPart.WeaponPartSkinID));
                    }
                }

                if (!IsBlankName(targetConfig.OrnamentID) && NameToString(suitAsset->WeaponOrnamentId) != NameToString(targetConfig.OrnamentID))
                {
                    suitAssetChanges.push_back("ornament:" + NameToString(suitAsset->WeaponOrnamentId) + "->" + NameToString(targetConfig.OrnamentID));
                    suitAsset->WeaponOrnamentId = targetConfig.OrnamentID;
                    outContext.SuitAssetWeaponOrnamentChanged = true;
                    mutated = true;
                }

                if (hasSightConfig &&
                    !IsBlankName(sightConfig.WeaponPartSpecialSkinID) &&
                    NameToString(suitAsset->CrossHairId) != NameToString(sightConfig.WeaponPartSpecialSkinID))
                {
                    suitAssetChanges.push_back("crosshair:" + NameToString(suitAsset->CrossHairId) + "->" + NameToString(sightConfig.WeaponPartSpecialSkinID));
                    suitAsset->CrossHairId = sightConfig.WeaponPartSpecialSkinID;
                    outContext.SuitAssetCrossHairChanged = true;
                    mutated = true;
                }

                appendSection("suitAsset", suitAssetChanges);
            }
        }

        if (!IsBlankName(suitPaintingId) && CanSafelyResolveLoadedObjectName(suitPaintingId))
        {
            if (UPBWeaponSuitPaintingCustom* suitPaintingAsset =
                FindLoadedNamedObject<UPBWeaponSuitPaintingCustom>(NameToString(suitPaintingId)))
            {
                outContext.SuitPaintingAsset = suitPaintingAsset;

                std::vector<std::string> suitPaintingAssetChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPaintingId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitPaintingAsset->SlotAndWeaponPartSkinPaintingIDMap,
                        slotType,
                        targetPart.WeaponPartSkinPaintingID,
                        &outContext.SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups,
                        &previousPaintingId))
                    {
                        mutated = true;
                        suitPaintingAssetChanges.push_back(
                            std::to_string(static_cast<int>(slotType)) + ":" + previousPaintingId + "->" + NameToString(targetPart.WeaponPartSkinPaintingID));
                    }
                }

                appendSection("suitPaintAsset", suitPaintingAssetChanges);
            }
        }

        outSummary = summary.str();
        if (!mutated)
        {
            outFailureReason = "weapon-definition-override-no-mutations row=" + resolvedWeaponDefinitionRowId;
        }
        return mutated;
    }

    void RestoreWeaponDefinitionOverrideContext(const WeaponDefinitionOverrideContext& context)
    {
        if (context.DefaultConfig)
        {
            RestorePartSlotNameMapEntries(context.DefaultConfig->EquippedPartIDMap, context.DefaultConfigEquippedPartIDMapBackups);
            if (context.DefaultConfigWeaponOrnamentChanged)
            {
                context.DefaultConfig->WeaponOrnament = context.DefaultConfigWeaponOrnament;
            }
        }

        if (context.SuitRow)
        {
            RestorePartSlotNameMapEntries(context.SuitRow->PartSlotAndIDMap, context.SuitRowPartSlotAndIDMapBackups);
            RestorePartSlotNameMapEntries(context.SuitRow->PartSlotAndSkinIDMap, context.SuitRowPartSlotAndSkinIDMapBackups);
            if (context.SuitRowWeaponOrnamentChanged)
            {
                context.SuitRow->WeaponOrnamentId = context.SuitRowWeaponOrnamentId;
            }
            if (context.SuitRowCrossHairChanged)
            {
                context.SuitRow->CrossHairId = context.SuitRowCrossHairId;
            }
        }

        if (context.SuitPaintingRow)
        {
            RestorePartSlotNameMapEntries(
                context.SuitPaintingRow->SlotAndWeaponPartSkinPaintingIDMap,
                context.SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups);
        }

        if (context.SuitAsset)
        {
            RestorePartSlotNameMapEntries(context.SuitAsset->PartSlotAndIDMap, context.SuitAssetPartSlotAndIDMapBackups);
            RestorePartSlotNameMapEntries(context.SuitAsset->PartSlotAndSkinIDMap, context.SuitAssetPartSlotAndSkinIDMapBackups);
            if (context.SuitAssetWeaponOrnamentChanged)
            {
                context.SuitAsset->WeaponOrnamentId = context.SuitAssetWeaponOrnamentId;
            }
            if (context.SuitAssetCrossHairChanged)
            {
                context.SuitAsset->CrossHairId = context.SuitAssetCrossHairId;
            }
        }

        if (context.SuitPaintingAsset)
        {
            RestorePartSlotNameMapEntries(
                context.SuitPaintingAsset->SlotAndWeaponPartSkinPaintingIDMap,
                context.SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups);
        }
    }

    bool BeginScopedWeaponDefinitionOverride(const FPBWeaponNetworkConfig& targetConfig, const std::string& source)
    {
        WeaponDefinitionOverrideContext context{};
        std::string summary;
        std::string failureReason;
        if (!TryApplyWeaponDefinitionOverrideContext(targetConfig, context, summary, failureReason))
        {
            if (!failureReason.empty() && ShouldLogWeaponDefinitionOverrideFailure(source, targetConfig, failureReason))
            {
                ClientLog("[LOADOUT] Scoped weapon definition override skipped (" + source + "): " +
                    failureReason + " target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return false;
        }

        ScopedWeaponDefinitionOverrides.push_back(std::move(context));
        if (!summary.empty() && ShouldLogWeaponDefinitionOverride(source, targetConfig))
        {
            ClientLog("[LOADOUT] Scoped weapon definition override (" + source + "): " + summary);
        }
        return true;
    }

    void EndScopedWeaponDefinitionOverride()
    {
        if (ScopedWeaponDefinitionOverrides.empty())
        {
            return;
        }

        WeaponDefinitionOverrideContext context = std::move(ScopedWeaponDefinitionOverrides.back());
        ScopedWeaponDefinitionOverrides.pop_back();
        RestoreWeaponDefinitionOverrideContext(context);
    }

    bool BeginProcessEventWeaponDefinitionOverride(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object)
        {
            return false;
        }

        const bool isFieldModGetWeapon = functionName.contains("PBFieldModManager.GetWeaponNetworkConfig");
        const bool isCustomizeGetWeapon = functionName.contains("PBCustomizeManager.GetWeaponNetworkConfig");
        const bool isFieldModSpawnWeapon = functionName.contains("PBFieldModManager.SpawnWeapon");
        const bool isWeaponInit = functionName.contains("PBWeapon.InitWeapon");
        const bool isWeaponOnRep = functionName.contains("PBWeapon.OnRep_PartNetworkConfig");
        const bool isWeaponInitComplete = functionName.contains("PBWeapon.K2_InitSimulatedPartsComplete");
        const bool isWeaponNotifyEquipFinished = functionName.contains("PBWeapon.NotifyEquipFinished");
        const bool isWeaponRefreshSkin = functionName.contains("PBWeapon.K2_RefreshSkin");
        const bool isCharacterAddWeapon = functionName.contains("PBCharacter.AddWeapon");
        const bool isCharacterEquipPendingWeapon = functionName.contains("PBCharacter.EquipPendingWeapon");
        if (!isFieldModGetWeapon && !isCustomizeGetWeapon && !isFieldModSpawnWeapon && !isWeaponInit &&
            !isWeaponOnRep && !isWeaponInitComplete && !isWeaponNotifyEquipFinished && !isWeaponRefreshSkin &&
            !isCharacterAddWeapon && !isCharacterEquipPendingWeapon)
        {
            return false;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return false;
            }

            snapshotCopy = state.Snapshot;
        }

        MaybeLogWeaponProcessEventProbe(object, functionName, parms, &snapshotCopy);

        FPBWeaponNetworkConfig targetConfig{};
        std::string source;

        if (isFieldModGetWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* getWeaponParms = static_cast<Params::PBFieldModManager_GetWeaponNetworkConfig*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(getWeaponParms->InRoleID),
                NameToString(getWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-fieldmod-get";
        }
        else if (isCustomizeGetWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* getWeaponParms = static_cast<Params::PBCustomizeManager_GetWeaponNetworkConfig*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(getWeaponParms->InCharacterID),
                NameToString(getWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-customize-get";
        }
        else if (isFieldModSpawnWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* spawnWeaponParms = static_cast<Params::PBFieldModManager_SpawnWeapon*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(spawnWeaponParms->InRoleID),
                NameToString(spawnWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-fieldmod-spawn";
        }
        else if (isWeaponInit)
        {
            if (!parms)
            {
                return false;
            }

            APBWeapon* weapon = object->IsA(APBWeapon::StaticClass()) ? static_cast<APBWeapon*>(object) : nullptr;
            auto* initParms = static_cast<Params::PBWeapon_InitWeapon*>(parms);
            std::string resolvedRoleId;
            if (!weapon ||
                !TryResolveSnapshotWeaponConfigForInit(snapshotCopy, weapon, initParms->InPartSaved, resolvedRoleId, targetConfig))
            {
                targetConfig = initParms->InPartSaved;
            }

            source = "process-weapon-init";
        }
        else if (isWeaponOnRep || isWeaponInitComplete || isWeaponNotifyEquipFinished || isWeaponRefreshSkin)
        {
            APBWeapon* weapon = object->IsA(APBWeapon::StaticClass()) ? static_cast<APBWeapon*>(object) : nullptr;
            std::string resolvedRoleId;
            if (!weapon || !TryResolveSnapshotWeaponConfigForLiveWeapon(snapshotCopy, weapon, resolvedRoleId, targetConfig))
            {
                return false;
            }

            if (isWeaponOnRep)
            {
                source = "process-weapon-onrep";
            }
            else if (isWeaponInitComplete)
            {
                source = "process-weapon-init-complete";
            }
            else if (isWeaponNotifyEquipFinished)
            {
                source = "process-weapon-equip-finished";
            }
            else
            {
                source = "process-weapon-refresh-skin";
            }
        }
        else
        {
            APBWeapon* weapon = nullptr;
            if (isCharacterAddWeapon && parms)
            {
                weapon = static_cast<Params::PBCharacter_AddWeapon*>(parms)->Weapon;
                source = "process-character-add-weapon";
            }
            else if (isCharacterEquipPendingWeapon && parms)
            {
                weapon = static_cast<Params::PBCharacter_EquipPendingWeapon*>(parms)->NewWeapon;
                source = "process-character-equip-pending";
            }

            std::string resolvedRoleId;
            if (!weapon || !TryResolveSnapshotWeaponConfigForLiveWeapon(snapshotCopy, weapon, resolvedRoleId, targetConfig))
            {
                return false;
            }
        }

        return BeginScopedWeaponDefinitionOverride(targetConfig, source);
    }

    void EndProcessEventWeaponDefinitionOverride()
    {
        EndScopedWeaponDefinitionOverride();
    }

    UPBWeaponPartManager* ResolveWeaponPartManager(APBWeapon* weapon)
    {
        if (weapon)
        {
            if (UPBWeaponPartManager* partManager = weapon->GetWeaponPartMgr())
            {
                return partManager;
            }
        }

        UObject* object = GetLastOfType(UPBWeaponPartManager::StaticClass(), false);
        return object ? static_cast<UPBWeaponPartManager*>(object) : nullptr;
    }

    void RevealPartHolderMeshes(UPartDataHolderComponent* holder)
    {
        if (!holder)
        {
            return;
        }

        if (holder->PartMesh1P)
        {
            holder->PartMesh1P->SetHiddenInGame(false, false);
        }

        if (holder->PartMesh3P)
        {
            holder->PartMesh3P->SetHiddenInGame(false, false);
        }

        if (holder->DuplicatePartMesh1P)
        {
            holder->DuplicatePartMesh1P->SetHiddenInGame(false, false);
        }

        if (holder->DuplicatePartMesh3P)
        {
            holder->DuplicatePartMesh3P->SetHiddenInGame(false, false);
        }
    }

    std::vector<APBWeaponPart*> GetAttachedWeaponPartActors(APBWeapon* weapon)
    {
        std::vector<APBWeaponPart*> parts;
        if (!weapon)
        {
            return parts;
        }

        std::unordered_set<AActor*> visitedActors;
        std::unordered_set<APBWeaponPart*> seenParts;
        std::vector<AActor*> pendingActors;

        visitedActors.insert(weapon);
        pendingActors.push_back(weapon);

        auto queueActor = [&](AActor* actor)
        {
            if (actor && visitedActors.insert(actor).second)
            {
                pendingActors.push_back(actor);
            }
        };

        while (!pendingActors.empty())
        {
            AActor* current = pendingActors.back();
            pendingActors.pop_back();

            if (current != weapon && current->IsA(APBWeaponPart::StaticClass()))
            {
                APBWeaponPart* part = static_cast<APBWeaponPart*>(current);
                if (seenParts.insert(part).second)
                {
                    parts.push_back(part);
                }
            }

            TArray<AActor*> attachedActors{};
            current->GetAttachedActors(&attachedActors, true);
            for (AActor* actor : attachedActors)
            {
                queueActor(actor);
            }

            for (AActor* actor : current->Children)
            {
                queueActor(actor);
            }
        }

        return parts;
    }

    APBWeaponPart* FindAttachedWeaponPartActor(const std::vector<APBWeaponPart*>& parts, const std::string& preferredId, const std::string& fallbackId)
    {
        auto matchesId = [](APBWeaponPart* part, const std::string& id) -> bool
        {
            return part &&
                !id.empty() &&
                (NameToString(part->CurrentLoadout.ID) == id || NameToString(part->PresetLoadout.ID) == id);
        };

        for (APBWeaponPart* part : parts)
        {
            if (matchesId(part, preferredId))
            {
                return part;
            }
        }

        for (APBWeaponPart* part : parts)
        {
            if (matchesId(part, fallbackId))
            {
                return part;
            }
        }

        return nullptr;
    }

    std::string DescribeAttachedWeaponParts(APBWeapon* weapon)
    {
        std::ostringstream stream;
        bool first = true;
        for (APBWeaponPart* part : GetAttachedWeaponPartActors(weapon))
        {
            if (!first)
            {
                stream << " | ";
            }

            first = false;
            stream << "current=" << DescribeWeaponPartLoadout(part->CurrentLoadout)
                << ", preset=" << DescribeWeaponPartLoadout(part->PresetLoadout);
        }

        if (first)
        {
            return "<none>";
        }

        return stream.str();
    }

    bool ApplyNativeWeaponPartState(APBWeapon* weapon, const FPBWeaponNetworkConfig& targetConfig, const FPBWeaponNetworkConfig& nativeBaselineConfig, std::string* outMutationSummary = nullptr)
    {
        if (!weapon || !HasWeaponConfig(targetConfig))
        {
            return false;
        }

        bool mutated = false;
        std::ostringstream summary;
        bool firstSummary = true;

        auto appendSummary = [&](const std::string& entry)
        {
            if (entry.empty())
            {
                return;
            }

            if (!firstSummary)
            {
                summary << " | ";
            }

            firstSummary = false;
            summary << entry;
        };

        if (UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon))
        {
            TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
            for (auto& pair : slotMap)
            {
                const EPBPartSlotType slotType = pair.Key();
                UPartDataHolderComponent* holder = pair.Value();
                if (!holder)
                {
                    continue;
                }

                FPBWeaponPartNetworkConfig targetPart{};
                if (!TryGetWeaponPartConfigForSlot(targetConfig, slotType, targetPart) || IsBlankName(targetPart.WeaponPartID))
                {
                    continue;
                }

                const std::string previousPartId = NameToString(holder->PartID);
                bool holderMutated = false;

                if (UClass* targetPartClass = partManager->GetPartClassByID(targetPart.WeaponPartID).Get())
                {
                    if (targetPartClass->DefaultObject && targetPartClass->DefaultObject->IsA(UPartDataHolderComponent::StaticClass()))
                    {
                        UPartDataHolderComponent* defaultHolder = static_cast<UPartDataHolderComponent*>(targetPartClass->DefaultObject);
                        holder->PartDisplayInfo = defaultHolder->PartDisplayInfo;
                        holder->PartConfigFromCSV = defaultHolder->PartConfigFromCSV;
                        holder->PartID = defaultHolder->PartID;
                        holderMutated = true;
                    }
                }

                if (NameToString(holder->PartID) != NameToString(targetPart.WeaponPartID))
                {
                    holder->PartID = targetPart.WeaponPartID;
                    holderMutated = true;
                }

                if (holderMutated)
                {
                    holder->ChangeAimingMesh(weapon->IsStartADS());
                    RevealPartHolderMeshes(holder);
                    mutated = true;
                    appendSummary("slot" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(holder->PartID));
                }
            }
        }

        const std::vector<APBWeaponPart*> attachedParts = GetAttachedWeaponPartActors(weapon);
        const int targetPartCount = (std::min)(targetConfig.WeaponPartSlotTypeArray.Num(), targetConfig.WeaponPartConfigs.Num());
        for (int index = 0; index < targetPartCount; ++index)
        {
            const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];
            if (IsBlankName(targetPart.WeaponPartID))
            {
                continue;
            }

            FPBWeaponPartNetworkConfig currentPart{};
            const bool hasCurrentPart = TryGetWeaponPartConfigForSlot(nativeBaselineConfig, slotType, currentPart);
            const std::string currentPartId = hasCurrentPart ? NameToString(currentPart.WeaponPartID) : std::string();
            const std::string targetPartId = NameToString(targetPart.WeaponPartID);
            APBWeaponPart* partActor = FindAttachedWeaponPartActor(attachedParts, currentPartId, targetPartId);
            if (!partActor)
            {
                continue;
            }

            const FPBWeaponPartLoadout targetLoadout = BuildWeaponPartLoadout(targetConfig, targetPart);
            const std::string previousCurrent = DescribeWeaponPartLoadout(partActor->CurrentLoadout);
            const std::string previousPreset = DescribeWeaponPartLoadout(partActor->PresetLoadout);

            if (!WeaponPartLoadoutEquals(partActor->CurrentLoadout, targetConfig, targetPart) ||
                !WeaponPartLoadoutEquals(partActor->PresetLoadout, targetConfig, targetPart))
            {
                partActor->CurrentLoadout = targetLoadout;
                partActor->PresetLoadout = targetLoadout;
                partActor->SetActorHiddenInGame(false);
                if (partActor->MeshComponent)
                {
                    partActor->MeshComponent->SetHiddenInGame(false, false);
                }
                partActor->ForceNetUpdate();
                MarkActorForReplication(partActor);
                mutated = true;
                appendSummary("part" + std::to_string(static_cast<int>(slotType)) + ":" + previousCurrent + "/" + previousPreset +
                    "->" + DescribeWeaponPartLoadout(targetLoadout));
            }
        }

        if (outMutationSummary)
        {
            *outMutationSummary = firstSummary ? std::string() : summary.str();
        }

        return mutated;
    }

    int ScoreWeaponSnapshotMatch(const FPBWeaponNetworkConfig& candidate, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon)
    {
        if (!HasWeaponConfig(candidate))
        {
            return 0;
        }

        if (WeaponConfigEquals(candidate, incoming))
        {
            return 5;
        }

        const std::string candidateWeaponId = NameToString(candidate.WeaponID);
        const std::string candidateClassId = NameToString(candidate.WeaponClassID);
        const std::string incomingWeaponId = NameToString(incoming.WeaponID);
        const std::string incomingClassId = NameToString(incoming.WeaponClassID);

        std::string liveWeaponId;
        std::string liveClassId;
        if (weapon)
        {
            liveWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
            if (IsBlankText(liveWeaponId))
            {
                liveWeaponId = NameToString(weapon->GetWeaponId());
            }

            liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);
            if (IsBlankText(liveClassId))
            {
                liveClassId = NameToString(weapon->GetWeaponClassID());
            }
        }

        int score = 0;
        if (!IsBlankText(candidateWeaponId))
        {
            if (!IsBlankText(incomingWeaponId) && candidateWeaponId == incomingWeaponId)
            {
                score = (std::max)(score, 4);
            }

            if (!IsBlankText(liveWeaponId) && candidateWeaponId == liveWeaponId)
            {
                score = (std::max)(score, 3);
            }
        }

        if (!IsBlankText(candidateClassId))
        {
            if (!IsBlankText(incomingClassId) && candidateClassId == incomingClassId)
            {
                score = (std::max)(score, 2);
            }

            if (!IsBlankText(liveClassId) && candidateClassId == liveClassId)
            {
                score = (std::max)(score, 1);
            }
        }

        return score;
    }

    bool TryGetRoleWeaponConfigForInit(const FPBRoleNetworkConfig& roleConfig, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, FPBWeaponNetworkConfig& outConfig)
    {
        int bestScore = 0;
        bool found = false;

        auto consider = [&](const FPBWeaponNetworkConfig& candidate)
        {
            const int score = ScoreWeaponSnapshotMatch(candidate, incoming, weapon);
            if (score > bestScore)
            {
                bestScore = score;
                outConfig = candidate;
                found = true;
            }
        };

        consider(roleConfig.FirstWeaponPartData);
        consider(roleConfig.SecondWeaponPartData);
        return found;
    }

    bool TryGetRoleWeaponConfigByWeaponId(const FPBRoleNetworkConfig& roleConfig, const std::string& desiredWeaponId, FPBWeaponNetworkConfig& outConfig)
    {
        if (desiredWeaponId.empty())
        {
            return false;
        }

        auto matches = [&](const FPBWeaponNetworkConfig& candidate) -> bool
        {
            return HasWeaponConfig(candidate) &&
                (NameToString(candidate.WeaponID) == desiredWeaponId || NameToString(candidate.WeaponClassID) == desiredWeaponId);
        };

        if (matches(roleConfig.FirstWeaponPartData))
        {
            outConfig = roleConfig.FirstWeaponPartData;
            return true;
        }

        if (matches(roleConfig.SecondWeaponPartData))
        {
            outConfig = roleConfig.SecondWeaponPartData;
            return true;
        }

        return false;
    }

    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        if (roleId.empty() || weaponId.empty() || !IsRoleAllowedBySnapshotSelection(snapshot, roleId))
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        return TryGetRoleWeaponConfigByWeaponId(roleConfig, weaponId, outConfig);
    }

    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outRoleId, FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();
        const std::string selectedRoleId = GetSnapshotSelectedRoleId(snapshot);

        auto tryRole = [&](const std::string& roleId) -> bool
        {
            if (roleId.empty() || !IsRoleAllowedBySnapshotSelection(snapshot, roleId))
            {
                return false;
            }

            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
            {
                return false;
            }

            if (!TryGetRoleWeaponConfigForInit(roleConfig, incoming, weapon, outConfig))
            {
                return false;
            }

            outRoleId = roleId;
            return true;
        };

        APBCharacter* ownerCharacter = nullptr;
        if (weapon)
        {
            try
            {
                ownerCharacter = weapon->GetPawnOwner();
            }
            catch (...)
            {
                ownerCharacter = nullptr;
            }
        }

        if (!amServer)
        {
            APBCharacter* localCharacter = GetLocalCharacter();
            if (localCharacter && ownerCharacter && ownerCharacter != localCharacter)
            {
                return false;
            }
        }

        if (tryRole(ResolveCharacterRoleId(ownerCharacter)))
        {
            return true;
        }

        if (!amServer)
        {
            if (tryRole(GetLocalPlayerPreferredRoleId()))
            {
                return true;
            }
        }

        // Avoid fighting the menu preview path. Selected-role fallback is only safe once
        // we are in a spawned match context or running on the authoritative server.
        if (!amServer && !GetLocalCharacter())
        {
            return false;
        }

        if (tryRole(selectedRoleId))
        {
            return true;
        }

        if (!amServer)
        {
            return false;
        }

        if (selectedRoleId.empty() && snapshot.contains("roles") && snapshot["roles"].is_array())
        {
            for (const auto& role : snapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                if (tryRole(role.value("roleId", "")))
                {
                    return true;
                }
            }
        }

        return false;
    }

    bool ShouldLogInitWeaponOverride(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon)
        {
            return false;
        }

        const std::string signature = BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        auto existing = state.WeaponInitOverrideSignatures.find(weapon);
        if (existing != state.WeaponInitOverrideSignatures.end() && existing->second == signature)
        {
            return false;
        }

        state.WeaponInitOverrideSignatures[weapon] = signature;
        return true;
    }

    bool ShouldLogWeaponQueryOverride(const std::string& channel, const std::string& roleId, const std::string& weaponId, const FPBWeaponNetworkConfig& config)
    {
        const std::string signature = channel + "|" + roleId + "|" + weaponId + "|" + BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponQueryOverrideLogs.insert(signature).second;
    }

    void MaybeOverrideInitWeaponConfig(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object || !parms || !object->IsA(APBWeapon::StaticClass()) || !functionName.contains("PBWeapon.InitWeapon"))
        {
            return;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return;
            }

            snapshotCopy = state.Snapshot;
        }

        APBWeapon* weapon = static_cast<APBWeapon*>(object);
        auto* initParms = static_cast<Params::PBWeapon_InitWeapon*>(parms);
        const FPBWeaponNetworkConfig previousConfig = initParms->InPartSaved;

        FPBWeaponNetworkConfig targetConfig{};
        std::string resolvedRoleId;
        if (!TryResolveSnapshotWeaponConfigForInit(snapshotCopy, weapon, previousConfig, resolvedRoleId, targetConfig))
        {
            return;
        }

        if (!HasWeaponConfig(targetConfig) || WeaponConfigEquals(previousConfig, targetConfig))
        {
            return;
        }

        initParms->InPartSaved = targetConfig;
        weapon->PartNetworkConfig = targetConfig;

        if (ShouldLogInitWeaponOverride(weapon, targetConfig))
        {
            ClientLog("[LOADOUT] Overriding InitWeapon from snapshot for role " +
                (resolvedRoleId.empty() ? std::string("<unknown>") : resolvedRoleId) +
                ": incoming={" + DescribeWeaponConfig(previousConfig) +
                "} incomingParts=[" + DescribeWeaponParts(previousConfig) +
                "] target={" + DescribeWeaponConfig(targetConfig) +
                "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
        }
    }

    void MaybeOverrideProcessEventResult(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object || !parms)
        {
            return;
        }

        const bool isFieldModGetWeapon = functionName.contains("PBFieldModManager.GetWeaponNetworkConfig");
        const bool isCustomizeGetWeapon = functionName.contains("PBCustomizeManager.GetWeaponNetworkConfig");
        const bool isFieldModSpawnWeapon = functionName.contains("PBFieldModManager.SpawnWeapon");
        if (!isFieldModGetWeapon && !isCustomizeGetWeapon && !isFieldModSpawnWeapon)
        {
            return;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return;
            }

            snapshotCopy = state.Snapshot;
        }

        if (isFieldModGetWeapon)
        {
            auto* getWeaponParms = static_cast<Params::PBFieldModManager_GetWeaponNetworkConfig*>(parms);
            const std::string roleId = NameToString(getWeaponParms->InRoleID);
            const std::string weaponId = NameToString(getWeaponParms->InWeaponID);

            FPBWeaponNetworkConfig targetConfig{};
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
                !HasWeaponConfig(targetConfig) ||
                WeaponConfigEquals(getWeaponParms->ReturnValue, targetConfig))
            {
                return;
            }

            getWeaponParms->ReturnValue = targetConfig;
            if (ShouldLogWeaponQueryOverride("fieldmod-get", roleId, weaponId, targetConfig))
            {
                ClientLog("[LOADOUT] Overrode PBFieldModManager.GetWeaponNetworkConfig for role " + roleId +
                    ", weapon " + weaponId + ": target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return;
        }

        if (isCustomizeGetWeapon)
        {
            auto* getWeaponParms = static_cast<Params::PBCustomizeManager_GetWeaponNetworkConfig*>(parms);
            const std::string roleId = NameToString(getWeaponParms->InCharacterID);
            const std::string weaponId = NameToString(getWeaponParms->InWeaponID);

            FPBWeaponNetworkConfig targetConfig{};
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
                !HasWeaponConfig(targetConfig) ||
                WeaponConfigEquals(getWeaponParms->ReturnValue, targetConfig))
            {
                return;
            }

            getWeaponParms->ReturnValue = targetConfig;
            if (ShouldLogWeaponQueryOverride("customize-get", roleId, weaponId, targetConfig))
            {
                ClientLog("[LOADOUT] Overrode PBCustomizeManager.GetWeaponNetworkConfig for role " + roleId +
                    ", weapon " + weaponId + ": target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return;
        }

        auto* spawnWeaponParms = static_cast<Params::PBFieldModManager_SpawnWeapon*>(parms);
        APBWeapon* spawnedWeapon = spawnWeaponParms->ReturnValue;
        if (!spawnedWeapon)
        {
            return;
        }

        const std::string roleId = NameToString(spawnWeaponParms->InRoleID);
        const std::string weaponId = NameToString(spawnWeaponParms->InWeaponID);
        FPBWeaponNetworkConfig targetConfig{};
        if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
            !HasWeaponConfig(targetConfig))
        {
            return;
        }

        const FPBWeaponNetworkConfig previousConfig = spawnedWeapon->GetPBWeaponPartSaveConfig();
        const bool definitionOverrideActive = BeginScopedWeaponDefinitionOverride(targetConfig, "spawned-reinit");

        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->InitWeapon(targetConfig, false);
        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->OnRep_PartNetworkConfig();
        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->InitWeapon(targetConfig, true);
        spawnedWeapon->PartNetworkConfig = targetConfig;
        std::string nativeMutationSummary;
        ApplyNativeWeaponPartState(spawnedWeapon, targetConfig, previousConfig, &nativeMutationSummary);
        RefreshWeaponRuntimeVisualsOnce(spawnedWeapon, targetConfig);

        if (ShouldLogWeaponQueryOverride("fieldmod-spawn", roleId, weaponId, targetConfig))
        {
            const FPBWeaponNetworkConfig nativeSaveConfig = spawnedWeapon->GetPBWeaponPartSaveConfig();
            ClientLog("[LOADOUT] Reinitialized spawned weapon from snapshot for role " + roleId +
                ", weapon " + weaponId + ": previousParts=[" + DescribeWeaponParts(previousConfig) +
                "] targetParts=[" + DescribeWeaponParts(targetConfig) +
                "] nativeSaveParts=[" + DescribeWeaponParts(nativeSaveConfig) +
                "] slotMap=[" + DescribeWeaponSlotMap(spawnedWeapon) +
                "] attachedParts=[" + DescribeAttachedWeaponParts(spawnedWeapon) +
                "] nativeMutations=[" + nativeMutationSummary + "]");
        }

        if (definitionOverrideActive)
        {
            EndScopedWeaponDefinitionOverride();
        }
    }

    std::string DescribeLiveWeapons(APBCharacter* character)
    {
        std::ostringstream stream;
        const std::vector<APBWeapon*> weapons = GetCharacterWeaponList(character);
        stream << "liveWeapons=" << weapons.size();
        for (size_t index = 0; index < weapons.size(); ++index)
        {
            APBWeapon* weapon = weapons[index];
            stream << " [" << index << ": "
                << DescribeWeaponConfig(weapon->PartNetworkConfig)
                << ", nativeSaveParts=[" << DescribeWeaponParts(weapon->GetPBWeaponPartSaveConfig()) << "]"
                << ", slotMap=[" << DescribeWeaponSlotMap(weapon) << "]"
                << ", attachedParts=[" << DescribeAttachedWeaponParts(weapon) << "]"
                << "]";
        }
        return stream.str();
    }

    std::string DescribeWeaponSlotMap(APBWeapon* weapon)
    {
        if (!weapon)
        {
            return "<null weapon>";
        }

        UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon);
        if (!partManager)
        {
            return "<missing part manager>";
        }

        std::ostringstream stream;
        bool first = true;
        TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
        for (auto& pair : slotMap)
        {
            if (!first)
            {
                stream << " | ";
            }

            first = false;
            UPartDataHolderComponent* holder = pair.Value();
            stream << static_cast<int>(pair.Key()) << ":"
                << (holder ? NameToString(holder->GetPartID()) : "<null>");
        }

        if (first)
        {
            return "<empty>";
        }

        return stream.str();
    }

    bool DoesWeaponSlotMapMatchConfig(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon || !HasWeaponConfig(config))
        {
            return false;
        }

        UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon);
        if (!partManager)
        {
            return false;
        }

        TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        if (count <= 0)
        {
            return false;
        }

        for (int index = 0; index < count; ++index)
        {
            const EPBPartSlotType slotType = config.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = config.WeaponPartConfigs[index];
            if (IsBlankName(targetPart.WeaponPartID))
            {
                continue;
            }

            UPartDataHolderComponent* holder = nullptr;
            for (auto& pair : slotMap)
            {
                if (pair.Key() == slotType)
                {
                    holder = pair.Value();
                    break;
                }
            }

            if (!holder)
            {
                return false;
            }

            if (NameToString(holder->GetPartID()) != NameToString(targetPart.WeaponPartID))
            {
                return false;
            }
        }

        return true;
    }

    APBWeapon* FindWeaponForConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredWeaponIndex)
    {
        if (!character || !HasWeaponConfig(config))
        {
            return nullptr;
        }

        std::vector<APBWeapon*> weapons = GetCharacterWeaponList(character);
        if (weapons.empty())
        {
            return nullptr;
        }

        APBWeapon* classMatchedWeapon = nullptr;
        const std::string desiredWeaponId = NameToString(config.WeaponID);
        const std::string desiredClassId = NameToString(config.WeaponClassID);
        bool sawReliableIdentity = false;

        for (APBWeapon* weapon : weapons)
        {
            std::string currentWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
            if (IsBlankText(currentWeaponId))
            {
                currentWeaponId = NameToString(weapon->GetWeaponId());
            }

            std::string currentClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);
            if (IsBlankText(currentClassId))
            {
                currentClassId = NameToString(weapon->GetWeaponClassID());
            }

            if (!IsBlankText(currentWeaponId) || !IsBlankText(currentClassId))
            {
                sawReliableIdentity = true;
            }

            if (!IsBlankText(desiredWeaponId) && currentWeaponId == desiredWeaponId)
            {
                return weapon;
            }

            if (!classMatchedWeapon && !IsBlankText(desiredClassId) && currentClassId == desiredClassId)
            {
                classMatchedWeapon = weapon;
            }
        }

        if (classMatchedWeapon)
        {
            return classMatchedWeapon;
        }

        // During early deployment every live APBWeapon can briefly have blank identity.
        // Only then is slot-order fallback safe; once any weapon exposes IDs, a mismatch
        // should wait instead of accidentally writing AKM data onto the APS instance.
        if (!sawReliableIdentity && preferredWeaponIndex >= 0 && preferredWeaponIndex < static_cast<int>(weapons.size()))
        {
            return weapons[preferredWeaponIndex];
        }

        return nullptr;
    }

    void RefreshWeaponRuntimeVisuals(APBWeapon* weapon)
    {
        if (!weapon)
        {
            return;
        }

        weapon->ApplyPartModification();
        weapon->K2_InitSimulatedPartsComplete();
        weapon->K2_RefreshSkin();
        weapon->NotifyRecalculateSpecialPartOffset();
        weapon->CalculateAimPointToSightSocketOffset();
        weapon->RefreshMuzzleLocationAndDirection();

        MarkActorForReplication(weapon);
    }

    bool RefreshWeaponRuntimeVisualsOnce(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon)
        {
            return false;
        }

        bool shouldRefresh = false;
        {
            const std::string signature = BuildWeaponConfigSignature(config);
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            auto existing = state.WeaponVisualRefreshSignatures.find(weapon);
            if (existing == state.WeaponVisualRefreshSignatures.end() || existing->second != signature)
            {
                state.WeaponVisualRefreshSignatures[weapon] = signature;
                shouldRefresh = true;
            }
        }

        if (!shouldRefresh)
        {
            return false;
        }

        RefreshWeaponRuntimeVisuals(weapon);
        return true;
    }

    bool ApplyWeaponConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredWeaponIndex, bool& outChanged)
    {
        if (!HasWeaponConfig(config))
        {
            return true;
        }

        APBWeapon* targetWeapon = FindWeaponForConfig(character, config, preferredWeaponIndex);
        if (!targetWeapon)
        {
            return false;
        }

        const FPBWeaponNetworkConfig liveSaveConfig = targetWeapon->GetPBWeaponPartSaveConfig();
        const bool slotMapMatchesConfig = DoesWeaponSlotMapMatchConfig(targetWeapon, config);
        if (WeaponConfigEquals(targetWeapon->PartNetworkConfig, config) &&
            (WeaponConfigEquals(liveSaveConfig, config) || slotMapMatchesConfig))
        {
            RefreshWeaponRuntimeVisualsOnce(targetWeapon, config);
            return true;
        }

        const FPBWeaponNetworkConfig previousConfig = targetWeapon->PartNetworkConfig;
        const bool definitionOverrideActive = BeginScopedWeaponDefinitionOverride(config, "live-apply");

        targetWeapon->PartNetworkConfig = config;
        targetWeapon->InitWeapon(config, false);
        targetWeapon->PartNetworkConfig = config;
        targetWeapon->OnRep_PartNetworkConfig();
        targetWeapon->PartNetworkConfig = config;
        targetWeapon->InitWeapon(config, true);
        targetWeapon->PartNetworkConfig = config;
        std::string nativeMutationSummary;
        ApplyNativeWeaponPartState(targetWeapon, config, liveSaveConfig, &nativeMutationSummary);
        RefreshWeaponRuntimeVisualsOnce(targetWeapon, config);
        const FPBWeaponNetworkConfig nativeSaveConfig = targetWeapon->GetPBWeaponPartSaveConfig();
        outChanged = true;
        ClientLog("[LOADOUT] Applied weapon config: previous={" + DescribeWeaponConfig(previousConfig) +
            "} previousParts=[" + DescribeWeaponParts(previousConfig) +
            "] target={" + DescribeWeaponConfig(config) + "} targetParts=[" + DescribeWeaponParts(config) +
            "] nativeSaveParts=[" + DescribeWeaponParts(nativeSaveConfig) +
            "] slotMap=[" + DescribeWeaponSlotMap(targetWeapon) +
            "] attachedParts=[" + DescribeAttachedWeaponParts(targetWeapon) +
            "] nativeMutations=[" + nativeMutationSummary + "]");

        if (definitionOverrideActive)
        {
            EndScopedWeaponDefinitionOverride();
        }
        return true;
    }

    bool ApplyLauncherConfig(APBLauncher* launcher, const FPBLauncherNetworkConfig& config, bool& outChanged)
    {
        if (!HasLauncherConfig(config))
        {
            return true;
        }

        if (!launcher)
        {
            return false;
        }

        if (LauncherConfigEquals(launcher->SavedData, config))
        {
            return true;
        }

        launcher->SavedData = config;
        launcher->OnRep_SavedData();
        MarkActorForReplication(launcher);
        outChanged = true;
        return true;
    }

    bool ApplyMeleeConfig(APBMeleeWeapon* meleeWeapon, const FPBMeleeWeaponNetworkConfig& config, bool& outChanged)
    {
        if (!HasMeleeConfig(config))
        {
            return true;
        }

        if (!meleeWeapon)
        {
            return false;
        }

        if (MeleeConfigEquals(meleeWeapon->MeleeNetworkConfig, config))
        {
            return true;
        }

        meleeWeapon->MeleeNetworkConfig = config;
        meleeWeapon->OnRep_MeleeNetworkConfig();
        MarkActorForReplication(meleeWeapon);
        outChanged = true;
        return true;
    }

    bool ApplyMobilityConfig(APBCharacter* character, const FPBMobilityModuleNetworkConfig& config, bool& outChanged)
    {
        if (!HasMobilityConfig(config))
        {
            return true;
        }

        if (!character || !character->CurrentMobilityModule)
        {
            return false;
        }

        if (MobilityConfigEquals(character->CurrentMobilityModule->SavedData, config))
        {
            return true;
        }

        character->CurrentMobilityModule->SavedData = config;
        character->OnRep_CurrentMobilityModule();
        MarkActorForReplication(character->CurrentMobilityModule);
        MarkActorForReplication(character);
        outChanged = true;
        return true;
    }

    std::string JoinMissingTargetNames(const std::vector<std::string>& missing)
    {
        if (missing.empty())
        {
            return "";
        }

        std::ostringstream stream;
        for (size_t index = 0; index < missing.size(); ++index)
        {
            if (index > 0)
            {
                stream << ",";
            }
            stream << missing[index];
        }

        return stream.str();
    }

    std::string GetMissingRequiredLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        std::vector<std::string> missing;

        if (!character)
        {
            return "character";
        }

        if (HasWeaponConfig(roleConfig.FirstWeaponPartData) && !FindWeaponForConfig(character, roleConfig.FirstWeaponPartData, 0))
        {
            missing.push_back("firstWeapon");
        }

        if (HasWeaponConfig(roleConfig.SecondWeaponPartData) && !FindWeaponForConfig(character, roleConfig.SecondWeaponPartData, 1))
        {
            missing.push_back("secondWeapon");
        }

        if (HasMeleeConfig(roleConfig.MeleeWeaponData) && !character->CurrentMeleeWeapon)
        {
            missing.push_back("meleeWeapon");
        }

        return JoinMissingTargetNames(missing);
    }

    std::string GetMissingOptionalLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        std::vector<std::string> missing;

        if (!character)
        {
            return "character";
        }

        if (HasLauncherConfig(roleConfig.LeftLauncherData) && !character->CurrentLeftLauncher)
        {
            missing.push_back("leftLauncher");
        }

        if (HasLauncherConfig(roleConfig.RightLauncherData) && !character->CurrentRightLauncher)
        {
            missing.push_back("rightLauncher");
        }

        if (HasMobilityConfig(roleConfig.MobilityModuleData) && !character->CurrentMobilityModule)
        {
            missing.push_back("mobilityModule");
        }

        return JoinMissingTargetNames(missing);
    }

    std::string GetMissingLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        const std::string requiredMissing = GetMissingRequiredLiveTargets(character, roleConfig);
        const std::string optionalMissing = GetMissingOptionalLiveTargets(character, roleConfig);
        if (requiredMissing.empty())
        {
            return optionalMissing;
        }

        if (optionalMissing.empty())
        {
            return requiredMissing;
        }

        return requiredMissing + "," + optionalMissing;
    }

    bool IsLiveCharacterReadyForConfig(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        if (!character)
        {
            return false;
        }

        const bool wantsFirstWeapon = HasWeaponConfig(roleConfig.FirstWeaponPartData);
        const bool wantsSecondWeapon = HasWeaponConfig(roleConfig.SecondWeaponPartData);
        if (!wantsFirstWeapon && !wantsSecondWeapon)
        {
            return true;
        }

        if (wantsFirstWeapon && FindWeaponForConfig(character, roleConfig.FirstWeaponPartData, 0))
        {
            return true;
        }

        if (wantsSecondWeapon && FindWeaponForConfig(character, roleConfig.SecondWeaponPartData, 1))
        {
            return true;
        }

        return false;
    }

    std::string BuildLiveApplyDebugSummary(APBCharacter* character, const json& snapshot)
    {
        const std::string roleId = ResolveCharacterRoleId(character);
        std::ostringstream stream;
        stream << "character=" << (character ? NameToString(character->CharacterID) : "<null>")
            << ", resolvedRole=" << roleId;

        FPBRoleNetworkConfig roleConfig{};
        if (character && TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            stream << ", role=" << NameToString(roleConfig.CharacterID)
                << ", missing=" << GetMissingLiveTargets(character, roleConfig)
                << ", first={" << DescribeWeaponConfig(roleConfig.FirstWeaponPartData) << "}"
                << ", second={" << DescribeWeaponConfig(roleConfig.SecondWeaponPartData) << "}"
                << ", " << DescribeLiveWeapons(character);
        }
        else
        {
            stream << ", role=<unresolved>";
        }

        return stream.str();
    }

    bool ApplySnapshotToLiveCharacter(APBCharacter* character, const json& snapshot)
    {
        if (!character)
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        const std::string roleId = ResolveCharacterRoleId(character);
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        if (!IsLiveCharacterReadyForConfig(character, roleConfig))
        {
            ClientLog("[LOADOUT] Live snapshot waiting for weapon target. " + BuildLiveApplyDebugSummary(character, snapshot));
            return false;
        }

        bool changed = false;
        bool ready = true;

        if (!CharacterConfigEquals(character->CharacterSkinConfig, roleConfig.CharacterData))
        {
            character->CharacterSkinConfig = roleConfig.CharacterData;
            character->OnRep_CharacterSkinConfig();
            if (auto* skinManager = const_cast<UPBSkinManager*>(UPBSkinManager::GetPBSkinManager()))
            {
                skinManager->RefreshCharacterSkin(character, roleConfig.CharacterData);
            }
            MarkActorForReplication(character);
            changed = true;
        }

        ready = ApplyWeaponConfig(character, roleConfig.FirstWeaponPartData, 0, changed) && ready;
        ready = ApplyWeaponConfig(character, roleConfig.SecondWeaponPartData, 1, changed) && ready;
        ready = ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed) && ready;

        const bool leftLauncherReady = ApplyLauncherConfig(character->CurrentLeftLauncher, roleConfig.LeftLauncherData, changed);
        const bool rightLauncherReady = ApplyLauncherConfig(character->CurrentRightLauncher, roleConfig.RightLauncherData, changed);
        const bool mobilityReady = ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed);
        const std::string optionalMissing = GetMissingOptionalLiveTargets(character, roleConfig);

        if (ready)
        {
            if (!optionalMissing.empty() && (!leftLauncherReady || !rightLauncherReady || !mobilityReady))
            {
                ClientLog("[LOADOUT] Applied live snapshot core fields to role " + NameToString(roleConfig.CharacterID) +
                    "; optional targets pending " + optionalMissing);
            }
            else
            {
                ClientLog("[LOADOUT] Applied live snapshot to role " + NameToString(roleConfig.CharacterID));
            }
        }
        else if (changed)
        {
            ClientLog("[LOADOUT] Applied partial live snapshot to role " + NameToString(roleConfig.CharacterID) +
                "; waiting for " + GetMissingRequiredLiveTargets(character, roleConfig));
        }

        return ready;
    }

    bool IsRuntimeRoundReadyForLiveApply(APBCharacter* character)
    {
        if (!character || character->Inventory.Num() <= 0)
        {
            return false;
        }

        for (UObject* object : getObjectsOfClass(APBGameState::StaticClass(), false))
        {
            APBGameState* gameState = static_cast<APBGameState*>(object);
            if (!gameState)
            {
                continue;
            }

            const std::string roundState = NameToString(gameState->RoundState);
            if (roundState.empty() ||
                roundState == "None" ||
                roundState.contains("InvalidState") ||
                roundState.contains("RoleSelection") ||
                roundState.contains("CountdownToStart") ||
                roundState.contains("Waiting"))
            {
                continue;
            }

            return true;
        }

        return false;
    }

    void ServerTick()
    {
        if (!amServer)
        {
            return;
        }

        const auto now = std::chrono::steady_clock::now();
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (now < state.NextServerTickAt)
            {
                return;
            }

            state.NextServerTickAt = now + std::chrono::seconds(1);
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return;
            }

            snapshotCopy = state.Snapshot;
        }

        constexpr int maxServerApplyAttempts = 20;
        for (APBCharacter* character : GetServerPlayerCharacters())
        {
            if (!character || !IsRuntimeRoundReadyForLiveApply(character))
            {
                continue;
            }

            int attempt = 0;
            {
                State& state = GetState();
                std::scoped_lock lock(state.Mutex);
                if (state.ServerLiveApplyComplete.contains(character))
                {
                    continue;
                }

                attempt = state.ServerLiveApplyAttempts[character];
                if (attempt >= maxServerApplyAttempts)
                {
                    continue;
                }

                state.ServerLiveApplyAttempts[character] = attempt + 1;
            }

            const bool applied = ApplySnapshotToLiveCharacter(character, snapshotCopy);
            if (applied)
            {
                State& state = GetState();
                std::scoped_lock lock(state.Mutex);
                state.ServerLiveApplyComplete.insert(character);
                state.ServerLiveApplyAttempts.erase(character);
                ClientLog("[LOADOUT][SERVER] Authoritative snapshot applied. " + BuildLiveApplyDebugSummary(character, snapshotCopy));
            }
            else if (attempt + 1 >= maxServerApplyAttempts)
            {
                ClientLog("[LOADOUT][SERVER] Authoritative snapshot apply timed out. " + BuildLiveApplyDebugSummary(character, snapshotCopy));
            }
        }
    }

    void NotifyMenuConstructed()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.MenuSeen = true;
        state.MenuSeenAt = std::chrono::steady_clock::now();
        state.InitialMenuCaptureComplete = false;
    }

    void TriggerAsyncExport(const std::string& reason, int delayMs)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.ExportScheduled = true;
        state.PendingExportReason = reason;
        state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(delayMs);
        state.NextWorkerTickAt = {};
    }

    void WorkerTick()
    {
        const auto now = std::chrono::steady_clock::now();

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (state.InGameThreadTick)
            {
                return;
            }

            if (now < state.NextWorkerTickAt)
            {
                return;
            }

            state.InGameThreadTick = true;
            state.NextWorkerTickAt = now + std::chrono::milliseconds(250);
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        bool snapshotAvailable = false;
        bool shouldTryMenuApply = false;
        bool shouldSkipMenuApply = false;
        bool shouldTryInitialCapture = false;
        bool shouldTryLiveApply = false;
        bool shouldTryScheduledExport = false;
        bool menuApplyGraceElapsed = false;
        int liveApplyAttempt = 0;
        constexpr int maxLiveApplyAttempts = 20;
        std::string exportReason;
        APBCharacter* localCharacter = GetLocalCharacter();
        const bool runtimeReadyForLiveApply = IsRuntimeRoundReadyForLiveApply(localCharacter);

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            snapshotAvailable = state.SnapshotAvailable;
            if (snapshotAvailable)
            {
                snapshotCopy = state.Snapshot;
            }

            if (state.MenuSeen && !state.InitialMenuCaptureComplete)
            {
                const auto elapsed = std::chrono::steady_clock::now() - state.MenuSeenAt;
                shouldTryInitialCapture = !state.SnapshotAvailable && elapsed >= std::chrono::seconds(2);
                menuApplyGraceElapsed = elapsed >= std::chrono::seconds(5);
            }

            shouldTryMenuApply = snapshotAvailable && !state.MenuApplyComplete && IsUnsafeMenuApplyEnabled();
            shouldSkipMenuApply = snapshotAvailable && !state.MenuApplyComplete && !IsUnsafeMenuApplyEnabled() && state.MenuSeen && menuApplyGraceElapsed;

            if (localCharacter != state.LastObservedCharacter)
            {
                state.LastObservedCharacter = localCharacter;
                state.PendingLiveApply = localCharacter != nullptr;
                state.LiveApplyAttempts = 0;
                state.NextLiveApplyAt = localCharacter ? now + std::chrono::seconds(8) : std::chrono::steady_clock::time_point{};
            }

            shouldTryLiveApply =
                snapshotAvailable &&
                state.PendingLiveApply &&
                localCharacter != nullptr &&
                runtimeReadyForLiveApply &&
                now >= state.NextLiveApplyAt &&
                state.LiveApplyAttempts < maxLiveApplyAttempts;
            if (shouldTryLiveApply)
            {
                state.LiveApplyAttempts++;
                liveApplyAttempt = state.LiveApplyAttempts;
                state.NextLiveApplyAt = now + std::chrono::seconds(5);
            }
            shouldTryScheduledExport = state.ExportScheduled && now >= state.ExportDueAt;
            exportReason = state.PendingExportReason;
        }

        if (shouldSkipMenuApply && HasArchiveLoaded())
        {
            ClientLog("[LOADOUT] Startup menu snapshot reapply is disabled by default to avoid unsafe UI recursion during login. Live battle apply remains enabled. Use -unsafeMenuLoadoutApply or PROJECTREBOUND_ENABLE_UNSAFE_MENU_APPLY=1 only for debugging.");
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.MenuApplyComplete = true;
        }

        if (shouldTryMenuApply && HasArchiveLoaded() && menuApplyGraceElapsed && IsMenuApplyReady() && ApplySnapshotToFieldModManager(snapshotCopy))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.MenuApplyComplete = true;
            state.PendingLiveApply = true;
        }

        if (shouldTryInitialCapture && HasArchiveLoaded() && ExportSnapshotNow("initial-menu"))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.InitialMenuCaptureComplete = true;
        }

        if (shouldTryLiveApply && ApplySnapshotToLiveCharacter(localCharacter, snapshotCopy))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.PendingLiveApply = false;
            state.LiveApplyAttempts = 0;
        }
        else if (shouldTryLiveApply && liveApplyAttempt >= maxLiveApplyAttempts)
        {
            ClientLog("[LOADOUT] Live snapshot apply deferred too long; stopping retries to avoid network reliable buffer pressure. " +
                BuildLiveApplyDebugSummary(localCharacter, snapshotCopy));
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.PendingLiveApply = false;
        }

        if (shouldTryScheduledExport)
        {
            const bool exported = ExportSnapshotNow(exportReason.empty() ? "scheduled" : exportReason);
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (exported)
            {
                state.ExportScheduled = false;
                state.PendingExportReason.clear();
            }
            else
            {
                state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(1000);
            }
        }

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.InGameThreadTick = false;
        }
    }
}

void StartServer()
{
    Log("[SERVER] Starting server...");

    LoadConfig();

    Log("[SERVER] Map loaded: " + std::string(Config.MapName.begin(), Config.MapName.end()));
    Log("[SERVER] Mode: " + std::string(Config.FullModePath.begin(), Config.FullModePath.end()));
    Log("[SERVER] Port: " + std::to_string(Config.Port));

    std::wstring openCmd = L"open " + Config.MapName + L"?game=" + Config.FullModePath;
    Log("[SERVER] Executing open command");

    UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), openCmd.c_str(), nullptr);

    Log("[SERVER] Waiting for world to load...");
    Sleep(8000);

    UEngine* Engine = UEngine::GetEngine();
    UWorld* World = UWorld::GetWorld();

    if (!World)
    {
        Log("[ERROR] World is NULL after map load!");
        return;
    }

    Log("[SERVER] Forcing streaming levels to load...");

    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
    {
        SDK::UObject* Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject())
            continue;

        if (Obj->IsA(ULevelStreaming::StaticClass()))
        {
            ULevelStreaming* LS = (ULevelStreaming*)Obj;

            LS->SetShouldBeLoaded(true);
            LS->SetShouldBeVisible(true);

            Log("[SERVER] Streaming level loaded: " + std::string(Obj->GetFullName()));
        }
    }

    if (!libReplicate)
    {
        Log("[ERROR] libReplicate is null before CreateNetDriver!");
        return;
    }

    Log("[SERVER] Creating NetDriver...");
    FName name = UKismetStringLibrary::Conv_StringToName(L"GameNetDriver");
    libReplicate->CreateNetDriver(Engine, World, &name);

    UIpNetDriver* NetDriver = (UIpNetDriver*)GetLastOfType(UIpNetDriver::StaticClass(), false);

    if (!NetDriver)
    {
        Log("[ERROR] NetDriver not found after CreateNetDriver!");
        return;
    }

    Log("[SERVER] NetDriver created successfully.");

    World->NetDriver = NetDriver;

    Log("[SERVER] Calling Listen()...");
    libReplicate->Listen(NetDriver, World, LibReplicate::EJoinMode::Open, Config.Port);

    listening = true;

    Log("[SERVER] Server is now listening.");
}
// ======================================================
//  SECTION 14 — CLIENT LOGIC
// ======================================================

void InitClientArmory()
{
    for (UObject* obj : getObjectsOfClass(UPBArmoryManager::StaticClass(), false)) {
        UPBArmoryManager* DefaultConfig = (UPBArmoryManager*)obj;

        std::ifstream items("DT_ItemType.json");
        nlohmann::json itemJson = nlohmann::json::parse(items);

        for (auto& [ItemId, _] : itemJson[0]["Rows"].items()) {
            std::string aString = std::string(ItemId.c_str());
            std::wstring wString = std::wstring(aString.begin(), aString.end());

            if (DefaultConfig->DefaultConfig)
                DefaultConfig->DefaultConfig->OwnedItems.Add(UKismetStringLibrary::Conv_StringToName(wString.c_str()));

            FPBItem item{};
            item.ID = UKismetStringLibrary::Conv_StringToName(wString.c_str());
            item.Count = 1;
            item.bIsNew = false;

            DefaultConfig->Armorys.OwnedItems.Add(item);
        }
    }
}

void AutoConnectToMatchFromCmdline()
{
    std::thread([]()
        {
            // Wait for world
            while (!UWorld::GetWorld())
                Sleep(100);

            // Wait for GameInstance
            while (!UWorld::GetWorld()->OwningGameInstance)
                Sleep(100);

            // Wait for LocalPlayer
            while (UWorld::GetWorld()->OwningGameInstance->LocalPlayers.Num() == 0)
                Sleep(100);

            // Wait for login complete
            while (!LoginCompleted)
                Sleep(100);

            // Delay to avoid main menu overriding the range transition
            Sleep(2000);

            // Enter Shooting Range
            auto* GI = UWorld::GetWorld()->OwningGameInstance;
            UPBLocalPlayer* LP = (UPBLocalPlayer*)GI->LocalPlayers[0];

            if (LP)
            {
                ClientLog("[CLIENT] Auto-enter Shooting Range...");
                LP->GoToRange(0.0f);
            }

            // Give travel a moment to initialize
            Sleep(1000);

            ReadyToAutoconnect = true;

            // Wait for flag
            while (!ReadyToAutoconnect)
                Sleep(100);

            Sleep(200);

            // Connect to match
            std::wstring wcmd = L"open " + std::wstring(MatchIP.begin(), MatchIP.end());
            ClientLog("[CLIENT] Auto-connecting to match: " + MatchIP);
            MatchReconnectAttempts = 0;

            UKismetSystemLibrary::ExecuteConsoleCommand(
                UWorld::GetWorld(),
                wcmd.c_str(),
                nullptr
            );

        }).detach();
}

// ======================================================
//  SECTION 15 — MAIN THREAD (ENTRY LOGIC)
// ======================================================

void MainThread()
{
    ClientLog("[BOOT] DLL injected, starting...");
    try
    {
        //Calms down the ui font missing panic
        InitMessageBoxHook();

        BaseAddress = (uintptr_t)GetModuleHandleA(nullptr);

        UC::FMemory::Init((void*)(BaseAddress + 0x18f4350));

        if (std::string(GetCommandLineA()).contains("-server")) {
            amServer = true;
        }

        while (!UWorld::GetWorld()) {
            if (amServer) {
                *(__int8*)(BaseAddress + 0x5ce2404) = 0;
                *(__int8*)(BaseAddress + 0x5ce2405) = 1;
            }
        }

        //DebugLocateSubsystems();
        //DebugDumpSubsystemsToFile();

        if (amServer)
        {
            InitServerHooks();
            Log("[SERVER] Hooks installed.");
            LoadoutExportManager::PreloadSnapshot();

            // Wait for world
            Log("[SERVER] Waiting for UWorld...");
            while (!UWorld::GetWorld())
                Sleep(10);
            Log("[SERVER] UWorld is ready.");

            //Initialize LibReplicate exactly like original code
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
                (void*)(BaseAddress + 0x3506320)
            );
            Log("[SERVER] LibReplicate initialized.");

            // Initialize LateJoinManager
            gLateJoinManager = new LateJoinManager(
                DidProcStartMatch,
                PlayerRespawnAllowedMap,
                ReportRoomStartedIfNeeded
            );
            Log("[SERVER] LateJoinManager initialized.");

            StartServer();

            // Heartbeat thread (game + backend)
            std::thread([]() {
                // Wait until Gamestate is Valid
                while (!UWorld::GetWorld() ||
                    !UWorld::GetWorld()->AuthorityGameMode ||
                    !UWorld::GetWorld()->AuthorityGameMode->GameState)
                {
                    Sleep(100);
                }
                while (true)
                {
                    int pc = GetCurrentPlayerCount();
                    std::cout << "[HEARTBEAT] PlayerCount = " << pc << std::endl;

                    if (IsRoundCurrentlyInProgress())
                    {
                        ReportRoomStartedIfNeeded();
                    }

                    if (!OnlineBackendAddress.empty())
                    {
                        SendServerStatus(OnlineBackendAddress);
                    }

                    Sleep(5000);
                }
                }).detach();
        }

        else {
            //We're client
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
            //auto dump below
            //std::thread(ClientAutoDumpThread).detach();
            //Init Hotkey Check 
            std::thread(HotkeyThread).detach();

            InitClientArmory();
            if (!MatchIP.empty())
            {
                AutoConnectToMatchFromCmdline();
            }
            /*
            Sleep(10 * 1000);

            UCommonActivatableWidget* widget = nullptr;
            reinterpret_cast<UPBMainMenuManager_BP_C*>(getObjectsOfClass(UPBMainMenuManager_BP_C::StaticClass(), false).back())->GetTopMenuWidget(&widget);
            widget->SetVisibility(ESlateVisibility::Hidden);
            widget->DeactivateWidget();

            UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), L"open 73.130.167.222", nullptr);
            */

            //UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), L"open 127.0.0.1", nullptr);
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
    DWORD  ul_reason_for_call,
    LPVOID lpReserved
)
{
    if (ul_reason_for_call == DLL_PROCESS_ATTACH) {
        std::thread t(MainThread);

        t.detach();
    }

    return TRUE;
}

// ======================================================
//  SECTION 17 — LOADOUT SYSTEM TEST
// ======================================================

void DebugLocateSubsystems()
{
    std::cout << "\nLocating Subsystems\n";

    //Armory Manager
    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    if (!armories.empty())
        std::cout << "[FOUND] UPBArmoryManager at " << armories.back() << std::endl;
    else
        std::cout << "[MISSING] UPBArmoryManager" << std::endl;

    //Field Mod Manager
    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    if (!fieldMods.empty())
        std::cout << "[FOUND] UPBFieldModManager at " << fieldMods.back() << std::endl;
    else
        std::cout << "[MISSING] UPBFieldModManager" << std::endl;

    //Weapon Part Manager
    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (!partMgrs.empty())
        std::cout << "[FOUND] UPBWeaponPartManager at " << partMgrs.back() << std::endl;
    else
        std::cout << "[MISSING] UPBWeaponPartManager" << std::endl;

    std::cout << "END PHASE 1.1\n\n";
}

void InitDebugConsole()
{
    AllocConsole();

    // Disable buffering
    setvbuf(stdout, NULL, _IONBF, 0);

    // Redirect stdout manually
    FILE* fDummy;
    freopen_s(&fDummy, "CONOUT$", "w", stdout);
    freopen_s(&fDummy, "CONOUT$", "w", stderr);

    std::wcout.clear();
    std::cout.clear();

    std::cout << "[DEBUG] Console initialized" << std::endl;
}

void DebugDumpSubsystemsToFile()
{
    std::ofstream out("subsystems_dump.txt", std::ios::trunc);
    if (!out.is_open())
        return;

    out << "=== SUBSYSTEM DUMP ===\n\n";

    // ----------------------------------------------------
    // 1) Armory Manager
    // ----------------------------------------------------
    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    if (!armories.empty())
    {
        UPBArmoryManager* Armory = (UPBArmoryManager*)armories.back();
        out << "[UPBArmoryManager] " << Armory << "\n";

        out << "  Armorys.OwnedItems:\n";
        for (int i = 0; i < Armory->Armorys.OwnedItems.Num(); ++i)
        {
            const FPBItem& item = Armory->Armorys.OwnedItems[i];
            std::string id = item.ID.ToString();

            out << "    [" << i << "] ID=" << id
                << " Count=" << item.Count
                << " bIsNew=" << (item.bIsNew ? "true" : "false") << "\n";
        }
        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBArmoryManager\n\n";
    }

    // ----------------------------------------------------
    // 2) Field Mod Manager
    // ----------------------------------------------------
    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    if (!fieldMods.empty())
    {
        UPBFieldModManager* FieldMod = (UPBFieldModManager*)fieldMods.back();
        out << "[UPBFieldModManager] " << FieldMod << "\n";

        out << "  CharacterPreOrderingInventoryConfigs:\n";
        for (auto& pair : FieldMod->CharacterPreOrderingInventoryConfigs)
        {
            // Correct SDK access: Key() and Value()
            std::string roleId = pair.Key().ToString();
            const FPBInventoryNetworkConfig& cfg = pair.Value();

            out << "    RoleID=" << roleId << "\n";

            for (int i = 0; i < cfg.CharacterSlots.Num(); ++i)
            {
                int slot = (int)cfg.CharacterSlots[i];
                std::string itemId = "";

                if (i < cfg.InventoryItems.Num())
                    itemId = cfg.InventoryItems[i].ToString();

                out << "      Slot[" << i << "] Type=" << slot
                    << " Item=" << itemId << "\n";
            }

            out << "\n";
        }
        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBFieldModManager\n\n";
    }

    // ----------------------------------------------------
    // 3) Weapon Part Manager
    // ----------------------------------------------------
    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (!partMgrs.empty())
    {
        UPBWeaponPartManager* PartMgr = (UPBWeaponPartManager*)partMgrs.back();
        out << "[UPBWeaponPartManager] " << PartMgr << "\n";

        out << "  WeaponSlotMap (keys only):\n";
        for (auto& pair : PartMgr->WeaponSlotMap)
        {
            // Correct SDK access: Key() and Value()
            APBWeapon* weapon = pair.Key();
            std::string name = weapon ? weapon->GetFullName() : "NULL";

            out << "    Weapon=" << name << "\n";
        }

        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBWeaponPartManager\n\n";
    }

    out << "=== END SUBSYSTEM DUMP ===\n";
    out.close();
}

void DebugDumpWeaponPartsToFile()
{
    std::ofstream out("weapon_parts_dump.txt", std::ios::trunc);
    if (!out.is_open())
        return;

    out << "=== WEAPON PARTS DUMP ===\n\n";

    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (partMgrs.empty())
    {
        out << "[MISSING] UPBWeaponPartManager\n";
        return;
    }

    UPBWeaponPartManager* PartMgr = (UPBWeaponPartManager*)partMgrs.back();
    out << "[UPBWeaponPartManager] " << PartMgr << "\n\n";

    out << "WeaponSlotMap:\n";

    for (auto& pair : PartMgr->WeaponSlotMap)
    {
        APBWeapon* weapon = pair.Key();   // <-- FIXED
        FWeaponSlotPartInfo info = pair.Value(); // <-- FIXED

        std::string weaponName = weapon ? weapon->GetFullName() : "NULL";
        out << "  Weapon=" << weaponName << "\n";

        // Iterate TMap<EPBPartSlotType, UPartDataHolderComponent*>
        for (auto& kvp : info.TypePartMap)
        {
            EPBPartSlotType slotType = kvp.Key();  // <-- FIXED
            UPartDataHolderComponent* holder = kvp.Value(); // <-- FIXED

            std::string partId = "NONE";
            if (holder)
            {
                FName id = holder->GetPartID();
                partId = id.ToString();
            }

            out << "    SlotType=" << (int)slotType
                << " PartID=" << partId << "\n";
        }

        out << "\n";
    }

    out << "=== END WEAPON PARTS DUMP ===\n";
    out.close();
}

//hotkey dump
void HotkeyThread()
{
    while (true)
    {
        // F5 pressed
        if (GetAsyncKeyState(VK_F5) & 0x8000)
        {
            DebugDumpSubsystemsToFile();
            ClientLog("[CLIENT] Auto-enter Shooting Range...");
            DebugDumpWeaponPartsToFile();
            ClientLog("[CLIENT] Auto-enter Shooting Range...");
            // simple debounce so it doesn't spam while held
            Sleep(300);
        }

        // F9 pressed
        if (GetAsyncKeyState(VK_F9) & 0x8000)
        {
            UPBLocalPlayer* LP = nullptr;
            auto* GI = UWorld::GetWorld()->OwningGameInstance;

            if (GI && GI->LocalPlayers.Num() > 0)
            {
                LP = (UPBLocalPlayer*)GI->LocalPlayers[0];
                if (LP)
                {
                    ClientLog("[CLIENT] Auto-enter Shooting Range...");
                    LP->GoToRange(0.0f);
                }
            }
            Sleep(300);
        }
        Sleep(10);
    }
}

