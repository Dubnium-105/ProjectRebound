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
#include <algorithm>
#include <cctype>
#include <chrono>
#include <mutex>
#include <thread>
#include <vector>
#include <Windows.h>
#include <winhttp.h>
#pragma comment(lib, "winhttp.lib")

using namespace SDK;

constexpr const char *BACKEND_PRIMARY = "https://api.project-rebound.space";
constexpr const char *BACKEND_FALLBACK = "https://cnapi.project-rebound.space";
constexpr const char *REG_TOKEN_PLACEHOLDER = "YOUR_TOKEN_HERE";

namespace
{
    std::wstring QuoteWindowsArgument(const std::string &value)
    {
        std::wstring input(value.begin(), value.end());
        std::wstring quoted = L"\"";
        size_t backslashes = 0;
        for (wchar_t character : input)
        {
            if (character == L'\\')
            {
                ++backslashes;
                continue;
            }
            if (character == L'\"')
            {
                quoted.append(backslashes * 2 + 1, L'\\');
                quoted.push_back(L'\"');
            }
            else
            {
                quoted.append(backslashes, L'\\');
                quoted.push_back(character);
            }
            backslashes = 0;
        }
        quoted.append(backslashes * 2, L'\\');
        quoted.push_back(L'\"');
        return quoted;
    }

    std::string IdentityFileName()
    {
        std::string instance = Config.ServerUniqueId.empty() ? "server-unknown" : Config.ServerUniqueId;
        std::replace_if(instance.begin(), instance.end(), [](unsigned char character)
                        { return !(std::isalnum(character) || character == '.' || character == '_' || character == '-'); },
                        '_');
        return "game-server-identity-" + instance + ".json";
    }

    bool FileExists(const std::string &path)
    {
        DWORD attributes = GetFileAttributesA(path.c_str());
        return attributes != INVALID_FILE_ATTRIBUTES && (attributes & FILE_ATTRIBUTE_DIRECTORY) == 0;
    }

    bool RunGameServerAgent(const std::string &backend)
    {
        static ULONGLONG lastSuccessfulRun = 0;
        const ULONGLONG now = GetTickCount64();
        if (lastSuccessfulRun != 0 && now - lastSuccessfulRun < 15000)
            return true;

        const std::string identityFile = IdentityFileName();
        const bool hasIdentity = FileExists(identityFile);
        if (Config.ServerUniqueId.empty() || (!hasIdentity && Config.PublicHost.empty()))
        {
            static bool configurationBannerShown = false;
            if (!configurationBannerShown)
            {
                configurationBannerShown = true;
                Log("[ONLINE] Dedicated Server registration requires -serverid and -publichost.");
            }
            return false;
        }
        if (!hasIdentity && (RegistrationToken.empty() || RegistrationToken == REG_TOKEN_PLACEHOLDER))
        {
            static bool tokenBannerShown = false;
            if (!tokenBannerShown)
            {
                tokenBannerShown = true;
                Log("[ONLINE] Registration token is not configured; server remains unlisted.");
            }
            return false;
        }
        if (!FileExists(Config.GameServerAgentPath))
        {
            static bool agentBannerShown = false;
            if (!agentBannerShown)
            {
                agentBannerShown = true;
                Log("[ONLINE] game-server-agent executable was not found: " + Config.GameServerAgentPath);
            }
            return false;
        }

        const std::string primary = backend.empty() ? BACKEND_PRIMARY : backend;
        const std::string fallback = primary == BACKEND_PRIMARY
                                         ? BACKEND_FALLBACK
                                         : (primary == BACKEND_FALLBACK ? BACKEND_PRIMARY : "");
        const std::string mode = Config.IsPvE ? "pve" : "tdm";
        const int playerCount = GetCurrentPlayerCount();
        const std::string heartbeatState = hasIdentity && playerCount > 0 ? "RUNNING" : "READY";
        std::wstring command = QuoteWindowsArgument(Config.GameServerAgentPath);
        auto append = [&command](const char *flag, const std::string &value)
        {
            command += L" ";
            command += std::wstring(flag, flag + std::char_traits<char>::length(flag));
            command += L" ";
            command += QuoteWindowsArgument(value);
        };
        append("-control-plane-url", primary);
        if (!fallback.empty())
            append("-fallback-control-plane-url", fallback);
        append("-identity-file", identityFile);
        append("-instance-id", Config.ServerUniqueId);
        append("-display-name", Config.ServerName);
        append("-region", Config.ServerRegion);
        append("-mode", mode);
        append("-version", "0.7.0");
        append("-public-host", Config.PublicHost);
        append("-public-port", std::to_string(Config.ExternalPort));
        append("-max-players", std::to_string(Config.MaxPlayers));
        append("-heartbeat-state", heartbeatState);
        append("-player-count", std::to_string(playerCount));
        command += L" -once";

        std::vector<wchar_t> mutableCommand(command.begin(), command.end());
        mutableCommand.push_back(L'\0');
        STARTUPINFOW startup{};
        startup.cb = sizeof(startup);
        PROCESS_INFORMATION process{};
        if (!CreateProcessW(nullptr, mutableCommand.data(), nullptr, nullptr, FALSE,
                            CREATE_NO_WINDOW, nullptr, nullptr, &startup, &process))
        {
            Log("[ONLINE] Failed to start game-server-agent.");
            return false;
        }
        WaitForSingleObject(process.hProcess, INFINITE);
        DWORD exitCode = 1;
        GetExitCodeProcess(process.hProcess, &exitCode);
        CloseHandle(process.hThread);
        CloseHandle(process.hProcess);
        if (exitCode != 0)
        {
            Log("[ONLINE] game-server-agent failed with exit code " + std::to_string(exitCode) + ".");
            return false;
        }
        if (!RegistrationToken.empty() && FileExists(identityFile))
        {
            SetEnvironmentVariableA("GAME_SERVER_REGISTRATION_TOKEN", nullptr);
            SecureZeroMemory(RegistrationToken.data(), RegistrationToken.size());
            RegistrationToken.clear();
        }
        lastSuccessfulRun = GetTickCount64();
        return true;
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
        return;
    }
    RunGameServerAgent(backend);
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
