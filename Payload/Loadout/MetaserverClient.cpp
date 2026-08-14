#include "MetaserverClient.h"

#include "LoadoutSerializer.h"

#include <Windows.h>
#include <winhttp.h>

#include <algorithm>
#include <cctype>
#include <limits>
#include <unordered_set>
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

        std::wstring ToWideUtf8(const std::string& value)
        {
            if (value.empty()) return {};
            const int count = MultiByteToWideChar(
                CP_UTF8, MB_ERR_INVALID_CHARS, value.data(),
                static_cast<int>(value.size()), nullptr, 0);
            if (count <= 0) return {};

            std::wstring result(static_cast<std::size_t>(count), L'\0');
            if (MultiByteToWideChar(
                CP_UTF8, MB_ERR_INVALID_CHARS, value.data(),
                static_cast<int>(value.size()), result.data(), count) != count)
            {
                return {};
            }
            return result;
        }

        std::string Trim(std::string value)
        {
            const auto isSpace = [](unsigned char ch) { return std::isspace(ch) != 0; };
            value.erase(value.begin(), std::find_if(value.begin(), value.end(), [&](char ch) {
                return !isSpace(static_cast<unsigned char>(ch));
            }));
            value.erase(std::find_if(value.rbegin(), value.rend(), [&](char ch) {
                return !isSpace(static_cast<unsigned char>(ch));
            }).base(), value.end());
            return value;
        }

        std::string NormalizeBaseUrl(std::string baseUrl)
        {
            baseUrl = Trim(std::move(baseUrl));
            if (baseUrl.size() >= 2 &&
                ((baseUrl.front() == '"' && baseUrl.back() == '"') ||
                    (baseUrl.front() == '\'' && baseUrl.back() == '\'')))
            {
                baseUrl = Trim(baseUrl.substr(1, baseUrl.size() - 2));
            }
            if (baseUrl.empty()) baseUrl = "http://127.0.0.1:8000";
            if (baseUrl.find("://") == std::string::npos) baseUrl = "http://" + baseUrl;
            while (!baseUrl.empty() && baseUrl.back() == '/') baseUrl.pop_back();
            return baseUrl;
        }

        std::string UrlEncodePathSegment(const std::string& value)
        {
            static constexpr char Hex[] = "0123456789ABCDEF";
            std::string result;
            result.reserve(value.size());
            for (const unsigned char ch : value)
            {
                const bool safe = std::isalnum(ch) != 0 || ch == '-' || ch == '_' || ch == '.' || ch == '~';
                if (safe)
                {
                    result.push_back(static_cast<char>(ch));
                    continue;
                }
                result.push_back('%');
                result.push_back(Hex[ch >> 4]);
                result.push_back(Hex[ch & 0x0F]);
            }
            return result;
        }

        bool CrackUrl(
            const std::string& url,
            std::wstring& host,
            INTERNET_PORT& port,
            std::wstring& path,
            bool& secure)
        {
            const std::wstring wideUrl = ToWideUtf8(url);
            if (wideUrl.empty()) return false;

            URL_COMPONENTS components{};
            components.dwStructSize = sizeof(components);
            components.dwSchemeLength = static_cast<DWORD>(-1);
            components.dwHostNameLength = static_cast<DWORD>(-1);
            components.dwUrlPathLength = static_cast<DWORD>(-1);
            components.dwExtraInfoLength = static_cast<DWORD>(-1);
            if (!WinHttpCrackUrl(wideUrl.c_str(), 0, 0, &components)) return false;
            if (components.nScheme != INTERNET_SCHEME_HTTP &&
                components.nScheme != INTERNET_SCHEME_HTTPS)
            {
                return false;
            }

            host.assign(components.lpszHostName, components.dwHostNameLength);
            port = components.nPort;
            secure = components.nScheme == INTERNET_SCHEME_HTTPS;
            path = L"/";
            if (components.lpszUrlPath && components.dwUrlPathLength > 0)
                path.assign(components.lpszUrlPath, components.dwUrlPathLength);
            if (components.lpszExtraInfo && components.dwExtraInfoLength > 0)
                path.append(components.lpszExtraInfo, components.dwExtraInfoLength);
            return !host.empty() && port != 0;
        }

        void SetTransportError(HttpResult& result, const char* message)
        {
            result.ErrorCode = HttpErrorCode::Transport;
            result.NativeError = GetLastError();
            result.ErrorMessage = message;
        }

        void ExtractEnvelopeMetadata(HttpResult& result)
        {
            if (!result.Body.is_object()) return;
            const auto& body = result.Body;
            if (body.contains("request_id") && body["request_id"].is_string())
                result.RequestId = body["request_id"].get<std::string>();
            if (!body.contains("error") || !body["error"].is_object()) return;

            const auto& error = body["error"];
            if (error.contains("code") && error["code"].is_string())
                result.ApiErrorCode = error["code"].get<std::string>();
            if (error.contains("message") && error["message"].is_string())
                result.ErrorMessage = error["message"].get<std::string>();
        }

        void SetDtoError(
            PlayerLoadoutsResult& result,
            HttpErrorCode code,
            std::string message)
        {
            result.Value.reset();
            result.Http.ErrorCode = code;
            result.Http.ErrorMessage = std::move(message);
        }

        bool ReadRequiredString(
            const nlohmann::json& object,
            const char* key,
            std::string& value)
        {
            if (!object.is_object() || !object.contains(key) || !object[key].is_string()) return false;
            value = object[key].get<std::string>();
            return !value.empty();
        }

        PlayerLoadoutsResult ParsePlayerLoadouts(
            HttpResult http,
            const std::string& expectedRoomId,
            const std::string& expectedPlayerId)
        {
            PlayerLoadoutsResult result;
            result.Http = std::move(http);
            if (!result.Http.Succeeded()) return result;

            try
            {
                if (!result.Http.Body.is_object() ||
                    !result.Http.Body.contains("data") ||
                    !result.Http.Body["data"].is_object())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "response data must be an object");
                    return result;
                }
                if (result.Http.RequestId.empty())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "response request_id is missing");
                    return result;
                }

                const auto& data = result.Http.Body["data"];
                if (!data.contains("schema_version") || !data["schema_version"].is_number_integer())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "data.schema_version must be an integer");
                    return result;
                }
                const int schemaVersion = data["schema_version"].get<int>();
                if (schemaVersion != 1)
                {
                    SetDtoError(result, HttpErrorCode::UnsupportedSchema, "unsupported loadout schema_version");
                    return result;
                }

                PlayerLoadoutsDto dto;
                dto.SchemaVersion = schemaVersion;
                if (!ReadRequiredString(data, "room_id", dto.RoomId) ||
                    !ReadRequiredString(data, "player_id", dto.PlayerId))
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "data room_id/player_id is missing");
                    return result;
                }
                if (dto.RoomId != expectedRoomId || dto.PlayerId != expectedPlayerId)
                {
                    SetDtoError(result, HttpErrorCode::IdentityMismatch, "response room_id/player_id does not match the request");
                    return result;
                }
                if (!data.contains("loadouts") || !data["loadouts"].is_array())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "data.loadouts must be an array");
                    return result;
                }
                if (data["loadouts"].size() > 64)
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope, "data.loadouts exceeds the role limit");
                    return result;
                }

                std::unordered_set<std::string> seenRoles;
                dto.Loadouts.reserve(data["loadouts"].size());
                for (const auto& entry : data["loadouts"])
                {
                    if (!entry.is_object())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout entry must be an object");
                        return result;
                    }

                    RoleLoadoutDto role;
                    if (!ReadRequiredString(entry, "role_id", role.RoleId) || role.RoleId.size() > 128)
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout role_id is invalid");
                        return result;
                    }
                    if (!seenRoles.insert(role.RoleId).second)
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout role_id is duplicated");
                        return result;
                    }
                    if (!entry.contains("revision") || !entry["revision"].is_number_integer())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout revision must be an integer");
                        return result;
                    }
                    role.Revision = entry["revision"].get<std::int64_t>();
                    if (role.Revision <= 0)
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout revision must be positive");
                        return result;
                    }
                    if (!entry.contains("snapshot") || !entry["snapshot"].is_object() ||
                        !entry.contains("weapon_configs") || !entry["weapon_configs"].is_object())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope, "loadout snapshot/weapon_configs must be objects");
                        return result;
                    }

                    std::string normalizeError;
                    if (!LoadoutSerializer::NormalizeMetaserverRole(
                        entry["snapshot"], entry["weapon_configs"], role.RoleId,
                        role.NormalizedRole, normalizeError))
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "invalid loadout for role " + role.RoleId + ": " + normalizeError);
                        return result;
                    }
                    dto.Loadouts.push_back(std::move(role));
                }

                result.Value = std::move(dto);
                return result;
            }
            catch (const std::exception& error)
            {
                SetDtoError(result, HttpErrorCode::InvalidEnvelope, error.what());
                return result;
            }
            catch (...)
            {
                SetDtoError(result, HttpErrorCode::InvalidEnvelope, "response DTO parsing failed");
                return result;
            }
        }

        PlayerLoadoutsResult ParseCurrentUserLoadouts(HttpResult http)
        {
            PlayerLoadoutsResult result;
            result.Http = std::move(http);
            if (!result.Http.Succeeded()) return result;

            try
            {
                if (!result.Http.Body.is_object() ||
                    !result.Http.Body.contains("data") ||
                    !result.Http.Body["data"].is_object() ||
                    !result.Http.Body["data"].contains("items") ||
                    !result.Http.Body["data"]["items"].is_array())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                        "response data.items must be an array");
                    return result;
                }
                if (result.Http.RequestId.empty())
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                        "response request_id is missing");
                    return result;
                }

                const auto& items = result.Http.Body["data"]["items"];
                if (items.size() > 64)
                {
                    SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                        "data.items exceeds the role limit");
                    return result;
                }

                PlayerLoadoutsDto dto;
                dto.SchemaVersion = 1;
                std::unordered_set<std::string> seenRoles;
                dto.Loadouts.reserve(items.size());

                for (const auto& entry : items)
                {
                    if (!entry.is_object())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "loadout entry must be an object");
                        return result;
                    }

                    std::string playerId;
                    RoleLoadoutDto role;
                    if (!ReadRequiredString(entry, "player_id", playerId) ||
                        playerId.size() < 3 || playerId.size() > 128 ||
                        playerId.rfind("p_", 0) != 0 ||
                        !ReadRequiredString(entry, "role_id", role.RoleId) ||
                        role.RoleId.size() > 128)
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "loadout player_id/role_id is invalid");
                        return result;
                    }
                    if (dto.PlayerId.empty()) dto.PlayerId = playerId;
                    if (dto.PlayerId != playerId)
                    {
                        SetDtoError(result, HttpErrorCode::IdentityMismatch,
                            "loadout entries contain multiple player IDs");
                        return result;
                    }
                    if (!seenRoles.insert(role.RoleId).second)
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "loadout role_id is duplicated");
                        return result;
                    }
                    if (!entry.contains("revision") ||
                        !entry["revision"].is_number_integer())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "loadout revision must be an integer");
                        return result;
                    }
                    role.Revision = entry["revision"].get<std::int64_t>();
                    if (role.Revision <= 0 || !entry.contains("snapshot") ||
                        !entry["snapshot"].is_object())
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "loadout revision/snapshot is invalid");
                        return result;
                    }

                    // The player list endpoint intentionally omits the large
                    // WeaponArchiveV2 documents. Supply definition-only
                    // archives for the two selected IDs; the native game
                    // archive remains authoritative for their detailed parts.
                    nlohmann::json definitionWeapons = nlohmann::json::object();
                    const auto addSelectedWeapon = [&](std::initializer_list<const char*> keys)
                    {
                        for (const char* key : keys)
                        {
                            if (!entry["snapshot"].contains(key)) continue;
                            const auto& selected = entry["snapshot"][key];
                            std::string weaponId;
                            if (selected.is_string())
                            {
                                weaponId = Trim(selected.get<std::string>());
                            }
                            else if (selected.is_object())
                            {
                                for (const char* idKey : {
                                    "id", "itemId", "item_id", "weaponId", "weapon_id" })
                                {
                                    if (selected.contains(idKey) && selected[idKey].is_string())
                                    {
                                        weaponId = Trim(selected[idKey].get<std::string>());
                                        break;
                                    }
                                }
                            }
                            if (!weaponId.empty() && weaponId != "None")
                                definitionWeapons[weaponId] = { { "weapon_id", weaponId } };
                            return;
                        }
                    };
                    addSelectedWeapon({ "primaryWeapon", "primary_weapon" });
                    addSelectedWeapon({
                        "secondaryWeapon", "secondary_weapon", "secondWeapon", "second_weapon" });

                    std::string normalizeError;
                    if (!LoadoutSerializer::NormalizeMetaserverRole(
                        entry["snapshot"], definitionWeapons, role.RoleId,
                        role.NormalizedRole, normalizeError))
                    {
                        SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                            "invalid loadout for role " + role.RoleId + ": " + normalizeError);
                        return result;
                    }
                    dto.Loadouts.push_back(std::move(role));
                }

                result.Value = std::move(dto);
                return result;
            }
            catch (const std::exception& error)
            {
                SetDtoError(result, HttpErrorCode::InvalidEnvelope, error.what());
                return result;
            }
            catch (...)
            {
                SetDtoError(result, HttpErrorCode::InvalidEnvelope,
                    "current-user loadout parsing failed");
                return result;
            }
        }
    }

    bool HttpResult::Succeeded() const
    {
        return ErrorCode == HttpErrorCode::None && StatusCode >= 200 && StatusCode < 300;
    }

    bool HttpResult::IsRetryable() const
    {
        if (ErrorCode == HttpErrorCode::Transport) return true;
        if (ErrorCode != HttpErrorCode::HttpStatus) return false;
        return StatusCode == 408 || StatusCode == 425 || StatusCode == 429 || StatusCode >= 500;
    }

    const RoleLoadoutDto* PlayerLoadoutsDto::FindRole(const std::string& roleId) const
    {
        const auto found = std::find_if(Loadouts.begin(), Loadouts.end(), [&](const RoleLoadoutDto& value) {
            return value.RoleId == roleId;
        });
        return found == Loadouts.end() ? nullptr : &*found;
    }

    nlohmann::json PlayerLoadoutsDto::ToNormalizedSnapshot() const
    {
        nlohmann::json result = {
            { "schemaVersion", SchemaVersion },
            { "source", RoomId.empty()
                ? "metaserver-current-user"
                : "metaserver-room-host" },
            { "playerId", PlayerId },
            { "roles", nlohmann::json::array() },
        };
        if (!RoomId.empty()) result["roomId"] = RoomId;
        for (const auto& role : Loadouts)
            result["roles"].push_back(role.NormalizedRole);
        return result;
    }

    bool PlayerLoadoutsResult::Succeeded() const
    {
        return Http.Succeeded() && Value.has_value();
    }

    bool PlayerLoadoutsResult::IsRetryable() const
    {
        return Http.IsRetryable();
    }

    MetaserverClient::MetaserverClient(std::string baseUrl)
    {
        SetBaseUrl(std::move(baseUrl));
    }

    void MetaserverClient::SetBaseUrl(std::string baseUrl)
    {
        std::lock_guard lock(baseUrlMutex_);
        baseUrl_ = NormalizeBaseUrl(std::move(baseUrl));
    }

    std::string MetaserverClient::BaseUrl() const
    {
        std::lock_guard lock(baseUrlMutex_);
        return baseUrl_;
    }

    PlayerLoadoutsResult MetaserverClient::GetRoomMemberLoadouts(
        const std::string& roomId,
        const std::string& playerId) const
    {
        if (roomId.empty() || roomId.size() > 128 ||
            playerId.size() < 3 || playerId.size() > 128 || playerId.rfind("p_", 0) != 0)
        {
            PlayerLoadoutsResult result;
            result.Http.ErrorCode = HttpErrorCode::InvalidArgument;
            result.Http.ErrorMessage = "roomId or canonical playerId is invalid";
            return result;
        }

        const std::string path =
            "/v1/meta/p2p-rooms/" + UrlEncodePathSegment(roomId) +
            "/members/" + UrlEncodePathSegment(playerId) + "/loadouts";
        return ParsePlayerLoadouts(RequestJson(path), roomId, playerId);
    }

    PlayerLoadoutsResult MetaserverClient::GetCurrentUserLoadouts() const
    {
        return ParseCurrentUserLoadouts(RequestJson("/v1/users/me/loadouts"));
    }

    HttpResult MetaserverClient::RequestJson(const std::string& path) const
    {
        HttpResult result;
        std::wstring host;
        std::wstring requestPath;
        INTERNET_PORT port = 0;
        bool secure = false;
        if (!CrackUrl(BaseUrl() + path, host, port, requestPath, secure))
        {
            result.ErrorCode = HttpErrorCode::InvalidBaseUrl;
            result.ErrorMessage = "LogicServerURL is not a valid HTTP URL";
            return result;
        }

        WinHttpHandle session(WinHttpOpen(
            L"ProjectReboundPayload/2.0",
            WINHTTP_ACCESS_TYPE_NO_PROXY,
            WINHTTP_NO_PROXY_NAME,
            WINHTTP_NO_PROXY_BYPASS,
            0));
        if (!session)
        {
            SetTransportError(result, "WinHttpOpen failed");
            return result;
        }
        WinHttpSetTimeouts(session.Handle, 3000, 3000, 3000, 3000);

        WinHttpHandle connection(WinHttpConnect(session.Handle, host.c_str(), port, 0));
        if (!connection)
        {
            SetTransportError(result, "WinHttpConnect failed");
            return result;
        }

        static const wchar_t* AcceptTypes[] = { L"application/json", nullptr };
        const DWORD flags = secure ? WINHTTP_FLAG_SECURE : 0;
        WinHttpHandle request(WinHttpOpenRequest(
            connection.Handle, L"GET", requestPath.c_str(), nullptr,
            WINHTTP_NO_REFERER, AcceptTypes, flags));
        if (!request)
        {
            SetTransportError(result, "WinHttpOpenRequest failed");
            return result;
        }

        // The configured origin is the local MetaTunnel trust boundary. Never
        // follow an upstream redirect or let WinHTTP synthesize credentials or
        // cookie state for a different origin.
        DWORD disabledFeatures =
            WINHTTP_DISABLE_REDIRECTS |
            WINHTTP_DISABLE_AUTHENTICATION |
            WINHTTP_DISABLE_COOKIES;
        if (!WinHttpSetOption(
            request.Handle, WINHTTP_OPTION_DISABLE_FEATURE,
            &disabledFeatures, sizeof(disabledFeatures)))
        {
            SetTransportError(result, "failed to disable WinHTTP redirect/auth state");
            return result;
        }

        // MetaTunnel injects the current player's bearer token. Payload must not
        // receive or synthesize an Authorization header.
        static constexpr wchar_t Headers[] =
            L"Accept: application/json\r\n"
            L"Cache-Control: no-cache\r\n";
        if (!WinHttpSendRequest(
            request.Handle, Headers, static_cast<DWORD>(-1),
            WINHTTP_NO_REQUEST_DATA, 0, 0, 0))
        {
            SetTransportError(result, "WinHttpSendRequest failed");
            return result;
        }
        if (!WinHttpReceiveResponse(request.Handle, nullptr))
        {
            SetTransportError(result, "WinHttpReceiveResponse failed");
            return result;
        }

        DWORD status = 0;
        DWORD statusSize = sizeof(status);
        if (!WinHttpQueryHeaders(
            request.Handle,
            WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
            WINHTTP_HEADER_NAME_BY_INDEX,
            &status, &statusSize, WINHTTP_NO_HEADER_INDEX))
        {
            SetTransportError(result, "HTTP status is unavailable");
            return result;
        }
        result.StatusCode = static_cast<int>(status);

        DWORD contentLength = 0;
        DWORD contentLengthSize = sizeof(contentLength);
        if (WinHttpQueryHeaders(
            request.Handle,
            WINHTTP_QUERY_CONTENT_LENGTH | WINHTTP_QUERY_FLAG_NUMBER,
            WINHTTP_HEADER_NAME_BY_INDEX,
            &contentLength, &contentLengthSize, WINHTTP_NO_HEADER_INDEX) &&
            contentLength > MaxResponseBytes)
        {
            result.ErrorCode = HttpErrorCode::ResponseTooLarge;
            result.ErrorMessage = "metaserver response exceeds 512 KiB";
            return result;
        }

        std::string body;
        if (contentLength > 0) body.reserve(contentLength);
        for (;;)
        {
            DWORD available = 0;
            if (!WinHttpQueryDataAvailable(request.Handle, &available))
            {
                SetTransportError(result, "WinHttpQueryDataAvailable failed");
                return result;
            }
            if (available == 0) break;
            if (available > MaxResponseBytes || body.size() > MaxResponseBytes - available)
            {
                result.ErrorCode = HttpErrorCode::ResponseTooLarge;
                result.ErrorMessage = "metaserver response exceeds 512 KiB";
                return result;
            }

            std::vector<char> buffer(available);
            DWORD read = 0;
            if (!WinHttpReadData(request.Handle, buffer.data(), available, &read))
            {
                SetTransportError(result, "WinHttpReadData failed");
                return result;
            }
            body.append(buffer.data(), read);
        }

        if (!body.empty())
        {
            result.Body = nlohmann::json::parse(body, nullptr, false);
            if (result.Body.is_discarded()) result.Body = nlohmann::json();
        }
        ExtractEnvelopeMetadata(result);

        if (status < 200 || status >= 300)
        {
            result.ErrorCode = HttpErrorCode::HttpStatus;
            if (result.ErrorMessage.empty())
                result.ErrorMessage = "metaserver returned HTTP " + std::to_string(status);
            return result;
        }
        if (body.empty() || result.Body.is_null())
        {
            result.ErrorCode = HttpErrorCode::InvalidJson;
            result.ErrorMessage = "metaserver returned invalid JSON";
            return result;
        }
        return result;
    }
}
