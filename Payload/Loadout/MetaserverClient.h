#pragma once

// Synchronous, side-effect-free HTTP/DTO boundary used by the server-side
// LoadoutManager. Callers are expected to invoke network methods on a worker
// thread; this class never touches Unreal objects.

#include <cstdint>
#include <mutex>
#include <optional>
#include <string>
#include <vector>

#include "../Libs/json.hpp"

namespace LoadoutMetaserver
{
    enum class HttpErrorCode
    {
        None,
        InvalidArgument,
        InvalidBaseUrl,
        Transport,
        ResponseTooLarge,
        InvalidJson,
        HttpStatus,
        InvalidEnvelope,
        UnsupportedSchema,
        IdentityMismatch,
    };

    struct HttpResult
    {
        int StatusCode = 0;
        HttpErrorCode ErrorCode = HttpErrorCode::None;
        unsigned long NativeError = 0;
        std::string ApiErrorCode;
        std::string RequestId;
        std::string ErrorMessage;
        nlohmann::json Body;

        bool Succeeded() const;
        bool IsRetryable() const;
    };

    struct RoleLoadoutDto
    {
        std::string RoleId;
        std::int64_t Revision = 0;

        // Existing structured role representation consumed by
        // LoadoutApplication: roleId, inventory, characterData,
        // weaponConfigs, meleeWeapon, launchers and mobilityModule.
        nlohmann::json NormalizedRole;
    };

    struct PlayerLoadoutsDto
    {
        int SchemaVersion = 0;
        std::string RoomId;
        std::string PlayerId;
        std::vector<RoleLoadoutDto> Loadouts;

        const RoleLoadoutDto* FindRole(const std::string& roleId) const;
        nlohmann::json ToNormalizedSnapshot() const;
    };

    struct PlayerLoadoutsResult
    {
        HttpResult Http;
        std::optional<PlayerLoadoutsDto> Value;

        bool Succeeded() const;
        bool IsRetryable() const;
    };

    class MetaserverClient
    {
    public:
        static constexpr std::size_t MaxResponseBytes = 512U * 1024U;

        explicit MetaserverClient(std::string baseUrl = "http://127.0.0.1:8000");

        void SetBaseUrl(std::string baseUrl);
        std::string BaseUrl() const;

        PlayerLoadoutsResult GetRoomMemberLoadouts(
            const std::string& roomId,
            const std::string& playerId) const;

        // Authenticated by the local MetaTunnel. This is the client-side
        // baseline used before the game's native FieldMod consumers run.
        PlayerLoadoutsResult GetCurrentUserLoadouts() const;

    private:
        HttpResult RequestJson(const std::string& path) const;

        mutable std::mutex baseUrlMutex_;
        std::string baseUrl_;
    };
}
