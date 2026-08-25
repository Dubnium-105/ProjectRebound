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

    enum class PlayerStateInventoryState
    {
        PlayerStateMissing,
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
    PlayerStateInventoryState InspectPlayerStateInventory(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& expected,
        bool equipping);

    // Resolves the exact FName value stored in the destination PlayerState.
    // Native FieldMod lookup compares the full eight-byte FName, so a string
    // round-trip is not an equivalent key on this pinned build.
    bool TryResolvePlayerStateInventoryRoleName(
        SDK::APBPlayerController* playerController,
        const std::string& roleId,
        SDK::FName& outRoleName);

    // The pinned build destroys two visible FieldMod configs and three native
    // sets with the source world but keeps PBPlayerState across seamless
    // travel. Restore their exact constructor headers at the owned destination
    // boundary; SeedSeamlessPlayerStateInventoryRoles then invokes the native
    // ClientInitFieldMod implementation body to reconstruct the live indices.
    bool NormalizeSeamlessPlayerStateInventoryContainers(
        SDK::APBPlayerController* playerController,
        std::string& outDetail);
    bool SeedSeamlessPlayerStateInventoryRoles(
        SDK::APBPlayerController* playerController,
        const std::vector<std::pair<
            std::string, const SDK::FPBInventoryNetworkConfig*>>& roles,
        std::string& outDetail);

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

    // Writes through the original server RPC and verifies the authoritative
    // per-player pre-ordering map. This function never writes PlayerState or
    // FieldMod containers directly.
    ApplyResult PreSpawnApplyInventory(
        const std::string& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory,
        SDK::APBPlayerController* playerController,
        std::string& outDetail,
        const SDK::FName* exactRoleName = nullptr);

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
        const SDK::FPBInventoryNetworkConfig& expectedInventory);

    SDK::APBWeapon* FindWeaponForConfig(
        SDK::APBCharacter* character,
        const SDK::FPBWeaponNetworkConfig& config,
        int preferredIndex);
    void MarkActorForReplication(SDK::AActor* actor);
}
