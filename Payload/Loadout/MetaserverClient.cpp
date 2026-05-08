#include "MetaserverClient.h"

#include <Windows.h>
#include <winhttp.h>

#include <algorithm>
#include <cctype>
#include <utility>
#include <vector>

#pragma comment(lib, "winhttp.lib")

namespace LoadoutMetaserver
{
    namespace
    {
        struct WinHttpHandle
        {
            HINTERNET Handle = nullptr;

            WinHttpHandle() = default;
            explicit WinHttpHandle(HINTERNET handle) : Handle(handle) {}
            ~WinHttpHandle() { if (Handle) WinHttpCloseHandle(Handle); }

            WinHttpHandle(const WinHttpHandle&) = delete;
            WinHttpHandle& operator=(const WinHttpHandle&) = delete;

            explicit operator bool() const { return Handle != nullptr; }
        };

        std::wstring ToWide(const std::string& value)
        {
            if (value.empty()) return L"";
            const int needed = MultiByteToWideChar(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), nullptr, 0);
            if (needed <= 0) return std::wstring(value.begin(), value.end());
            std::wstring wide(static_cast<size_t>(needed), L'\0');
            MultiByteToWideChar(CP_UTF8, 0, value.data(), static_cast<int>(value.size()), wide.data(), needed);
            return wide;
        }

        std::string Trim(std::string value)
        {
            auto isSpace = [](unsigned char ch) { return std::isspace(ch) != 0; };
            value.erase(value.begin(), std::find_if(value.begin(), value.end(), [&](char ch) { return !isSpace(static_cast<unsigned char>(ch)); }));
            value.erase(std::find_if(value.rbegin(), value.rend(), [&](char ch) { return !isSpace(static_cast<unsigned char>(ch)); }).base(), value.end());
            return value;
        }

        std::string NormalizeBaseUrl(std::string baseUrl)
        {
            baseUrl = Trim(std::move(baseUrl));
            if (baseUrl.size() >= 2 &&
                ((baseUrl.front() == '"' && baseUrl.back() == '"') ||
                    (baseUrl.front() == '\'' && baseUrl.back() == '\'')))
            {
                baseUrl = baseUrl.substr(1, baseUrl.size() - 2);
                baseUrl = Trim(std::move(baseUrl));
            }
            if (baseUrl.empty()) baseUrl = "http://127.0.0.1:8000";
            if (baseUrl.find("://") == std::string::npos) baseUrl = "http://" + baseUrl;
            while (!baseUrl.empty() && baseUrl.back() == '/') baseUrl.pop_back();
            return baseUrl;
        }

        std::string UrlEncodePathSegment(const std::string& value)
        {
            static const char* hex = "0123456789ABCDEF";
            std::string encoded;
            for (unsigned char ch : value)
            {
                const bool safe = std::isalnum(ch) || ch == '-' || ch == '_' || ch == '.' || ch == '~';
                if (safe)
                {
                    encoded.push_back(static_cast<char>(ch));
                }
                else
                {
                    encoded.push_back('%');
                    encoded.push_back(hex[ch >> 4]);
                    encoded.push_back(hex[ch & 0x0F]);
                }
            }
            return encoded;
        }

        bool CrackUrl(const std::string& url, std::wstring& host, INTERNET_PORT& port, std::wstring& path, bool& secure)
        {
            const std::wstring wideUrl = ToWide(url);
            URL_COMPONENTS components{};
            components.dwStructSize = sizeof(components);
            components.dwSchemeLength = static_cast<DWORD>(-1);
            components.dwHostNameLength = static_cast<DWORD>(-1);
            components.dwUrlPathLength = static_cast<DWORD>(-1);
            components.dwExtraInfoLength = static_cast<DWORD>(-1);

            if (!WinHttpCrackUrl(wideUrl.c_str(), 0, 0, &components)) return false;

            host.assign(components.lpszHostName, components.dwHostNameLength);
            port = components.nPort;
            secure = components.nScheme == INTERNET_SCHEME_HTTPS;

            if (components.lpszUrlPath && components.dwUrlPathLength > 0)
                path.assign(components.lpszUrlPath, components.dwUrlPathLength);
            else
                path = L"/";
            if (components.lpszExtraInfo && components.dwExtraInfoLength > 0)
                path.append(components.lpszExtraInfo, components.dwExtraInfoLength);
            return !host.empty();
        }
    }

    MetaserverClient::MetaserverClient(std::string baseUrl)
    {
        SetBaseUrl(std::move(baseUrl));
    }

    void MetaserverClient::SetBaseUrl(std::string baseUrl)
    {
        baseUrl_ = NormalizeBaseUrl(std::move(baseUrl));
    }

    const std::string& MetaserverClient::BaseUrl() const
    {
        return baseUrl_;
    }

    bool MetaserverClient::IsAvailable() const
    {
        auto health = GetJson("/api/health");
        return health && health->is_object() && health->value("status", "") == "ok";
    }

    std::optional<nlohmann::json> MetaserverClient::GetPlayerLoadout(const std::string& playerId) const
    {
        return GetJson("/api/loadout/" + UrlEncodePathSegment(playerId));
    }

    std::optional<nlohmann::json> MetaserverClient::GetPlayerRoleLoadout(const std::string& playerId, const std::string& roleId) const
    {
        return GetJson("/api/loadout/" + UrlEncodePathSegment(playerId) + "/" + UrlEncodePathSegment(roleId));
    }

    std::optional<nlohmann::json> MetaserverClient::GetJson(const std::string& path) const
    {
        std::wstring host;
        INTERNET_PORT port = 0;
        std::wstring requestPath;
        bool secure = false;
        const std::string fullUrl = baseUrl_ + path;
        if (!CrackUrl(fullUrl, host, port, requestPath, secure)) return std::nullopt;

        WinHttpHandle session(WinHttpOpen(
            L"ProjectReboundPayload/1.0",
            WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
            WINHTTP_NO_PROXY_NAME,
            WINHTTP_NO_PROXY_BYPASS,
            0));
        if (!session) return std::nullopt;

        WinHttpSetTimeouts(session.Handle, 3000, 3000, 3000, 3000);

        WinHttpHandle connection(WinHttpConnect(session.Handle, host.c_str(), port, 0));
        if (!connection) return std::nullopt;

        const DWORD flags = secure ? WINHTTP_FLAG_SECURE : 0;
        WinHttpHandle request(WinHttpOpenRequest(
            connection.Handle,
            L"GET",
            requestPath.c_str(),
            nullptr,
            WINHTTP_NO_REFERER,
            WINHTTP_DEFAULT_ACCEPT_TYPES,
            flags));
        if (!request) return std::nullopt;

        static constexpr wchar_t acceptHeader[] = L"Accept: application/json\r\n";
        if (!WinHttpSendRequest(
            request.Handle,
            acceptHeader,
            static_cast<DWORD>(-1),
            WINHTTP_NO_REQUEST_DATA,
            0,
            0,
            0))
        {
            return std::nullopt;
        }
        if (!WinHttpReceiveResponse(request.Handle, nullptr)) return std::nullopt;

        DWORD status = 0;
        DWORD statusSize = sizeof(status);
        if (!WinHttpQueryHeaders(
            request.Handle,
            WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
            WINHTTP_HEADER_NAME_BY_INDEX,
            &status,
            &statusSize,
            WINHTTP_NO_HEADER_INDEX))
        {
            return std::nullopt;
        }
        if (status < 200 || status >= 300) return std::nullopt;

        std::string body;
        for (;;)
        {
            DWORD available = 0;
            if (!WinHttpQueryDataAvailable(request.Handle, &available)) return std::nullopt;
            if (available == 0) break;

            std::vector<char> buffer(available);
            DWORD read = 0;
            if (!WinHttpReadData(request.Handle, buffer.data(), available, &read)) return std::nullopt;
            body.append(buffer.data(), read);
        }

        auto parsed = nlohmann::json::parse(body, nullptr, false);
        if (parsed.is_discarded()) return std::nullopt;
        return parsed;
    }
}
