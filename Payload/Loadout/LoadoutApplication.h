#pragma once

// ======================================================
//  LoadoutApplication — 服务端权威应用
// ======================================================
//  从 LoadoutManager.cpp 提取的服务端应用逻辑。
//  负责在角色生成前后权威地应用配装快照到游戏实体。

#include <string>
#include <vector>
#include <utility>

#include "../Libs/json.hpp"
#include "../SDK.hpp"

namespace LoadoutApplication
{
    using json = nlohmann::json;

    // ---- 角色 ID 解析 ----
    std::string ResolveCharacterRoleId(SDK::APBCharacter* character);
    std::string ResolveLiveCharacterRoleId(SDK::APBCharacter* character);

    // ---- 对象查找 ----
    SDK::APBPlayerController* GetLocalPlayerController();
    SDK::APBCharacter* GetLocalCharacter();
    SDK::APBPlayerController* FindPlayerControllerForCharacter(SDK::APBCharacter* character);
    SDK::APBCharacter* GetControllerCharacter(SDK::APBPlayerController* playerController);
    bool IsCharacterAlive(SDK::APBCharacter* character);
    SDK::UPBFieldModManager* GetFieldModManager();

    // ---- 库存构建 ----
    std::vector<std::pair<std::string, SDK::FPBInventoryNetworkConfig>> BuildInventoryListFromSnapshot(const json& snapshot);

    // ---- 出生前库存推送 ----
    bool PreSpawnApply(const json& snapshot, SDK::APBPlayerController* preferredController, std::string& outDetail);
    void PushPreSpawnInventory(SDK::APBPlayerController* playerController);

    // ---- 配装应用 ----
    bool ApplyLauncherConfig(SDK::APBLauncher* launcher, const SDK::FPBLauncherNetworkConfig& config, bool& outChanged);
    bool ApplyMeleeConfig(SDK::APBMeleeWeapon* meleeWeapon, const SDK::FPBMeleeWeaponNetworkConfig& config, bool& outChanged);
    bool ApplyMobilityConfig(SDK::APBCharacter* character, const SDK::FPBMobilityModuleNetworkConfig& config, bool& outChanged);

    // ---- 出生后实时应用 ----
    bool PostSpawnApply(SDK::APBCharacter* character, const json& snapshot);

    // ---- 武器辅助 ----
    SDK::APBWeapon* FindWeaponForConfig(SDK::APBCharacter* character, const SDK::FPBWeaponNetworkConfig& config, int preferredIndex);
    void RefreshWeaponRuntimeVisuals(SDK::APBWeapon* weapon);
    void MarkActorForReplication(SDK::AActor* actor);
}
