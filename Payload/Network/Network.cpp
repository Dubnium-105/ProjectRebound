// Network.cpp
#include "Network.h"
#include "../Config/Config.h"
#include "../ServerLogic/ServerLogic.h"
#include "../Debug/Debug.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../Libs/json.hpp"
#include <iostream>
#include <string>
#include <chrono>
#include <thread>
#include <Windows.h>
#include <winhttp.h>
#pragma comment(lib, "winhttp.lib")

using namespace SDK;

namespace
{
    bool BuildHttpTarget(const std::string &backend, std::string &host, INTERNET_PORT &port)
    {
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

        host = cleanBackend.substr(0, colon);
        std::string portText = cleanBackend.substr(colon + 1);

        try
        {
            int parsedPort = std::stoi(portText);
            if (parsedPort <= 0 || parsedPort > 65535)
                return false;

            port = static_cast<INTERNET_PORT>(parsedPort);
            return true;
        }
        catch (...)
        {
            std::cout << "[ONLINE] Invalid backend port." << std::endl;
            return false;
        }
    }

    bool SendJsonPost(const std::string &backend, const std::string &path, const nlohmann::json &payload, const char *logPrefix)
    {
        std::string host;
        INTERNET_PORT port = 0;
        if (!BuildHttpTarget(backend, host, port))
            return false;

        const std::string body = payload.dump();

        HINTERNET hSession = WinHttpOpen(L"BoundaryDLL/1.0",
                                         WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                         WINHTTP_NO_PROXY_NAME,
                                         WINHTTP_NO_PROXY_BYPASS, 0);
        if (!hSession)
            return false;

        WinHttpSetTimeouts(hSession, 3000, 3000, 3000, 3000);

        std::wstring whost(host.begin(), host.end());
        HINTERNET hConnect = WinHttpConnect(hSession, whost.c_str(), port, 0);
        if (!hConnect)
        {
            WinHttpCloseHandle(hSession);
            return false;
        }

        std::wstring wpath(path.begin(), path.end());
        HINTERNET hRequest = WinHttpOpenRequest(
            hConnect,
            L"POST",
            wpath.c_str(),
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

        BOOL ok = WinHttpSendRequest(
            hRequest,
            L"Content-Type: application/json",
            -1,
            (LPVOID)body.c_str(),
            (DWORD)body.size(),
            (DWORD)body.size(),
            0);

        if (ok)
            ok = WinHttpReceiveResponse(hRequest, NULL);

        WinHttpCloseHandle(hRequest);
        WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);

        std::cout << logPrefix << (ok ? " Sent " : " Failed ") << path << ": " << body << std::endl;
        return ok == TRUE;
    }
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
    int playerCount = GetCurrentPlayerCount();

    std::string map = std::string(Config.MapName.begin(), Config.MapName.end());
    std::string mode = std::string(Config.FullModePath.begin(), Config.FullModePath.end());

    std::string state = "Unknown";

    // FIXED: Add proper null checks before dereferencing
    UWorld *World = UWorld::GetWorld();
    if (World && World->AuthorityGameMode && World->AuthorityGameMode->GameState)
    {
        APBGameState *GS = (APBGameState *)World->AuthorityGameMode->GameState;
        state = GS->RoundState.ToString();
    }

    nlohmann::json payload = {
        {"name", Config.ServerName},
        {"region", Config.ServerRegion},
        {"mode", mode},
        {"map", map},
        {"port", Config.ExternalPort},
        {"playerCount", playerCount},
        {"serverState", state}};

    return payload;
}

nlohmann::json BuildRoomHeartbeatPayload()
{
    int playerCount = GetCurrentPlayerCount();
    std::string state = "Unknown";

    UWorld *World = UWorld::GetWorld();
    if (World && World->AuthorityGameMode && World->AuthorityGameMode->GameState)
    {
        APBGameState *GS = (APBGameState *)World->AuthorityGameMode->GameState;
        state = GS->RoundState.ToString();
    }

    nlohmann::json payload = {
        {"hostToken", HostToken},
        {"playerCount", playerCount},
        {"serverState", state}};

    return payload;
}

// Send Message to Backend HTTP Helper
void SendServerStatus(const std::string &backend)
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

    SendJsonPost(backend, path, payload, "[ONLINE]");
}

bool SendRoomLifecycleEvent(const std::string &backend, const std::string &lifecycleAction)
{
    if (HostRoomId.empty() || HostToken.empty())
        return false;

    nlohmann::json payload = {
        {"hostToken", HostToken}};

    std::string path = "/v1/rooms/" + HostRoomId + "/" + lifecycleAction;
    return SendJsonPost(backend, path, payload, "[LIFECYCLE]");
}

// 心跳线程（原本在 MainThread 中启动）
void StartHeartbeatThread()
{
    std::thread([]()
                {
        // Wait until Gamestate is Valid
        while (!IsServerShutdownRequested() &&
            (!UWorld::GetWorld() ||
            !UWorld::GetWorld()->AuthorityGameMode ||
            !UWorld::GetWorld()->AuthorityGameMode->GameState))
        {
            Sleep(100);
        }
        while (!IsServerShutdownRequested())
        {
            if (IsServerHeartbeatHealthy())
            {
                int pc = GetCurrentPlayerCount();
                std::cout << "[HEARTBEAT] PlayerCount = " << pc << std::endl;

                if (!OnlineBackendAddress.empty())
                {
                    SendServerStatus(OnlineBackendAddress);
                }
            }
            else
            {
                std::cout << "[HEALTH] Heartbeat suppressed: game tick is stale or shutdown is pending." << std::endl;
            }

            Sleep(5000);

        }

        std::cout << "[HEALTH] Heartbeat thread stopped." << std::endl; })
        .detach();
}