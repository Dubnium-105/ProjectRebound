// ======================================================
//  LoadoutSerializer — JSON ↔ SDK 实现
// ======================================================

#include "LoadoutSerializer.h"

#include <Windows.h>
#include <algorithm>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <string>
#include <unordered_map>

#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"

using namespace SDK;

namespace LoadoutSerializer
{
    // =====================================================================
    //  基础工具
    // =====================================================================

    std::wstring ToWide(const std::string& value)
    {
        return std::wstring(value.begin(), value.end());
    }

    std::string NameToString(const FName& value)
    {
        if (value.ComparisonIndex <= 0) return "None";
        if (value.ComparisonIndex > 10000000 || value.Number < 0 || value.Number > 1000000) return "None";
        const std::string text = value.ToString();
        return text.empty() ? "None" : text;
    }

    bool IsBlankText(const std::string& text)
    {
        return text.empty() || text == "None";
    }

    bool IsBlankName(const FName& value)
    {
        return value.ComparisonIndex <= 0;
    }

    FName NameFromString(const std::string& value)
    {
        if (value.empty()) return FName();
        const std::wstring wideValue = ToWide(value);
        return UKismetStringLibrary::Conv_StringToName(wideValue.c_str());
    }

    // =====================================================================
    //  空 JSON 工厂
    // =====================================================================

    json EmptyInventoryJson() { return json{ { "slots", json::array() } }; }
    json EmptyWeaponJson() { return json{ { "weaponId", "" }, { "weaponClassId", "" }, { "ornamentId", "" }, { "parts", json::array() } }; }
    json EmptyCharacterJson() { return json{ { "skinClassArray", json::array() }, { "skinIdArray", json::array() }, { "skinPaintingId", "" } }; }
    json EmptyLauncherJson() { return json{ { "id", "" }, { "skinId", "" } }; }
    json EmptyMeleeJson() { return json{ { "id", "" }, { "skinId", "" } }; }
    json EmptyMobilityJson() { return json{ { "mobilityModuleId", "" } }; }

    json EmptyRoleJson(const std::string& roleId)
    {
        return json{
            { "roleId", roleId },
            { "inventory", EmptyInventoryJson() },
            { "characterData", EmptyCharacterJson() },
            { "weaponConfigs", json::object() },
            { "meleeWeapon", EmptyMeleeJson() },
            { "leftLauncher", EmptyLauncherJson() },
            { "rightLauncher", EmptyLauncherJson() },
            { "mobilityModule", EmptyMobilityJson() }
        };
    }

    // =====================================================================
    //  JSON I/O
    // =====================================================================

    bool ReadJsonFile(const std::filesystem::path& path, json& outJson)
    {
        try
        {
            if (!std::filesystem::exists(path)) return false;
            std::ifstream file(path);
            if (!file.is_open()) return false;
            outJson = json::parse(file, nullptr, false);
            return !outJson.is_discarded() && outJson.is_object();
        }
        catch (...) { return false; }
    }

    bool WriteJsonFile(const std::filesystem::path& path, const json& value)
    {
        try
        {
            std::filesystem::create_directories(path.parent_path());
            std::ofstream file(path, std::ios::trunc);
            if (!file.is_open()) return false;
            file << value.dump(2);
            return true;
        }
        catch (...) { return false; }
    }

    // =====================================================================
    //  SDK → JSON
    // =====================================================================

    json WeaponPartToJson(const FPBWeaponPartNetworkConfig& config, EPBPartSlotType slotType)
    {
        return json{
            { "slotType", static_cast<int>(slotType) },
            { "weaponPartId", NameToString(config.WeaponPartID) },
            { "weaponPartSkinId", NameToString(config.WeaponPartSkinID) },
            { "weaponPartSpecialSkinId", NameToString(config.WeaponPartSpecialSkinID) },
            { "weaponPartSkinPaintingId", NameToString(config.WeaponPartSkinPaintingID) }
        };
    }

    json WeaponToJson(const FPBWeaponNetworkConfig& config)
    {
        json parts = json::array();
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int i = 0; i < count; ++i)
            parts.push_back(WeaponPartToJson(config.WeaponPartConfigs[i], config.WeaponPartSlotTypeArray[i]));
        return json{
            { "weaponId", NameToString(config.WeaponID) },
            { "weaponClassId", NameToString(config.WeaponClassID) },
            { "ornamentId", NameToString(config.OrnamentID) },
            { "parts", parts }
        };
    }

    json CharacterToJson(const FPBCharacterNetworkConfig& config)
    {
        json skinClasses = json::array();
        json skinIds = json::array();
        for (int i = 0; i < config.SkinClassArray.Num(); ++i)
            skinClasses.push_back(static_cast<int>(config.SkinClassArray[i]));
        for (int i = 0; i < config.SkinIDArray.Num(); ++i)
            skinIds.push_back(NameToString(config.SkinIDArray[i]));
        return json{
            { "skinClassArray", skinClasses },
            { "skinIdArray", skinIds },
            { "skinPaintingId", NameToString(config.SkinPaintingID) }
        };
    }

    json LauncherToJson(const FPBLauncherNetworkConfig& config)
    {
        return json{ { "id", NameToString(config.ID) }, { "skinId", NameToString(config.SkinID) } };
    }

    json MeleeToJson(const FPBMeleeWeaponNetworkConfig& config)
    {
        return json{ { "id", NameToString(config.ID) }, { "skinId", NameToString(config.SkinID) } };
    }

    json MobilityToJson(const FPBMobilityModuleNetworkConfig& config)
    {
        return json{ { "mobilityModuleId", NameToString(config.MobilityModuleID) } };
    }

    json InventoryToJson(const FPBInventoryNetworkConfig& config)
    {
        json slots = json::array();
        const int count = (std::min)(config.CharacterSlots.Num(), config.InventoryItems.Num());
        for (int i = 0; i < count; ++i)
        {
            slots.push_back(json{
                { "slotType", static_cast<int>(config.CharacterSlots[i]) },
                { "itemId", NameToString(config.InventoryItems[i]) }
            });
        }
        return json{ { "slots", slots } };
    }

    json RoleToJson(const FPBRoleNetworkConfig& config, const FPBInventoryNetworkConfig* inventoryOverride)
    {
        return json{
            { "roleId", NameToString(config.CharacterID) },
            { "inventory", InventoryToJson(inventoryOverride ? *inventoryOverride : config.InventoryData) },
            { "characterData", CharacterToJson(config.CharacterData) },
            { "weaponConfigs", json::object() },
            { "meleeWeapon", MeleeToJson(config.MeleeWeaponData) },
            { "leftLauncher", LauncherToJson(config.LeftLauncherData) },
            { "rightLauncher", LauncherToJson(config.RightLauncherData) },
            { "mobilityModule", MobilityToJson(config.MobilityModuleData) }
        };
    }

    // =====================================================================
    //  JSON → SDK
    // =====================================================================

    bool InventoryFromJson(const json& value, FPBInventoryNetworkConfig& outConfig)
    {
        outConfig.CharacterSlots.Clear();
        outConfig.InventoryItems.Clear();
        const json* slots = value.is_object() && value.contains("slots") ? &value["slots"] : nullptr;
        if (!slots || !slots->is_array()) return true;
        for (const auto& entry : *slots)
        {
            if (!entry.is_object()) continue;
            outConfig.CharacterSlots.Add(static_cast<EPBCharacterSlotType>(entry.value("slotType", 0)));
            outConfig.InventoryItems.Add(NameFromString(entry.value("itemId", "")));
        }
        return true;
    }

    bool CharacterFromJson(const json& value, FPBCharacterNetworkConfig& outConfig)
    {
        outConfig.SkinClassArray.Clear();
        outConfig.SkinIDArray.Clear();
        outConfig.SkinPaintingID = NameFromString(value.value("skinPaintingId", ""));
        if (value.contains("skinClassArray") && value["skinClassArray"].is_array())
            for (const auto& sc : value["skinClassArray"])
                outConfig.SkinClassArray.Add(static_cast<EPBSkinClass>(sc.get<int>()));
        if (value.contains("skinIdArray") && value["skinIdArray"].is_array())
            for (const auto& sid : value["skinIdArray"])
                outConfig.SkinIDArray.Add(NameFromString(sid.get<std::string>()));
        return true;
    }

    bool WeaponFromJson(const json& value, FPBWeaponNetworkConfig& outConfig)
    {
        outConfig.WeaponPartSlotTypeArray.Clear();
        outConfig.WeaponPartConfigs.Clear();
        outConfig.OrnamentID = NameFromString(value.value("ornamentId", ""));
        outConfig.WeaponID = NameFromString(value.value("weaponId", ""));
        outConfig.WeaponClassID = NameFromString(value.value("weaponClassId", ""));
        if (value.contains("parts") && value["parts"].is_array())
        {
            for (const auto& entry : value["parts"])
            {
                if (!entry.is_object()) continue;
                FPBWeaponPartNetworkConfig partConfig{};
                partConfig.WeaponPartID = NameFromString(entry.value("weaponPartId", ""));
                partConfig.WeaponPartSkinID = NameFromString(entry.value("weaponPartSkinId", ""));
                partConfig.WeaponPartSpecialSkinID = NameFromString(entry.value("weaponPartSpecialSkinId", ""));
                partConfig.WeaponPartSkinPaintingID = NameFromString(entry.value("weaponPartSkinPaintingId", ""));
                outConfig.WeaponPartSlotTypeArray.Add(static_cast<EPBPartSlotType>(entry.value("slotType", 0)));
                outConfig.WeaponPartConfigs.Add(partConfig);
            }
        }
        return true;
    }

    bool LauncherFromJson(const json& value, FPBLauncherNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MeleeFromJson(const json& value, FPBMeleeWeaponNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MobilityFromJson(const json& value, FPBMobilityModuleNetworkConfig& outConfig)
    {
        outConfig.MobilityModuleID = NameFromString(value.value("mobilityModuleId", ""));
        return true;
    }

    // =====================================================================
    //  weaponConfigs map ↔ JSON
    // =====================================================================

    json WeaponConfigsMapToJson(const std::unordered_map<std::string, FPBWeaponNetworkConfig>& weaponConfigs)
    {
        json result = json::object();
        for (const auto& [weaponId, config] : weaponConfigs)
        {
            if (weaponId.empty() || !HasWeaponConfig(config)) continue;
            result[weaponId] = WeaponToJson(config);
        }
        return result;
    }

    bool WeaponConfigsMapFromJson(const json& value, std::unordered_map<std::string, FPBWeaponNetworkConfig>& outConfigs)
    {
        if (!value.is_object()) return false;
        for (auto it = value.begin(); it != value.end(); ++it)
        {
            if (!it.value().is_object()) continue;
            FPBWeaponNetworkConfig config{};
            WeaponFromJson(it.value(), config);
            if (HasWeaponConfig(config)) outConfigs[it.key()] = config;
        }
        return true;
    }

    // =====================================================================
    //  配置存在性判断
    // =====================================================================

    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponID) || !IsBlankName(config.WeaponClassID) ||
            !IsBlankName(config.OrnamentID) || config.WeaponPartConfigs.Num() > 0;
    }

    bool HasLauncherConfig(const FPBLauncherNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMeleeConfig(const FPBMeleeWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMobilityConfig(const FPBMobilityModuleNetworkConfig& config)
    {
        return !IsBlankName(config.MobilityModuleID);
    }

    // =====================================================================
    //  快照解析
    // =====================================================================

    bool TryResolveWeaponConfigFromSnapshot(const json& role, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        const std::string configId = NameToString(NameFromString(weaponId));
        if (configId.empty() || configId == "None") return false;

        if (role.contains("weaponConfigs") && role["weaponConfigs"].is_object())
        {
            const json& wcs = role["weaponConfigs"];
            if (wcs.contains(configId) && wcs[configId].is_object())
            {
                WeaponFromJson(wcs[configId], outConfig);
                return HasWeaponConfig(outConfig);
            }
            for (auto it = wcs.begin(); it != wcs.end(); ++it)
            {
                if (!it.value().is_object()) continue;
                if (it.value().value("weaponClassId", "") == configId)
                {
                    WeaponFromJson(it.value(), outConfig);
                    return HasWeaponConfig(outConfig);
                }
            }
        }
        return false;
    }

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig)
    {
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array()) return false;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;
            if (role.value("roleId", "") == roleId)
            {
                outConfig.CharacterID = NameFromString(roleId);
                InventoryFromJson(role.value("inventory", EmptyInventoryJson()), outConfig.InventoryData);
                CharacterFromJson(role.value("characterData", EmptyCharacterJson()), outConfig.CharacterData);
                MeleeFromJson(role.value("meleeWeapon", EmptyMeleeJson()), outConfig.MeleeWeaponData);
                LauncherFromJson(role.value("leftLauncher", EmptyLauncherJson()), outConfig.LeftLauncherData);
                LauncherFromJson(role.value("rightLauncher", EmptyLauncherJson()), outConfig.RightLauncherData);
                MobilityFromJson(role.value("mobilityModule", EmptyMobilityJson()), outConfig.MobilityModuleData);
                return true;
            }
        }
        return false;
    }

    json ExtractSingleRoleFromSnapshot(const json& snapshot, const std::string& roleId)
    {
        json result;
        result["schemaVersion"] = snapshot.value("schemaVersion", 1);
        result["roles"] = json::array();
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array()) return result;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;
            if (role.value("roleId", "") == roleId)
            {
                result["roles"].push_back(role);
                return result;
            }
        }
        return result;  // 未找到
    }

    // =====================================================================
    //  库存查询
    // =====================================================================

    bool TryGetInventoryItemForSlot(const FPBInventoryNetworkConfig& inventory, EPBCharacterSlotType slotType, FName& outItemId)
    {
        const int count = (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
        for (int i = 0; i < count; ++i)
        {
            if (inventory.CharacterSlots[i] == slotType)
            {
                outItemId = inventory.InventoryItems[i];
                return !IsBlankName(outItemId);
            }
        }
        return false;
    }

    // =====================================================================
    //  自定义配装加载
    // =====================================================================

    std::filesystem::path GetAppDataRoot()
    {
        char* appData = nullptr;
        size_t len = 0;
        if (_dupenv_s(&appData, &len, "APPDATA") == 0 && appData && *appData)
        {
            const std::filesystem::path result = std::filesystem::path(appData) / "ProjectReboundBrowser";
            free(appData);
            return result;
        }
        free(appData);
        return std::filesystem::current_path() / "ProjectReboundBrowser";
    }

    std::filesystem::path GetCustomLoadoutPath()
    {
        return GetAppDataRoot() / "custom-loadout-v1.json";
    }

    json RoleFromCustomLoadout(const json& entry)
    {
        const std::string roleId = entry.value("roleId", "");
        json role = EmptyRoleJson(roleId);

        const std::string skinId = entry.value("skinId", "");
        const std::string skinPaintingId = entry.value("skinPaintingId", "");
        if (!skinId.empty()) { role["characterData"]["skinClassArray"] = json::array({ 0 }); role["characterData"]["skinIdArray"] = json::array({ skinId }); }
        if (!skinPaintingId.empty()) role["characterData"]["skinPaintingId"] = skinPaintingId;

        if (entry.contains("weapons") && entry["weapons"].is_array())
        {
            const auto& weapons = entry["weapons"];
            for (size_t wi = 0; wi < weapons.size() && wi < 2; ++wi)
            {
                const auto& w = weapons[wi];
                if (!w.is_object()) continue;
                json weaponJson = EmptyWeaponJson();
                const std::string itemId = w.value("itemId", "");
                weaponJson["weaponId"] = itemId;
                weaponJson["weaponClassId"] = w.value("weaponClassId", "");
                weaponJson["ornamentId"] = w.value("ornamentId", "WO-NONE");
                if (w.contains("parts") && w["parts"].is_object())
                {
                    json partsArray = json::array();
                    for (const auto& [slotKey, partId] : w["parts"].items())
                    {
                        if (!partId.is_string()) continue;
                        json part;
                        part["slotType"] = std::stoi(slotKey);
                        part["weaponPartId"] = partId.get<std::string>();
                        part["weaponPartSkinId"] = "PartOri";
                        part["weaponPartSkinPaintingId"] = "PTOriginal";
                        part["weaponPartSpecialSkinId"] = "None";
                        partsArray.push_back(part);
                    }
                    weaponJson["parts"] = partsArray;
                }
                if (!role.contains("weaponConfigs")) role["weaponConfigs"] = json::object();
                if (!itemId.empty())
                {
                    const std::string nk = NameToString(NameFromString(itemId));
                    if (!nk.empty() && nk != "None") role["weaponConfigs"][nk] = weaponJson;
                }
            }
        }

        auto setSimpleItem = [](json& target, const std::string& id, const std::string& idField = "id") {
            target[idField] = id;
            if (idField == "id") target["skinId"] = "";
        };
        const std::string meleeId = entry.value("meleeWeapon", "");
        if (!meleeId.empty() && meleeId != "None") setSimpleItem(role["meleeWeapon"], meleeId);
        const std::string leftId = entry.value("leftLauncher", "");
        if (!leftId.empty() && leftId != "None") setSimpleItem(role["leftLauncher"], leftId);
        const std::string rightId = entry.value("rightLauncher", "");
        if (!rightId.empty() && rightId != "None") setSimpleItem(role["rightLauncher"], rightId);
        const std::string mobilityId = entry.value("mobilityModule", "");
        if (!mobilityId.empty() && mobilityId != "None") setSimpleItem(role["mobilityModule"], mobilityId, "mobilityModuleId");

        json& slots = role["inventory"]["slots"];
        slots = json::array();
        auto pushSlot = [&](int slotType, const std::string& itemId) {
            if (!itemId.empty() && itemId != "None") slots.push_back({ { "slotType", slotType }, { "itemId", itemId } });
        };
        if (entry.contains("weapons") && entry["weapons"].is_array())
        {
            const auto& weapons = entry["weapons"];
            if (weapons.size() > 0 && weapons[0].is_object()) pushSlot(static_cast<int>(EPBCharacterSlotType::FirstWeapon), weapons[0].value("itemId", ""));
            if (weapons.size() > 1 && weapons[1].is_object()) pushSlot(static_cast<int>(EPBCharacterSlotType::SecondWeapon), weapons[1].value("itemId", ""));
        }
        pushSlot(static_cast<int>(EPBCharacterSlotType::LeftPod), leftId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::RightPod), rightId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::MeleeWeapon), meleeId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::Mobility), mobilityId);
        return role;
    }

    bool LoadCustomLoadoutConfig(json& outSnapshot)
    {
        const std::filesystem::path customPath = GetCustomLoadoutPath();
        json custom;
        if (!ReadJsonFile(customPath, custom)) return false;
        if (!custom.contains("loadouts") || !custom["loadouts"].is_array() || custom["loadouts"].empty()) return false;

        json snapshot;
        snapshot["schemaVersion"] = 1;
        snapshot["gameVersion"] = "unknown";
        snapshot["source"] = "custom-config";
        snapshot["roles"] = json::array();
        for (const auto& entry : custom["loadouts"])
        {
            if (!entry.is_object()) continue;
            const std::string roleId = entry.value("roleId", "");
            if (roleId.empty()) continue;
            snapshot["roles"].push_back(RoleFromCustomLoadout(entry));
        }
        if (snapshot["roles"].empty()) return false;
        outSnapshot = snapshot;
        return true;
    }

    // =====================================================================
    //  格式适配（metaserver 新 flat 格式 → 结构化格式）
    // =====================================================================

    static bool IsNewFlatRoleFormat(const json& role)
    {
        return role.contains("primaryWeapon") || role.contains("_weaponArchiveRaw");
    }

    static json NormalizeSingleRole(const json& role)
    {
        const std::string roleId = role.value("roleId", "");

        // 如果不是新格式，原样返回
        if (!IsNewFlatRoleFormat(role)) return role;

        // 解析 _weaponArchiveRaw → weaponConfigs
        json weaponConfigs = json::object();
        if (role.contains("_weaponArchiveRaw") && role["_weaponArchiveRaw"].is_string())
        {
            weaponConfigs = DecodeWeaponArchiveRaw(role["_weaponArchiveRaw"].get<std::string>());
        }

        // 构建 inventory slots
        json slots = json::array();
        auto pushSlot = [&](int slotType, const std::string& itemId) {
            if (!itemId.empty() && itemId != "None")
                slots.push_back({ { "slotType", slotType }, { "itemId", itemId } });
        };

        const std::string primary = role.value("primaryWeapon", "");
        const std::string secondary = role.value("secondaryWeapon", "");
        const std::string meleeId = role.value("meleeWeapon", "");
        const std::string leftId = role.value("leftPod", role.value("leftLauncher", ""));
        const std::string rightId = role.value("rightPod", role.value("rightLauncher", ""));
        const std::string mobilityId = role.value("mobilityModule", "");

        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::FirstWeapon), primary);
        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::SecondWeapon), secondary);
        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::MeleeWeapon), meleeId);
        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::LeftPod), leftId);
        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::RightPod), rightId);
        pushSlot(static_cast<int>(SDK::EPBCharacterSlotType::Mobility), mobilityId);

        // 确保 primaryWeapon / secondaryWeapon 的 weaponConfigs 键名与 inventory 中的一致
        // DecodeWeaponArchiveRaw 返回的键可能是 weaponId 格式，需要匹配
        json normalizedWeaponConfigs = json::object();
        for (auto& [key, wc] : weaponConfigs.items())
        {
            if (!wc.is_object()) continue;
            const std::string nk = NameToString(NameFromString(key));
            if (!nk.empty() && nk != "None")
            {
                if (!wc.contains("weaponId") || wc["weaponId"].get<std::string>().empty())
                    wc["weaponId"] = key;
                normalizedWeaponConfigs[nk] = wc;
            }
        }

        // 如果武器不在 weaponConfigs 中，创建空条目
        for (const auto& wid : { primary, secondary })
        {
            if (wid.empty() || wid == "None") continue;
            const std::string nk = NameToString(NameFromString(wid));
            if (nk.empty() || nk == "None") continue;
            if (!normalizedWeaponConfigs.contains(nk))
            {
                json wc = EmptyWeaponJson();
                wc["weaponId"] = nk;
                normalizedWeaponConfigs[nk] = wc;
            }
        }

        // 构建结果
        json result = EmptyRoleJson(roleId);
        result["inventory"]["slots"] = slots;
        result["weaponConfigs"] = normalizedWeaponConfigs;

        // melee / launcher / mobility
        if (!meleeId.empty() && meleeId != "None")
        {
            result["meleeWeapon"]["id"] = meleeId;
            result["meleeWeapon"]["skinId"] = "";
        }
        if (!leftId.empty() && leftId != "None")
        {
            result["leftLauncher"]["id"] = leftId;
            result["leftLauncher"]["skinId"] = "";
        }
        if (!rightId.empty() && rightId != "None")
        {
            result["rightLauncher"]["id"] = rightId;
            result["rightLauncher"]["skinId"] = "";
        }
        if (!mobilityId.empty() && mobilityId != "None")
        {
            result["mobilityModule"]["mobilityModuleId"] = mobilityId;
        }

        // 皮肤 token / ornament
        const std::string skinToken = role.value("_skinToken", "");
        const std::string ornamentId = role.value("_ornamentId", "");
        if (!skinToken.empty() || !ornamentId.empty())
        {
            ApplySkinAndOrnament(result, skinToken, ornamentId);
        }

        return result;
    }

    json NormalizeLoadoutFormat(const json& loadoutOrRole)
    {
        if (!loadoutOrRole.is_object()) return loadoutOrRole;

        // 情况 1: 包含 "roles" 键的完整 loadout
        if (loadoutOrRole.contains("roles"))
        {
            json normalized;
            normalized["schemaVersion"] = loadoutOrRole.value("schemaVersion", 2);
            normalized["savedAtUtc"] = loadoutOrRole.value("savedAtUtc", "");
            normalized["gameVersion"] = loadoutOrRole.value("gameVersion", "unknown");
            normalized["source"] = loadoutOrRole.value("source", "metaserver");
            normalized["roles"] = json::array();

            const auto& roles = loadoutOrRole["roles"];
            if (roles.is_object())
            {
                for (auto& [rid, roleData] : roles.items())
                {
                    if (!roleData.is_object()) continue;
                    json roleEntry = roleData;
                    if (!roleEntry.contains("roleId"))
                        roleEntry["roleId"] = rid;
                    normalized["roles"].push_back(NormalizeSingleRole(roleEntry));
                }
            }
            else if (roles.is_array())
            {
                for (const auto& role : roles)
                {
                    if (!role.is_object()) continue;
                    normalized["roles"].push_back(NormalizeSingleRole(role));
                }
            }
            return normalized;
        }

        // 情况 2: 单角色 — 可能是新 flat 格式或旧结构化格式
        return NormalizeSingleRole(loadoutOrRole);
    }

    // =====================================================================
    //  武器存档 hex 解码（最小 protobuf 解析器）
    // =====================================================================

    static uint32_t ReadVarint(const uint8_t* data, size_t size, size_t& offset, bool& ok)
    {
        uint32_t result = 0;
        int shift = 0;
        while (offset < size)
        {
            uint8_t byte = data[offset++];
            result |= (static_cast<uint32_t>(byte & 0x7F) << shift);
            if (!(byte & 0x80)) { ok = true; return result; }
            shift += 7;
        }
        ok = false;
        return 0;
    }

    static bool HexToBytes(const std::string& hex, std::vector<uint8_t>& outBytes)
    {
        if (hex.size() % 2 != 0) return false;
        outBytes.reserve(hex.size() / 2);
        for (size_t i = 0; i < hex.size(); i += 2)
        {
            const std::string byteStr = hex.substr(i, 2);
            char* end = nullptr;
            const long val = std::strtol(byteStr.c_str(), &end, 16);
            if (end != byteStr.c_str() + 2) return false;
            outBytes.push_back(static_cast<uint8_t>(val));
        }
        return true;
    }

    json DecodeWeaponArchiveRaw(const std::string& hexPayload)
    {
        json weaponConfigs = json::object();
        if (hexPayload.empty()) return weaponConfigs;

        std::vector<uint8_t> data;
        if (!HexToBytes(hexPayload, data)) return weaponConfigs;

        size_t offset = 0;
        const size_t size = data.size();

        // === 解析顶层 WeaponArchivePayload ===
        // 遍历顶层字段: 1=roleId, 2=weapons
        while (offset < size)
        {
            bool ok = false;
            const uint32_t tag = ReadVarint(data.data(), size, offset, ok);
            if (!ok) break;

            const uint32_t fieldNumber = tag >> 3;
            const uint32_t wireType = tag & 0x07;

            if (wireType == 2) // length-delimited
            {
                const uint32_t length = ReadVarint(data.data(), size, offset, ok);
                if (!ok || offset + length > size) break;

                if (fieldNumber == 2) // weapons (repeated WeaponConfig)
                {
                    // === 解析 WeaponConfig ===
                    size_t msgEnd = offset + length;
                    std::string weaponId;
                    json partsArray = json::array();

                    while (offset < msgEnd)
                    {
                        const uint32_t innerTag = ReadVarint(data.data(), size, offset, ok);
                        if (!ok) break;
                        const uint32_t innerField = innerTag >> 3;
                        const uint32_t innerWire = innerTag & 0x07;

                        if (innerWire == 2)
                        {
                            const uint32_t innerLen = ReadVarint(data.data(), size, offset, ok);
                            if (!ok || offset + innerLen > size) break;

                            if (innerField == 1) // weaponId
                            {
                                weaponId = std::string(reinterpret_cast<const char*>(data.data() + offset), innerLen);
                            }
                            else if (innerField == 2) // parts (repeated WeaponPartSlot)
                            {
                                // === 解析 WeaponPartSlot ===
                                size_t partEnd = offset + innerLen;
                                int slotIndex = -1;
                                std::string partId;

                                while (offset < partEnd)
                                {
                                    const uint32_t slotTag = ReadVarint(data.data(), size, offset, ok);
                                    if (!ok) break;
                                    const uint32_t slotField = slotTag >> 3;
                                    const uint32_t slotWire = slotTag & 0x07;

                                    if (slotWire == 0 && slotField == 1) // slotIndex (varint)
                                    {
                                        // already read the varint as tag... re-read
                                        // slotTag is the field tag, need to read value
                                        slotIndex = static_cast<int>(ReadVarint(data.data(), size, offset, ok));
                                        if (!ok) break;
                                    }
                                    else if (slotWire == 2)
                                    {
                                        const uint32_t slotLen = ReadVarint(data.data(), size, offset, ok);
                                        if (!ok || offset + slotLen > size) break;

                                        if (slotField == 2) // partId
                                        {
                                            partId = std::string(reinterpret_cast<const char*>(data.data() + offset), slotLen);
                                        }
                                        // field 3 = origin (PartOrigin message) — 跳过
                                        offset += slotLen;
                                    }
                                    else if (slotWire == 0)
                                    {
                                        // varint for non-field-1 (shouldn't happen for slotIndex which is field 1)
                                        ReadVarint(data.data(), size, offset, ok);
                                    }
                                    else if (slotWire == 5)
                                    {
                                        // 32-bit
                                        offset += 4;
                                    }
                                    else
                                    {
                                        break;
                                    }
                                }

                                if (slotIndex >= 0 && !partId.empty())
                                {
                                    json part;
                                    part["slotType"] = slotIndex;
                                    part["weaponPartId"] = partId;
                                    part["weaponPartSkinId"] = "PartOri";
                                    part["weaponPartSkinPaintingId"] = "PTOriginal";
                                    part["weaponPartSpecialSkinId"] = "None";
                                    partsArray.push_back(part);
                                }

                                offset = partEnd; // 确保偏移正确
                            }
                            else
                            {
                                offset += innerLen;
                            }
                        }
                        else if (innerWire == 0)
                        {
                            // varint, skip
                            ReadVarint(data.data(), size, offset, ok);
                        }
                        else if (innerWire == 5)
                        {
                            offset += 4;
                        }
                        else
                        {
                            break;
                        }
                    }

                    if (!weaponId.empty())
                    {
                        json weaponConfig = EmptyWeaponJson();
                        weaponConfig["weaponId"] = weaponId;
                        weaponConfig["parts"] = partsArray;
                        weaponConfigs[weaponId] = weaponConfig;
                    }

                    offset = msgEnd;
                }
                else
                {
                    // 其他 length-delimited 字段（如 roleId）— 跳过
                    offset += length;
                }
            }
            else if (wireType == 0)
            {
                // varint — 跳过
                ReadVarint(data.data(), size, offset, ok);
            }
            else if (wireType == 5)
            {
                offset += 4;
            }
            else
            {
                break;
            }
        }

        return weaponConfigs;
    }

    // =====================================================================
    //  皮肤 token / ornament 应用
    // =====================================================================

    void ApplySkinAndOrnament(json& roleJson, const std::string& skinToken, const std::string& ornamentId)
    {
        // 皮肤 token 映射到 characterData
        if (!skinToken.empty())
        {
            // token 格式: "PEACE_TOK-DOS-01" — 映射为 skinId
            if (!roleJson.contains("characterData"))
                roleJson["characterData"] = EmptyCharacterJson();
            auto& cd = roleJson["characterData"];
            if (!cd.contains("skinIdArray") || !cd["skinIdArray"].is_array() || cd["skinIdArray"].empty())
            {
                cd["skinIdArray"] = json::array({ skinToken });
            }
            if (cd.value("skinPaintingId", "").empty())
            {
                cd["skinPaintingId"] = skinToken;
            }
        }

        // ornament ID 应用到所有武器的 ornamentId
        if (!ornamentId.empty())
        {
            if (roleJson.contains("weaponConfigs") && roleJson["weaponConfigs"].is_object())
            {
                for (auto& [key, wc] : roleJson["weaponConfigs"].items())
                {
                    if (wc.is_object() && wc.value("ornamentId", "").empty())
                    {
                        wc["ornamentId"] = ornamentId;
                    }
                }
            }
        }
    }
}
