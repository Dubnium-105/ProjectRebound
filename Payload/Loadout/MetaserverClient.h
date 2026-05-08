#pragma once

#include <optional>
#include <string>

#include "../Libs/json.hpp"

namespace LoadoutMetaserver
{
    class MetaserverClient
    {
    public:
        explicit MetaserverClient(std::string baseUrl = "http://127.0.0.1:8000");

        void SetBaseUrl(std::string baseUrl);
        const std::string& BaseUrl() const;

        bool IsAvailable() const;
        std::optional<nlohmann::json> GetPlayerLoadout(const std::string& playerId) const;
        std::optional<nlohmann::json> GetPlayerRoleLoadout(const std::string& playerId, const std::string& roleId) const;

    private:
        std::optional<nlohmann::json> GetJson(const std::string& path) const;

        std::string baseUrl_;
    };
}
