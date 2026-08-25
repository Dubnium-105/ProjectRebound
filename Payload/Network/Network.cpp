// Network.cpp
#include "Network.h"
#include "../Config/Config.h"
#include "../ServerLogic/ServerLogic.h"
#include "../ServerLogic/DedicatedMultiMatch.h"
#include "../Debug/Debug.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../Libs/json.hpp"
#include <iostream>
#include <string>
#include <atomic>
#include <mutex>
#include <thread>
#include <Windows.h>
#include <winhttp.h>
#pragma comment(lib, "winhttp.lib")

using namespace SDK;

constexpr const char *BACKEND_PRIMARY = "https://api.project-rebound.space";

namespace
{
    std::atomic<int> ToolboxPlayerCount{0};
    std::atomic<bool> ToolboxStatusReady{false};
    std::mutex ToolboxRoundStateMutex;
    std::string ToolboxRoundState = "Unknown";

    void RefreshToolboxServerStatus(const int playerCount)
    {
        ToolboxPlayerCount.store(playerCount < 0 ? 0 : playerCount, std::memory_order_release);
        std::string state = "Unknown";
        if (DedicatedMultiMatch::OwnsWorldTransition())
        {
            state = "Transitioning";
        }
        else if (UWorld *world = UWorld::GetWorld();
            world && world->AuthorityGameMode && world->AuthorityGameMode->GameState)
        {
            APBGameState *gameState = (APBGameState *)world->AuthorityGameMode->GameState;
            state = gameState->RoundState.ToString();
        }
        std::lock_guard<std::mutex> lock(ToolboxRoundStateMutex);
        ToolboxRoundState = std::move(state);
    }
}

void RefreshServerStatusSnapshot()
{
    const bool transitioning = DedicatedMultiMatch::OwnsWorldTransition();
    const int playerCount = transitioning
        ? ToolboxPlayerCount.load(std::memory_order_acquire)
        : GetCurrentPlayerCount();
    RefreshToolboxServerStatus(playerCount);
    ToolboxStatusReady.store(true, std::memory_order_release);
}

// ======================================================
//  SECTION 4 — UTILITY HELPERS (network related)
// ======================================================

std::string StripHttpScheme(const std::string &backend)
{
    const std::string http = "http://";
    const std::string https = "https://";

    if (backend.rfind(http, 0) == 0)
        return backend.substr(http.length());

    if (backend.rfind(https, 0) == 0)
        return backend.substr(https.length());

    return backend;
}

nlohmann::json BuildServerStatusPayload()
{
    const int playerCount = ToolboxPlayerCount.load(std::memory_order_acquire);

    const nlohmann::json multiMatch = DedicatedMultiMatch::BuildStatusPayload();
    std::string map;
    if (multiMatch.value("enabled", false))
        map = multiMatch.value("activeMap", "");
    if (map.empty())
        map = std::string(Config.MapName.begin(), Config.MapName.end());
    std::string mode = std::string(Config.FullModePath.begin(), Config.FullModePath.end());

    std::string state;
    {
        std::lock_guard<std::mutex> lock(ToolboxRoundStateMutex);
        state = ToolboxRoundState;
    }

    nlohmann::json payload = {
        {"name", Config.ServerName},
        {"region", Config.ServerRegion},
        {"mode", mode},
        {"map", map},
        {"port", Config.ExternalPort},
        {"playerCount", playerCount},
        {"serverState", state},
        {"lifecycleState", multiMatch.value("lifecycleState", "Disabled")},
        {"activeMap", map},
        {"nextMap", multiMatch.value("nextMap", "")},
        {"matchGeneration", multiMatch.value("matchGeneration", 0ULL)},
        {"vote", multiMatch.value("vote", nlohmann::json::object())}};

    return payload;
}

nlohmann::json BuildRoomHeartbeatPayload()
{
    const int playerCount = ToolboxPlayerCount.load(std::memory_order_acquire);
    std::string state;
    {
        std::lock_guard<std::mutex> lock(ToolboxRoundStateMutex);
        state = ToolboxRoundState;
    }

    nlohmann::json payload = {
        {"hostToken", HostToken},
        {"playerCount", playerCount},
        {"serverState", state}};

    return payload;
}

// Generic HTTP POST helper
bool PostJsonToBackend(const std::string &backend, const std::string &path, const nlohmann::json &payload)
{
    std::string body = payload.dump();
    std::string cleanBackend = StripHttpScheme(backend);

    size_t slash = cleanBackend.find('/');
    if (slash != std::string::npos)
        cleanBackend = cleanBackend.substr(0, slash);

    size_t colon = cleanBackend.find(':');
    std::string host = colon == std::string::npos ? cleanBackend : cleanBackend.substr(0, colon);
    const bool isHttps = backend.rfind("https://", 0) == 0;
    INTERNET_PORT requestPort = isHttps ? INTERNET_DEFAULT_HTTPS_PORT : INTERNET_DEFAULT_HTTP_PORT;
    if (colon != std::string::npos)
        requestPort = static_cast<INTERNET_PORT>(std::stoi(cleanBackend.substr(colon + 1)));

    HINTERNET hSession = WinHttpOpen(L"BoundaryDLL/1.0",
                                     WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                     WINHTTP_NO_PROXY_NAME,
                                     WINHTTP_NO_PROXY_BYPASS, 0);

    if (!hSession)
        return false;

    WinHttpSetTimeouts(hSession, 5000, 5000, 15000, 15000);
    std::wstring whost(host.begin(), host.end());

    HINTERNET hConnect = WinHttpConnect(hSession, whost.c_str(), requestPort, 0);
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
        isHttps ? WINHTTP_FLAG_SECURE : 0);

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

    if (bResults)
        bResults = WinHttpReceiveResponse(hRequest, NULL);

    DWORD statusCode = 0;
    DWORD statusSize = sizeof(statusCode);
    if (bResults)
        bResults = WinHttpQueryHeaders(hRequest, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                                       WINHTTP_HEADER_NAME_BY_INDEX, &statusCode, &statusSize,
                                       WINHTTP_NO_HEADER_INDEX);

    WinHttpCloseHandle(hRequest);
    WinHttpCloseHandle(hConnect);
    WinHttpCloseHandle(hSession);

    std::cout << "[ONLINE] POST " << path << " returned HTTP " << statusCode << std::endl;
    return bResults && statusCode >= 200 && statusCode < 300;
}

// Send Message to Backend HTTP Helper
void SendServerStatus(const std::string &backend)
{
    bool useRoomHeartbeat = !HostRoomId.empty() && !HostToken.empty();
    if (useRoomHeartbeat)
    {
        PostJsonToBackend(backend, "/v1/rooms/" + HostRoomId + "/heartbeat", BuildRoomHeartbeatPayload());
    }
}

bool SendRoomLifecycleStart(const std::string &backend)
{
    if (HostRoomId.empty() || HostToken.empty())
        return false;

    nlohmann::json payload = {
        { "hostToken", HostToken }
    };
    return PostJsonToBackend(backend, "/v1/rooms/" + HostRoomId + "/start", payload);
}

static bool RoomStartReportSucceeded = false;
static bool RoomStartReportInFlight = false;
static std::mutex RoomStartReportMutex;
static bool DisableBackendRoomStartPromotion = true;

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

// 心跳线程（原本在 MainThread 中启动）
void StartHeartbeatThread()
{
    std::thread([]()
                {
        // UObject state is sampled by the game-thread NetDriver tick.  The
        // detached HTTP worker only consumes the resulting atomics/strings.
        while (!ToolboxStatusReady.load(std::memory_order_acquire))
        {
            Sleep(100);
        }
        while (true)
        {
            const int pc = ToolboxPlayerCount.load(std::memory_order_acquire);
            std::cout << "[HEARTBEAT] PlayerCount = " << pc << std::endl;

            if (!OnlineBackendAddress.empty())
            {
                SendServerStatus(OnlineBackendAddress);
            }
            else
            {
                SendServerStatus(BACKEND_PRIMARY);
            }

            Sleep(5000);
        } })
        .detach();
}
