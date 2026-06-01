#pragma once

// ======================================================
//  LoadoutSerializer — JSON ↔ SDK 结构体互转
// ======================================================
//  从 LoadoutManager.cpp 提取的序列化/反序列化函数。
//  负责 SDK 网络配置结构体与 JSON 之间的双向转换。

#include <string>
#include <unordered_map>
#include <filesystem>

#include "../Libs/json.hpp"
#include "../SDK.hpp"

namespace LoadoutSerializer
{
    using json = nlohmann::json;

    // ---- 基础工具 ----
    std::wstring ToWide(const std::string& value);
    std::string NameToString(const SDK::FName& value);
    bool IsBlankText(const std::string& text);
    bool IsBlankName(const SDK::FName& value);
    SDK::FName NameFromString(const std::string& value);

    // ---- 空 JSON 工厂 ----
    json EmptyInventoryJson();
    json EmptyWeaponJson();
    json EmptyCharacterJson();
    json EmptyLauncherJson();
    json EmptyMeleeJson();
    json EmptyMobilityJson();
    json EmptyRoleJson(const std::string& roleId);

    // ---- JSON I/O ----
    bool ReadJsonFile(const std::filesystem::path& path, json& outJson);
    bool WriteJsonFile(const std::filesystem::path& path, const json& value);

    // ---- SDK → JSON ----
    json WeaponPartToJson(const SDK::FPBWeaponPartNetworkConfig& config, SDK::EPBPartSlotType slotType);
    json WeaponToJson(const SDK::FPBWeaponNetworkConfig& config);
    json CharacterToJson(const SDK::FPBCharacterNetworkConfig& config);
    json LauncherToJson(const SDK::FPBLauncherNetworkConfig& config);
    json MeleeToJson(const SDK::FPBMeleeWeaponNetworkConfig& config);
    json MobilityToJson(const SDK::FPBMobilityModuleNetworkConfig& config);
    json InventoryToJson(const SDK::FPBInventoryNetworkConfig& config);
    json RoleToJson(const SDK::FPBRoleNetworkConfig& config, const SDK::FPBInventoryNetworkConfig* inventoryOverride = nullptr);

    // ---- JSON → SDK ----
    bool InventoryFromJson(const json& value, SDK::FPBInventoryNetworkConfig& outConfig);
    bool CharacterFromJson(const json& value, SDK::FPBCharacterNetworkConfig& outConfig);
    bool WeaponFromJson(const json& value, SDK::FPBWeaponNetworkConfig& outConfig);
    bool LauncherFromJson(const json& value, SDK::FPBLauncherNetworkConfig& outConfig);
    bool MeleeFromJson(const json& value, SDK::FPBMeleeWeaponNetworkConfig& outConfig);
    bool MobilityFromJson(const json& value, SDK::FPBMobilityModuleNetworkConfig& outConfig);

    // ---- weaponConfigs map ↔ JSON ----
    json WeaponConfigsMapToJson(const std::unordered_map<std::string, SDK::FPBWeaponNetworkConfig>& weaponConfigs);
    bool WeaponConfigsMapFromJson(const json& value, std::unordered_map<std::string, SDK::FPBWeaponNetworkConfig>& outConfigs);

    // ---- 配置存在性判断 ----
    bool HasWeaponConfig(const SDK::FPBWeaponNetworkConfig& config);
    bool HasLauncherConfig(const SDK::FPBLauncherNetworkConfig& config);
    bool HasMeleeConfig(const SDK::FPBMeleeWeaponNetworkConfig& config);
    bool HasMobilityConfig(const SDK::FPBMobilityModuleNetworkConfig& config);

    // ---- 快照解析 ----
    bool TryResolveWeaponConfigFromSnapshot(const json& roleJson, const std::string& weaponId, SDK::FPBWeaponNetworkConfig& outConfig);
    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, SDK::FPBRoleNetworkConfig& outConfig);
    json ExtractSingleRoleFromSnapshot(const json& snapshot, const std::string& roleId);

    // ---- 库存查询 ----
    bool TryGetInventoryItemForSlot(const SDK::FPBInventoryNetworkConfig& inventory, SDK::EPBCharacterSlotType slotType, SDK::FName& outItemId);

    // ---- 自定义配装加载 ----
    json RoleFromCustomLoadout(const json& entry);
    bool LoadCustomLoadoutConfig(json& outSnapshot);
    std::filesystem::path GetCustomLoadoutPath();

    // ---- 格式适配（metaserver 新格式 → 结构化格式） ----
    // 检测是否为 metaserver 新 flat 格式（包含 primaryWeapon / _weaponArchiveRaw），
    // 若是则转换为结构化格式（inventory + weaponConfigs + characterData + ...）。
    // 若已是结构化格式则原样返回。
    json NormalizeLoadoutFormat(const json& loadoutOrRole);

    // ---- 武器存档 hex 解码 ----
    // 解码 _weaponArchiveRaw（hex 编码的 protobuf）为 { weaponId: { parts: [...] } } 映射。
    // 解码失败返回空 object。
    json DecodeWeaponArchiveRaw(const std::string& hexPayload);

    // ---- 皮肤 token 映射 ----
    // 将 _skinToken 映射到角色 JSON 的 characterData 字段。
    // 同时将 _ornamentId 应用到所有武器的 ornamentId。
    void ApplySkinAndOrnament(json& roleJson, const std::string& skinToken, const std::string& ornamentId);
}
