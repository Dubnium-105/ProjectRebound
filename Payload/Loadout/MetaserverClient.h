#pragma once

// ======================================================
//  MetaserverClient — HTTP 客户端
// ======================================================
//  封装对 metaserver REST API 的 HTTP 调用（WinHTTP）。
//  提供物品定义查询（带缓存）和配装修验/过滤。

#include <string>
#include <optional>
#include <unordered_map>
#include <unordered_set>
#include <vector>
#include <mutex>

#include "../Libs/json.hpp"

// ---- 从 metaserver 返回的角色定义 ----
struct RoleDef {
    std::unordered_set<std::string> weaponScope;
    std::unordered_set<std::string> podScope;
    std::unordered_set<std::string> meleeWeaponScope;
    std::unordered_set<std::string> mobilityScope;
    std::string radarId;
    std::string vehicleId;
};

// ---- 从 metaserver 返回的武器槽位定义 ----
// slotName → validPartIds
using WeaponDef = std::unordered_map<std::string, std::unordered_set<std::string>>;

// ---- 配装修验结果 ----
struct ValidationResult {
    bool valid = true;
    std::vector<std::string> errors;
    std::vector<std::string> warnings;
};

class MetaserverClient {
public:
    explicit MetaserverClient(const std::string& baseUrl = "http://127.0.0.1:8000");

    // ---- 健康检查 ----
    bool IsAvailable();

    // ---- 定义查询（带缓存） ----
    std::optional<RoleDef> GetRole(const std::string& roleId);
    std::optional<WeaponDef> GetWeapon(const std::string& weaponId);
    std::optional<std::string> GetItemType(const std::string& itemId);

    // ---- 武器重定向 ----
    // baseWeaponId ("GSW-AR") + roleId ("PEACE") → roleWeaponId ("PEACE_GSW-AR") or nullopt
    std::optional<std::string> ResolveRoleWeaponId(const std::string& roleId, const std::string& baseWeaponId);

    // ---- 配装查询 ----
    std::optional<nlohmann::json> GetPlayerLoadout(const std::string& playerId);
    std::optional<nlohmann::json> GetPlayerRoleLoadout(const std::string& playerId, const std::string& roleId);

    // ---- 配装修验/过滤 ----
    ValidationResult ValidateLoadout(const nlohmann::json& loadout);
    nlohmann::json FilterLoadout(const nlohmann::json& loadout);

    // ---- 缓存控制 ----
    void ClearCache();

private:
    std::string baseUrl_;

    // 缓存
    std::unordered_map<std::string, RoleDef> roleCache_;
    std::unordered_map<std::string, WeaponDef> weaponCache_;
    std::mutex cacheMutex_;

    // HTTP 内部方法
    std::optional<nlohmann::json> HttpGet(const std::string& path);
    std::optional<nlohmann::json> HttpPost(const std::string& path, const nlohmann::json& body);

    // WinHTTP 底层
    std::optional<std::string> WinHttpRequest(const std::string& method, const std::string& path,
                                              const std::string* body = nullptr);

    // 辅助：从 JSON 解析 RoleDef / WeaponDef
    static RoleDef ParseRoleDef(const nlohmann::json& j);
    static WeaponDef ParseWeaponDef(const nlohmann::json& j);
};
