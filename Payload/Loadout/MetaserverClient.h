#pragma once

// ======================================================
//  MetaserverClient - BoundaryMetaServer HTTP 客户端
// ======================================================
//
//  职责：
//    1. 为局内配装桥接提供最小 REST JSON 访问能力。
//    2. 只读取 metaserver 的 loadout 数据，不参与大厅原生 protobuf 流程。
//    3. 对外保持 playerId / roleId 级别接口，调用方负责归一化和应用。

#include <optional>
#include <string>

#include "../Libs/json.hpp"

namespace LoadoutMetaserver
{
    class MetaserverClient
    {
    public:
        explicit MetaserverClient(std::string baseUrl = "http://127.0.0.1:8000");

        // ---- 配置 / 状态 ----
        void SetBaseUrl(std::string baseUrl);
        const std::string& BaseUrl() const;

        bool IsAvailable() const;

        // ---- loadout REST 读取 ----
        std::optional<nlohmann::json> GetPlayerLoadout(const std::string& playerId) const;
        std::optional<nlohmann::json> GetPlayerRoleLoadout(const std::string& playerId, const std::string& roleId) const;
        bool PutPlayerLoadout(const std::string& playerId, const nlohmann::json& snapshot) const;

    private:
        // ---- 内部 HTTP + JSON 解析 ----
        std::optional<nlohmann::json> GetJson(const std::string& path) const;
        std::optional<nlohmann::json> RequestJson(
            const std::wstring& method,
            const std::string& path,
            const std::string* jsonBody) const;

        std::string baseUrl_;
    };
}
