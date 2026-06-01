// PlayerNaming.cpp
// Consolidated Steam name resolution.
// PostLogin detection -> async WinHTTP -> in-place FString write on game thread.
#include "PlayerNaming.h"
#include "../Logging/LogManager.h"
#include <Windows.h>
#include <winhttp.h>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <vector>

#pragma comment(lib, "winhttp.lib")

using namespace SDK;

namespace
{
    std::mutex g_ResolveCacheMutex;
    std::unordered_map<std::string, std::string> g_ResolveCache;

    std::string HttpGetXml(const std::wstring &host, const std::wstring &path)
    {
        HINTERNET hSession = WinHttpOpen(L"BoundaryUserNameFix/1.0",
                                         WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                         WINHTTP_NO_PROXY_NAME,
                                         WINHTTP_NO_PROXY_BYPASS, 0);
        if (!hSession) return {};
        WinHttpSetTimeouts(hSession, 3000, 3000, 3000, 3000);
        HINTERNET hConnect = WinHttpConnect(hSession, host.c_str(),
                                            INTERNET_DEFAULT_HTTPS_PORT, 0);
        if (!hConnect) { WinHttpCloseHandle(hSession); return {}; }
        HINTERNET hRequest = WinHttpOpenRequest(
            hConnect, L"GET", path.c_str(),
            NULL, WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES,
            WINHTTP_FLAG_SECURE);
        if (!hRequest) { WinHttpCloseHandle(hConnect); WinHttpCloseHandle(hSession); return {}; }
        std::string result;
        BOOL ok = WinHttpSendRequest(hRequest, WINHTTP_NO_ADDITIONAL_HEADERS, 0,
                                     WINHTTP_NO_REQUEST_DATA, 0, 0, 0);
        if (ok) ok = WinHttpReceiveResponse(hRequest, NULL);
        if (ok) {
            DWORD bytesRead = 0;
            char buffer[2048];
            while (WinHttpReadData(hRequest, buffer, sizeof(buffer) - 1, &bytesRead) && bytesRead > 0) {
                buffer[bytesRead] = '\0';
                result.append(buffer, bytesRead);
                if (result.size() > 65536) break;
            }
        }
        WinHttpCloseHandle(hRequest);
        WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);
        return result;
    }

    std::string ExtractSteamNameFromXml(const std::string &xml)
    {
        constexpr const char *kOpen  = "<steamID>";
        constexpr const char *kClose = "</steamID>";
        size_t pos = xml.find(kOpen);
        if (pos == std::string::npos) return {};
        pos += strlen(kOpen);
        size_t end = xml.find(kClose, pos);
        if (end == std::string::npos) return {};
        std::string raw = xml.substr(pos, end - pos);
        constexpr const char *kCdataOpen  = "<![CDATA[";
        constexpr const char *kCdataClose = "]]>";
        if (raw.starts_with(kCdataOpen) && raw.ends_with(kCdataClose))
            raw = raw.substr(strlen(kCdataOpen), raw.size() - strlen(kCdataOpen) - strlen(kCdataClose));
        size_t first = raw.find_first_not_of(" \t\r\n");
        size_t last  = raw.find_last_not_of(" \t\r\n");
        if (first == std::string::npos) return {};
        return raw.substr(first, last - first + 1);
    }
}

static bool LooksLikeSteamId64(const std::string &s)
{
    if (s.length() != 17) return false;
    for (char c : s) if (c < '0' || c > '9') return false;
    return true;
}

static std::string ResolveSteamName(const std::string &steamId64)
{
    if (!LooksLikeSteamId64(steamId64)) return {};
    {
        std::lock_guard<std::mutex> lock(g_ResolveCacheMutex);
        auto it = g_ResolveCache.find(steamId64);
        if (it != g_ResolveCache.end()) return it->second;
    }
    std::wstring host(L"steamcommunity.com");
    std::wstring path(L"/profiles/" + std::wstring(steamId64.begin(), steamId64.end()) + L"/?xml=1");
    std::string xml = HttpGetXml(host, path);
    if (xml.empty()) return {};
    std::string name = ExtractSteamNameFromXml(xml);
    if (name.empty()) return {};
    {
        std::lock_guard<std::mutex> lock(g_ResolveCacheMutex);
        g_ResolveCache[steamId64] = name;
    }
    return name;
}

namespace
{
    struct PendingNameChange
    {
        AGameMode *GameMode;
        APBPlayerController *PC;
        std::string ResolvedName;
    };
    std::mutex g_PendingMutex;
    std::vector<PendingNameChange> g_Pending;
}

static void InPlaceFStringWrite(APlayerState *PS, uintptr_t offset, const wchar_t *text)
{
    uintptr_t base = reinterpret_cast<uintptr_t>(PS);
    TCHAR*&  data  = *reinterpret_cast<TCHAR**>(base + offset + 0);
    int32&   count = *reinterpret_cast<int32*> (base + offset + 8);
    int32&   max   = *reinterpret_cast<int32*> (base + offset + 12);
    int32 needed = static_cast<int32>(wcslen(text));
    if (max > needed) {
        wcscpy_s(data, max, text);
        count = needed + 1;
    }
}

void UserNameFix_OnPostLogin(AGameMode *GameMode, APBPlayerController *PC)
{
    if (!PC || !PC->PlayerState || !GameMode) return;
    APBPlayerState *PBPS = static_cast<APBPlayerState *>(PC->PlayerState);
    std::string steamIdStr = PBPS->GetDefaultIDStr().ToString();
    std::string currentName = PBPS->GetPlayerName().ToString();

    ServerDebugLog("[NAME-FIX] PlayerName=\"" + currentName + "\" SteamID=" + steamIdStr);

    if (!LooksLikeSteamId64(steamIdStr))
    {
        ServerDebugLog("[NAME-FIX] SKIP: not a valid SteamID64");
        return;
    }

    ServerDebugLog("[NAME-FIX] Spawning resolver for " + steamIdStr);
    std::thread([GameMode, PC, steamIdStr]() {
        std::string resolved = ResolveSteamName(steamIdStr);
        if (!resolved.empty()) {
            std::lock_guard<std::mutex> lock(g_PendingMutex);
            g_Pending.push_back({GameMode, PC, resolved});
        } else {
            ServerDebugLog("[NAME-FIX] Resolve FAILED for " + steamIdStr);
        }
    }).detach();
}

void UserNameFix_DrainPending()
{
    std::lock_guard<std::mutex> lock(g_PendingMutex);
    for (auto &req : g_Pending) {
        if (!req.GameMode || !req.PC || !req.PC->PlayerState) continue;
        std::wstring wResolved(req.ResolvedName.begin(), req.ResolvedName.end());
        InPlaceFStringWrite(req.PC->PlayerState, 0x0300, wResolved.c_str());
        req.PC->PlayerState->OnRep_PlayerName();
        ServerDebugLog("[NAME-FIX] Set name: \"" + req.ResolvedName + "\"");
    }
    g_Pending.clear();
}
