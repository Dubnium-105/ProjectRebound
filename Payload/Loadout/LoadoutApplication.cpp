// ======================================================
//  LoadoutApplication — 服务端权威应用 实现
// ======================================================

#include "LoadoutApplication.h"
#include "LoadoutSerializer.h"

#include <algorithm>
#include <string>
#include <vector>

#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Debug/Debug.h"

using namespace SDK;

// 前向声明 — UClass* 签名与 Debug.cpp 一致
extern std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
extern UObject* GetLastOfType(UClass* theClass, bool includeDefault);

// 全局变量声明（定义在 dllmain.cpp）
extern bool amServer;

namespace LoadoutApplication
{
    using namespace LoadoutSerializer;

    // =====================================================================
    //  对象查找
    // =====================================================================

    UPBFieldModManager* GetFieldModManager()
    {
        UObject* object = GetLastOfType(UPBFieldModManager::StaticClass(), false);
        return object ? static_cast<UPBFieldModManager*>(object) : nullptr;
        return object ? static_cast<UPBFieldModManager*>(object) : nullptr;
    }

    APBPlayerController* GetLocalPlayerController()
    {
        UWorld* world = UWorld::GetWorld();
        if (!world || !world->OwningGameInstance) return nullptr;
        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* pc = static_cast<APBPlayerController*>(object);
            if (pc && pc->PBGameInstance == world->OwningGameInstance) return pc;
        }
        return nullptr;
    }

    APBCharacter* GetLocalCharacter()
    {
        APBPlayerController* pc = GetLocalPlayerController();
        if (pc && pc->PBCharacter) return pc->PBCharacter;
        return nullptr;
    }

    APBCharacter* GetControllerCharacter(APBPlayerController* playerController)
    {
        if (!playerController) return nullptr;
        if (playerController->PBCharacter) return playerController->PBCharacter;
        if (playerController->Pawn && playerController->Pawn->IsA(APBCharacter::StaticClass()))
            return static_cast<APBCharacter*>(playerController->Pawn);
        return nullptr;
    }

    APBPlayerController* FindPlayerControllerForCharacter(APBCharacter* character)
    {
        if (!character) return nullptr;
        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* pc = static_cast<APBPlayerController*>(object);
            if (GetControllerCharacter(pc) == character) return pc;
        }
        return nullptr;
    }

    bool IsCharacterAlive(APBCharacter* character)
    {
        if (!character) return false;
        try { return character->IsAlive(); }
        catch (...) { return false; }
    }

    // =====================================================================
    //  角色 ID 解析
    // =====================================================================

    std::string ResolveCharacterRoleId(APBCharacter* character)
    {
        if (!character) return "";
        APBPlayerController* pc = FindPlayerControllerForCharacter(character);
        if (pc && pc->PBPlayerState)
        {
            APBPlayerState* ps = pc->PBPlayerState;
            if (!IsBlankName(ps->UsageCharacterID)) return NameToString(ps->UsageCharacterID);
        }
        if (!IsBlankName(character->CharacterID)) return NameToString(character->CharacterID);
        return "";
    }

    std::string ResolveLiveCharacterRoleId(APBCharacter* character)
    {
        if (!character) return "";
        if (!IsBlankName(character->CharacterID)) return NameToString(character->CharacterID);
        return ResolveCharacterRoleId(character);
    }

    // =====================================================================
    //  库存构建
    // =====================================================================

    std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> BuildInventoryListFromSnapshot(const json& snapshot)
    {
        std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> inventories;
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array()) return inventories;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;
            const std::string roleId = role.value("roleId", "");
            if (roleId.empty()) continue;
            FPBInventoryNetworkConfig inventory{};
            InventoryFromJson(role.value("inventory", EmptyInventoryJson()), inventory);
            inventories.push_back({ roleId, inventory });
        }
        return inventories;
    }

    // =====================================================================
    //  出生前库存推送
    // =====================================================================

    bool PreSpawnApply(const json& snapshot, APBPlayerController* preferredController, std::string& outDetail)
    {
        const auto inventories = BuildInventoryListFromSnapshot(snapshot);
        if (inventories.empty()) { outDetail = "snapshot-inventories-empty"; return false; }

        APBPlayerController* pc = preferredController;
        if (!pc) pc = GetLocalPlayerController();
        if (!pc) { outDetail = "target-player-controller-missing"; return false; }

        APBPlayerState* ps = pc->PBPlayerState;
        int pushed = 0, refreshed = 0;

        for (const auto& [roleId, inventory] : inventories)
        {
            const FName roleName = NameFromString(roleId);
            if (IsBlankName(roleName)) continue;
            pc->ServerPreOrderInventory(roleName, inventory);
            ++pushed;
            if (ps)
            {
                ps->ClientRefreshRolePreOrderingInventory(roleName, inventory);
                ps->ClientRefreshRoleEquippingInventory(roleName, inventory);
                ++refreshed;
            }
        }

        if (pushed <= 0) { outDetail = "no-valid-role-inventories"; return false; }
        outDetail = "controller=" + pc->GetFullName() + ", roles=" + std::to_string(pushed) +
            ", refreshed=" + std::to_string(refreshed) + (ps ? "" : ", playerState=missing");
        return true;
    }

    void PushPreSpawnInventory(APBPlayerController* playerController)
    {
        // 注意：实际库存推送由 LoadoutManager::OnRoleSelectionConfirmed 完成。
        // 此函数保留为兼容性简写。
        if (!playerController) return;
        if (!amServer) return;
    }

    // =====================================================================
    //  配装应用
    // =====================================================================

    bool ApplyLauncherConfig(APBLauncher* launcher, const FPBLauncherNetworkConfig& config, bool& outChanged)
    {
        if (!HasLauncherConfig(config)) return true;
        if (!launcher) return false;
        launcher->SavedData = config;
        launcher->OnRep_SavedData();
        MarkActorForReplication(launcher);
        outChanged = true;
        return true;
    }

    bool ApplyMeleeConfig(APBMeleeWeapon* meleeWeapon, const FPBMeleeWeaponNetworkConfig& config, bool& outChanged)
    {
        if (!HasMeleeConfig(config)) return true;
        if (!meleeWeapon) return false;
        meleeWeapon->MeleeNetworkConfig = config;
        meleeWeapon->OnRep_MeleeNetworkConfig();
        MarkActorForReplication(meleeWeapon);
        outChanged = true;
        return true;
    }

    bool ApplyMobilityConfig(APBCharacter* character, const FPBMobilityModuleNetworkConfig& config, bool& outChanged)
    {
        if (!HasMobilityConfig(config)) return true;
        if (!character || !character->CurrentMobilityModule) return false;
        character->CurrentMobilityModule->SavedData = config;
        character->OnRep_CurrentMobilityModule();
        MarkActorForReplication(character->CurrentMobilityModule);
        MarkActorForReplication(character);
        outChanged = true;
        return true;
    }

    // =====================================================================
    //  出生后实时应用
    // =====================================================================

    bool PostSpawnApply(APBCharacter* character, const json& snapshot)
    {
        if (!character) return false;

        try
        {
            FPBRoleNetworkConfig roleConfig{};
            const std::string roleId = ResolveLiveCharacterRoleId(character);
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) return false;

            // 查找角色 JSON
            const json* roleJson = nullptr;
            if (snapshot.contains("roles") && snapshot["roles"].is_array())
            {
                for (const auto& role : snapshot["roles"])
                {
                    if (role.is_object() && role.value("roleId", "") == roleId)
                    {
                        roleJson = &role;
                        break;
                    }
                }
            }

            bool changed = false;

            character->CharacterSkinConfig = roleConfig.CharacterData;
            character->OnRep_CharacterSkinConfig();
            if (auto* skinManager = const_cast<UPBSkinManager*>(UPBSkinManager::GetPBSkinManager()))
            {
                skinManager->RefreshCharacterSkin(character, roleConfig.CharacterData);
            }
            MarkActorForReplication(character);
            changed = true;

            // 武器配装
            const int inventoryCount = character->Inventory.Num();
            for (int i = 0; i < inventoryCount; ++i)
            {
                APBWeapon* weapon = character->Inventory[i];
                if (!weapon || !weapon->IsA(APBWeapon::StaticClass())) continue;

                const std::string liveWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
                const std::string liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);

                FPBWeaponNetworkConfig matchedConfig{};
                bool found = false;
                if (roleJson && !liveWeaponId.empty() && liveWeaponId != "None")
                    found = TryResolveWeaponConfigFromSnapshot(*roleJson, liveWeaponId, matchedConfig);
                if (!found && roleJson && !liveClassId.empty() && liveClassId != "None")
                    found = TryResolveWeaponConfigFromSnapshot(*roleJson, liveClassId, matchedConfig);

                if (found && HasWeaponConfig(matchedConfig))
                {
                    weapon->InitWeapon(matchedConfig, false);
                    RefreshWeaponRuntimeVisuals(weapon);
                    MarkActorForReplication(weapon);
                }
            }

            ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed);
            ApplyLauncherConfig(character->CurrentLeftLauncher, roleConfig.LeftLauncherData, changed);
            ApplyLauncherConfig(character->CurrentRightLauncher, roleConfig.RightLauncherData, changed);
            ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed);

            return true;
        }
        catch (...) { return false; }
    }

    // =====================================================================
    //  武器辅助
    // =====================================================================

    APBWeapon* FindWeaponForConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredIndex)
    {
        if (!character) return nullptr;
        const std::string configWeaponId = NameToString(config.WeaponID);
        const std::string configClassId = NameToString(config.WeaponClassID);
        const bool hasIdentity = !configWeaponId.empty() && configWeaponId != "None";

        APBWeapon* byIndex = nullptr;
        for (int i = 0; i < character->Inventory.Num(); ++i)
        {
            APBWeapon* weapon = character->Inventory[i];
            if (!weapon) continue;
            if (i == preferredIndex) byIndex = weapon;
            if (hasIdentity)
            {
                const std::string liveId = NameToString(weapon->PartNetworkConfig.WeaponID);
                const std::string liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);
                if (liveId == configWeaponId || liveClassId == configClassId || liveClassId == configWeaponId)
                    return weapon;
            }
        }
        if (byIndex && hasIdentity) return nullptr;
        return byIndex;
    }

    void RefreshWeaponRuntimeVisuals(APBWeapon* weapon)
    {
        if (!weapon) return;
        weapon->ApplyPartModification();
        weapon->K2_InitSimulatedPartsComplete();
        weapon->K2_RefreshSkin();
        weapon->NotifyRecalculateSpecialPartOffset();
        weapon->CalculateAimPointToSightSocketOffset();
    }

    void MarkActorForReplication(AActor* actor)
    {
        if (!actor) return;
        actor->ForceNetUpdate();
        actor->FlushNetDormancy();
    }
}
