// ======================================================
//  MetaserverClient — HTTP 客户端实现 (WinHTTP)
// ======================================================

#include "MetaserverClient.h"

#include <Windows.h>
#include <winhttp.h>
#include <string>
#include <sstream>
#include <iostream>

#pragma comment(lib, "winhttp.lib")

using json = nlohmann::json;

// ---- 构造 ----

MetaserverClient::MetaserverClient(const std::string& baseUrl)
    : baseUrl_(baseUrl)
{
    // 去掉末尾斜杠
    while (!baseUrl_.empty() && baseUrl_.back() == '/')
    {
        baseUrl_.pop_back();
    }
}

// ---- 健康检查 ----

bool MetaserverClient::IsAvailable()
{
    auto result = HttpGet("/api/health");
    return result.has_value() && result->value("status", "") == "ok";
}

// ---- 定义查询 ----

std::optional<RoleDef> MetaserverClient::GetRole(const std::string& roleId)
{
    {
        std::scoped_lock lock(cacheMutex_);
        auto it = roleCache_.find(roleId);
        if (it != roleCache_.end())
        {
            return it->second;
        }
    }

    auto result = HttpGet("/api/definitions/roles/" + roleId);
    if (!result.has_value())
    {
        return std::nullopt;
    }

    RoleDef def = ParseRoleDef(result.value());

    {
        std::scoped_lock lock(cacheMutex_);
        roleCache_[roleId] = def;
    }

    return def;
}

std::optional<WeaponDef> MetaserverClient::GetWeapon(const std::string& weaponId)
{
    {
        std::scoped_lock lock(cacheMutex_);
        auto it = weaponCache_.find(weaponId);
        if (it != weaponCache_.end())
        {
            return it->second;
        }
    }

    auto result = HttpGet("/api/definitions/weapons/" + weaponId);
    if (!result.has_value())
    {
        return std::nullopt;
    }

    WeaponDef def = ParseWeaponDef(result.value());

    {
        std::scoped_lock lock(cacheMutex_);
        weaponCache_[weaponId] = def;
    }

    return def;
}

std::optional<std::string> MetaserverClient::GetItemType(const std::string& itemId)
{
    auto result = HttpGet("/api/definitions/items/" + itemId + "/type");
    if (!result.has_value())
    {
        return std::nullopt;
    }

    bool found = result->value("found", false);
    if (!found)
    {
        return std::nullopt;
    }

    return result->value("type", std::string("Unknown"));
}

// ---- 武器重定向 ----

std::optional<std::string> MetaserverClient::ResolveRoleWeaponId(const std::string& roleId, const std::string& baseWeaponId)
{
    auto result = HttpGet("/api/definitions/resolve-weapon/" + roleId + "/" + baseWeaponId);
    if (!result.has_value())
    {
        return std::nullopt;
    }

    bool found = result->value("found", false);
    if (!found)
    {
        return std::nullopt;
    }

    std::string roleWeaponId = result->value("roleWeaponId", "");
    if (roleWeaponId.empty())
    {
        return std::nullopt;
    }

    return roleWeaponId;
}

// ---- 配装查询 ----

std::optional<json> MetaserverClient::GetPlayerLoadout(const std::string& playerId)
{
    return HttpGet("/api/loadout/" + playerId);
}

std::optional<json> MetaserverClient::GetPlayerRoleLoadout(const std::string& playerId, const std::string& roleId)
{
    return HttpGet("/api/loadout/" + playerId + "/" + roleId);
}

// ---- 配装修验/过滤 ----

ValidationResult MetaserverClient::ValidateLoadout(const json& loadout)
{
    ValidationResult vr;

    json body = { { "loadout", loadout } };
    auto result = HttpPost("/api/loadout/validate", body);

    if (!result.has_value())
    {
        vr.valid = false;
        vr.errors.push_back("Metaserver unreachable");
        return vr;
    }

    vr.valid = result->value("valid", true);
    if (result->contains("errors"))
    {
        for (const auto& e : (*result)["errors"])
        {
            vr.errors.push_back(e.get<std::string>());
        }
    }
    if (result->contains("warnings"))
    {
        for (const auto& w : (*result)["warnings"])
        {
            vr.warnings.push_back(w.get<std::string>());
        }
    }

    return vr;
}

json MetaserverClient::FilterLoadout(const json& loadout)
{
    json body = { { "loadout", loadout } };
    auto result = HttpPost("/api/loadout/filter", body);

    if (!result.has_value())
    {
        // metaserver 不可用，返回原始 loadout
        return loadout;
    }

    if (result->contains("loadout"))
    {
        return (*result)["loadout"];
    }

    return loadout;
}

// ---- 缓存控制 ----

void MetaserverClient::ClearCache()
{
    std::scoped_lock lock(cacheMutex_);
    roleCache_.clear();
    weaponCache_.clear();
}

// ---- HTTP 内部方法 ----

std::optional<json> MetaserverClient::HttpGet(const std::string& path)
{
    auto body = WinHttpRequest("GET", path, nullptr);
    if (!body.has_value())
    {
        return std::nullopt;
    }

    try
    {
        json j = json::parse(body.value(), nullptr, false);
        if (j.is_discarded())
        {
            return std::nullopt;
        }
        return j;
    }
    catch (...)
    {
        return std::nullopt;
    }
}

std::optional<json> MetaserverClient::HttpPost(const std::string& path, const json& body)
{
    std::string bodyStr = body.dump();
    auto response = WinHttpRequest("POST", path, &bodyStr);
    if (!response.has_value())
    {
        return std::nullopt;
    }

    try
    {
        json j = json::parse(response.value(), nullptr, false);
        if (j.is_discarded())
        {
            return std::nullopt;
        }
        return j;
    }
    catch (...)
    {
        return std::nullopt;
    }
}

// ---- WinHTTP 底层 ----

std::optional<std::string> MetaserverClient::WinHttpRequest(const std::string& method, const std::string& path,
                                                            const std::string* body)
{
    // 解析 base URL 获取 host 和 port
    std::string host = "127.0.0.1";
    int port = 8000;
    bool useHttps = false;

    std::string url = baseUrl_;
    if (url.rfind("https://", 0) == 0)
    {
        useHttps = true;
        url = url.substr(8);
    }
    else if (url.rfind("http://", 0) == 0)
    {
        url = url.substr(7);
    }

    size_t colonPos = url.find(':');
    size_t slashPos = url.find('/');
    if (colonPos != std::string::npos)
    {
        host = url.substr(0, colonPos);
        std::string portStr;
        if (slashPos != std::string::npos)
        {
            portStr = url.substr(colonPos + 1, slashPos - colonPos - 1);
        }
        else
        {
            portStr = url.substr(colonPos + 1);
        }
        try { port = std::stoi(portStr); } catch (...) { port = useHttps ? 443 : 8000; }
    }
    else
    {
        if (slashPos != std::string::npos)
        {
            host = url.substr(0, slashPos);
        }
        else
        {
            host = url;
        }
        port = useHttps ? 443 : 8000;
    }

    std::wstring wHost(host.begin(), host.end());
    std::wstring wPath(path.begin(), path.end());

    // 打开 WinHTTP session
    HINTERNET hSession = WinHttpOpen(L"ProjectRebound-LoadoutManager/1.0",
                                     WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                     WINHTTP_NO_PROXY_NAME,
                                     WINHTTP_NO_PROXY_BYPASS, 0);
    if (!hSession)
    {
        return std::nullopt;
    }

    HINTERNET hConnect = WinHttpConnect(hSession, wHost.c_str(), port, 0);
    if (!hConnect)
    {
        WinHttpCloseHandle(hSession);
        return std::nullopt;
    }

    DWORD dwFlags = useHttps ? WINHTTP_FLAG_SECURE : 0;
    HINTERNET hRequest = WinHttpOpenRequest(hConnect,
                                            method == "POST" ? L"POST" : L"GET",
                                            wPath.c_str(),
                                            nullptr,
                                            WINHTTP_NO_REFERER,
                                            WINHTTP_DEFAULT_ACCEPT_TYPES,
                                            dwFlags);
    if (!hRequest)
    {
        WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);
        return std::nullopt;
    }

    // 设置超时
    DWORD timeout = 3000; // 3 秒（本地调用）
    WinHttpSetOption(hRequest, WINHTTP_OPTION_CONNECT_TIMEOUT, &timeout, sizeof(timeout));
    WinHttpSetOption(hRequest, WINHTTP_OPTION_RECEIVE_TIMEOUT, &timeout, sizeof(timeout));
    WinHttpSetOption(hRequest, WINHTTP_OPTION_SEND_TIMEOUT, &timeout, sizeof(timeout));

    BOOL sendResult = FALSE;
    if (body && method == "POST")
    {
        std::string bodyStr = *body;
        LPCWSTR headers = L"Content-Type: application/json\r\n";
        if (!WinHttpSendRequest(hRequest, headers, (DWORD)-1,
                                (LPVOID)bodyStr.c_str(), (DWORD)bodyStr.size(),
                                (DWORD)bodyStr.size(), 0))
        {
            WinHttpCloseHandle(hRequest);
            WinHttpCloseHandle(hConnect);
            WinHttpCloseHandle(hSession);
            return std::nullopt;
        }
    }
    else
    {
        if (!WinHttpSendRequest(hRequest, WINHTTP_NO_ADDITIONAL_HEADERS, 0,
                                WINHTTP_NO_REQUEST_DATA, 0, 0, 0))
        {
            WinHttpCloseHandle(hRequest);
            WinHttpCloseHandle(hConnect);
            WinHttpCloseHandle(hSession);
            return std::nullopt;
        }
    }

    if (!WinHttpReceiveResponse(hRequest, nullptr))
    {
        WinHttpCloseHandle(hRequest);
        WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);
        return std::nullopt;
    }

    // 读取响应
    std::string response;
    DWORD bytesAvailable = 0;
    char buffer[4096];

    while (WinHttpQueryDataAvailable(hRequest, &bytesAvailable) && bytesAvailable > 0)
    {
        DWORD bytesToRead = bytesAvailable < sizeof(buffer) ? bytesAvailable : sizeof(buffer);
        DWORD bytesRead = 0;
        if (WinHttpReadData(hRequest, buffer, bytesToRead, &bytesRead))
        {
            response.append(buffer, bytesRead);
        }
        else
        {
            break;
        }
    }

    WinHttpCloseHandle(hRequest);
    WinHttpCloseHandle(hConnect);
    WinHttpCloseHandle(hSession);

    if (response.empty())
    {
        return std::nullopt;
    }

    return response;
}

// ---- JSON 解析辅助 ----

RoleDef MetaserverClient::ParseRoleDef(const json& j)
{
    RoleDef def;
    def.radarId = j.value("radarId", "");
    def.vehicleId = j.value("vehicleId", "");

    auto parseSet = [](const json& arr) -> std::unordered_set<std::string> {
        std::unordered_set<std::string> result;
        if (arr.is_array())
        {
            for (const auto& item : arr)
            {
                result.insert(item.get<std::string>());
            }
        }
        return result;
    };

    def.weaponScope = parseSet(j.value("weaponScope", json::array()));
    def.podScope = parseSet(j.value("podScope", json::array()));
    def.meleeWeaponScope = parseSet(j.value("meleeWeaponScope", json::array()));
    def.mobilityScope = parseSet(j.value("mobilityScope", json::array()));

    return def;
}

WeaponDef MetaserverClient::ParseWeaponDef(const json& j)
{
    WeaponDef def;
    if (j.contains("slotScopes") && j["slotScopes"].is_object())
    {
        for (auto& [slotName, partArray] : j["slotScopes"].items())
        {
            std::unordered_set<std::string> partSet;
            if (partArray.is_array())
            {
                for (const auto& part : partArray)
                {
                    partSet.insert(part.get<std::string>());
                }
            }
            def[slotName] = std::move(partSet);
        }
    }
    return def;
}
