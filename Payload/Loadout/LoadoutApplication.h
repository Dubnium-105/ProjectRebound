#pragma once

// Game-thread-only application helpers for a validated, normalized loadout.

#include <string>
#include <utility>
#include <vector>

#include "../Libs/json.hpp"
#include "../SDK.hpp"

namespace LoadoutApplication
{
    using json = nlohmann::json;

    enum class ApplyResult
    {
        Pending,
        Applied,
        IdentityMismatch,
        Invalid,
    };

    enum class FieldModCacheState
    {
        ManagerMissing,
        RoleMissing,
        Match,
        Mismatch,
    };

    std::string ResolveCharacterRoleId(SDK::APBCharacter* character);
    std::string ResolveLiveCharacterRoleId(SDK::APBCharacter* character);

    SDK::APBPlayerController* GetLocalPlayerController();
    SDK::APBCharacter* GetLocalCharacter();
    SDK::APBPlayerController* FindPlayerControllerForCharacter(SDK::APBCharacter* character);
    SDK::APBCharacter* GetControllerCharacter(SDK::APBPlayerController* playerController);
    bool IsCharacterAlive(SDK::APBCharacter* character);
    SDK::UPBFieldModManager* GetFieldModManager();
    FieldModCacheState InspectFieldModCache(
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& expected);

    bool TryBuildRoleInventory(
        const json& snapshot,
        const std::string& roleId,
        SDK::FPBInventoryNetworkConfig& outInventory,
        std::string& outDetail);

    // Resolves the role quota from the native character definition table.
    // This keeps ClientInitFieldMod aligned with the exact game build rather
    // than duplicating DT_CharacterDefinition values in Payload.
    bool TryResolveRoleOwnedQuota(
        const std::string& roleId,
        int& outOwnedQuota,
        std::string& outDetail);

    ApplyResult PreSpawnApplyRole(
        const json& snapshot,
        const std::string& roleId,
        SDK::APBPlayerController* playerController,
        std::string& outDetail);

    // Reasserts an already validated runtime inventory immediately before a
    // spawn and verifies the authoritative FieldMod cache.
    ApplyResult PreSpawnApplyInventory(
        const std::string& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory,
        SDK::APBPlayerController* playerController,
        std::string& outDetail);

    // Builds the role's six-slot inventory from the authoritative character
    // definition asset and writes it through the same FieldMod path. This is
    // required when no metaserver/runtime loadout exists so an old shared
    // world+role cache cannot leak another player's inventory into the spawn.
    ApplyResult PreSpawnApplyNativeDefault(
        const std::string& roleId,
        SDK::APBPlayerController* playerController,
        std::string& outDetail);

    // Resolves the native role definition without retaining a controller.
    // Callers that track connection generations can revalidate the controller
    // after asset loading and before issuing ServerPreOrderInventory.
    ApplyResult TryBuildNativeDefaultInventory(
        const std::string& roleId,
        SDK::FPBInventoryNetworkConfig& outInventory,
        std::string& outDetail);

    // Compatibility wrapper retained for callers outside the new hook bridge.
    bool PreSpawnApply(
        const json& snapshot,
        SDK::APBPlayerController* preferredController,
        std::string& outDetail);
    void PushPreSpawnInventory(SDK::APBPlayerController* playerController);

    ApplyResult PostSpawnApply(
        SDK::APBCharacter* character,
        const json& snapshot,
        bool runtimeOverrideActive = false);

    bool ApplyLauncherConfig(
        SDK::APBLauncher* launcher,
        const SDK::FPBLauncherNetworkConfig& config,
        bool& outChanged);
    bool ApplyMeleeConfig(
        SDK::APBMeleeWeapon* meleeWeapon,
        const SDK::FPBMeleeWeaponNetworkConfig& config,
        bool& outChanged);
    bool ApplyMobilityConfig(
        SDK::APBCharacter* character,
        const SDK::FPBMobilityModuleNetworkConfig& config,
        bool& outChanged);

    SDK::APBWeapon* FindWeaponForConfig(
        SDK::APBCharacter* character,
        const SDK::FPBWeaponNetworkConfig& config,
        int preferredIndex);
    void RefreshWeaponRuntimeVisuals(SDK::APBWeapon* weapon);
    void MarkActorForReplication(SDK::AActor* actor);
}
