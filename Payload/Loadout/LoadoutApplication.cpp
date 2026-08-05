#include "LoadoutApplication.h"
#include "LoadoutSerializer.h"

#include <algorithm>
#include <sstream>
#include <string>
#include <unordered_set>
#include <vector>

#include "../Debug/Debug.h"

using namespace SDK;

extern std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
extern UObject* GetLastOfType(UClass* theClass, bool includeDefault);

namespace LoadoutApplication
{
    using namespace LoadoutSerializer;

    namespace
    {
        bool IsUsableName(const FName& name)
        {
            const std::string value = NameToString(name);
            return !value.empty() && value != "None";
        }

        bool SameName(const FName& left, const FName& right)
        {
            return NameToString(left) == NameToString(right);
        }

        bool HasCharacterAppearance(const FPBCharacterNetworkConfig& config)
        {
            if (IsUsableName(config.SkinPaintingID)) return true;
            for (int index = 0; index < config.SkinIDArray.Num(); ++index)
            {
                if (IsUsableName(config.SkinIDArray[index])) return true;
            }
            return false;
        }

        bool SameInventory(
            const FPBInventoryNetworkConfig& left,
            const FPBInventoryNetworkConfig& right)
        {
            if (left.CharacterSlots.Num() != right.CharacterSlots.Num() ||
                left.InventoryItems.Num() != right.InventoryItems.Num())
            {
                return false;
            }

            for (int i = 0; i < left.CharacterSlots.Num(); ++i)
            {
                if (left.CharacterSlots[i] != right.CharacterSlots[i] ||
                    !SameName(left.InventoryItems[i], right.InventoryItems[i]))
                {
                    return false;
                }
            }
            return true;
        }

        bool TryGetSlotItem(
            const FPBInventoryNetworkConfig& inventory,
            EPBCharacterSlotType slot,
            FName& outItem)
        {
            const int count = (std::min)(
                inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
            for (int i = 0; i < count; ++i)
            {
                if (inventory.CharacterSlots[i] == slot && IsUsableName(inventory.InventoryItems[i]))
                {
                    outItem = inventory.InventoryItems[i];
                    return true;
                }
            }
            return false;
        }

        bool RoleExists(const json& snapshot, const std::string& roleId)
        {
            if (!snapshot.is_object() || !snapshot.contains("roles") ||
                !snapshot["roles"].is_array())
            {
                return false;
            }
            for (const auto& role : snapshot["roles"])
            {
                if (role.is_object() && role.value("roleId", "") == roleId) return true;
            }
            return false;
        }

        FieldModCacheState InspectFieldModCacheInternal(
            const FName& roleId,
            const FPBInventoryNetworkConfig& expected)
        {
            UWorld* currentWorld = UWorld::GetWorld();
            if (!currentWorld) return FieldModCacheState::ManagerMissing;
            bool managerFound = false;
            bool roleFound = false;
            for (UObject* object : getObjectsOfClass(UPBFieldModManager::StaticClass(), false))
            {
                if (!object || object->IsDefaultObject()) continue;
                auto* manager = static_cast<UPBFieldModManager*>(object);
                if (manager->Outer != currentWorld) continue;
                managerFound = true;
                if (!manager->CharacterPreOrderingInventoryConfigs.IsValid()) continue;

                for (const auto& entry : manager->CharacterPreOrderingInventoryConfigs)
                {
                    if (!SameName(entry.Key(), roleId)) continue;
                    roleFound = true;
                    if (SameInventory(entry.Value(), expected))
                    {
                        return FieldModCacheState::Match;
                    }
                }
            }
            if (!managerFound) return FieldModCacheState::ManagerMissing;
            return roleFound
                ? FieldModCacheState::Mismatch
                : FieldModCacheState::RoleMissing;
        }

        ApplyResult MergeResult(ApplyResult current, ApplyResult next)
        {
            if (current == ApplyResult::Invalid || next == ApplyResult::Invalid)
                return ApplyResult::Invalid;
            if (current == ApplyResult::IdentityMismatch || next == ApplyResult::IdentityMismatch)
                return ApplyResult::IdentityMismatch;
            if (current == ApplyResult::Pending || next == ApplyResult::Pending)
                return ApplyResult::Pending;
            return ApplyResult::Applied;
        }

        const json* FindRoleJson(const json& snapshot, const std::string& roleId)
        {
            if (!snapshot.is_object() || !snapshot.contains("roles") ||
                !snapshot["roles"].is_array()) return nullptr;
            for (const auto& role : snapshot["roles"])
            {
                if (role.is_object() && role.value("roleId", "") == roleId)
                    return &role;
            }
            return nullptr;
        }

        const json* FindWeaponJson(const json* role, const FName& weaponId)
        {
            if (!role || !role->contains("weaponConfigs") ||
                !(*role)["weaponConfigs"].is_object()) return nullptr;
            const std::string id = NameToString(weaponId);
            const auto& configs = (*role)["weaponConfigs"];
            auto found = configs.find(id);
            return found != configs.end() && found->is_object() ? &*found : nullptr;
        }

        template <typename AssetType>
        UDataTable* ResolveDataTable(const TSoftObjectPtr<AssetType>& softTable)
        {
            if (auto* loaded = softTable.Get()) return loaded;

            // The generated SDK has no converting constructor between typed
            // TSoftObjectPtr specializations.  Copy the shared soft-object
            // base, then resolve it synchronously on the game thread.
            TSoftObjectPtr<UObject> generic{};
            static_cast<FSoftObjectPtr&>(generic) =
                static_cast<const FSoftObjectPtr&>(softTable);
            UObject* loaded = UKismetSystemLibrary::LoadAsset_Blocking(generic);
            return loaded && loaded->IsA(UDataTable::StaticClass())
                ? static_cast<UDataTable*>(loaded)
                : nullptr;
        }

        template <typename RowType>
        const RowType* FindDefinitionRow(
            UDataTable* table,
            const FName& rowId,
            const char* expectedRowStruct)
        {
            if (!table || !table->RowStruct ||
                table->RowStruct->GetName() != expectedRowStruct ||
                !table->RowMap.IsValid() || !IsUsableName(rowId)) return nullptr;
            for (const auto& entry : table->RowMap)
            {
                if (SameName(entry.Key(), rowId) && entry.Value())
                    return reinterpret_cast<const RowType*>(entry.Value());
            }
            return nullptr;
        }

        const json* FindPartJson(const json* weaponJson, EPBPartSlotType slot)
        {
            if (!weaponJson || !weaponJson->contains("parts") ||
                !(*weaponJson)["parts"].is_array()) return nullptr;
            for (const auto& part : (*weaponJson)["parts"])
            {
                if (part.is_object() && part.value("slotType", 0) == static_cast<int>(slot))
                    return &part;
            }
            return nullptr;
        }

        ApplyResult ExpandWeaponSuite(
            const json* weaponJson,
            FPBWeaponNetworkConfig& config)
        {
            if (!weaponJson) return ApplyResult::Applied;
            const std::string suiteId = weaponJson->value(
                "weaponSuitId", weaponJson->value("weaponSkinType", ""));
            const std::string paintingId = weaponJson->value(
                "weaponSuitPaintingId", weaponJson->value("weaponSkinId", ""));
            if (suiteId.empty() && !paintingId.empty()) return ApplyResult::Invalid;

            const FPBWeaponSuiteDefinitionRow* suite = nullptr;
            const FPBWeaponSuitePaintingDefinitionRow* painting = nullptr;
            if (!suiteId.empty())
            {
                UEngineSubsystem* subsystem = USubsystemBlueprintLibrary::GetEngineSubsystem(
                    UPBDataTableManager::StaticClass());
                if (!subsystem || !subsystem->IsA(UPBDataTableManager::StaticClass()))
                    return ApplyResult::Pending;
                auto* tables = static_cast<UPBDataTableManager*>(subsystem);
                UDataTable* suiteTable = ResolveDataTable(
                    tables->WeaponSuitDefinitionDataTable);
                UDataTable* paintingTable = paintingId.empty()
                    ? nullptr
                    : ResolveDataTable(tables->WeaponSuitPaintingDefinitionDataTable);
                if (!suiteTable || (!paintingId.empty() && !paintingTable))
                    return ApplyResult::Invalid;

                suite = FindDefinitionRow<FPBWeaponSuiteDefinitionRow>(
                    suiteTable, NameFromString(suiteId), "PBWeaponSuiteDefinitionRow");
                if (!suite) return ApplyResult::Invalid;

                if (!paintingId.empty())
                {
                    bool paintingBelongsToSuite = false;
                    const FName requestedPainting = NameFromString(paintingId);
                    for (const FName& allowed : suite->WeaponSuitPaintingIDArray)
                    {
                        if (SameName(allowed, requestedPainting))
                        {
                            paintingBelongsToSuite = true;
                            break;
                        }
                    }
                    if (!paintingBelongsToSuite) return ApplyResult::Invalid;

                    painting = FindDefinitionRow<FPBWeaponSuitePaintingDefinitionRow>(
                        paintingTable, requestedPainting,
                        "PBWeaponSuitePaintingDefinitionRow");
                    if (!painting) return ApplyResult::Invalid;
                }
            }

            const int count = (std::min)(
                config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
            for (int index = 0; index < count; ++index)
            {
                const EPBPartSlotType slot = config.WeaponPartSlotTypeArray[index];
                auto& part = config.WeaponPartConfigs[index];

                // A suite supplies the base appearance.  Explicit per-part
                // archive ornament fields override it; only an omitted field
                // falls through to the game's original appearance.
                part.WeaponPartSkinID = FName{};
                if (suite)
                {
                    for (const auto& entry : suite->PartSlotAndSkinIDMap)
                    {
                        if (entry.Key() == slot && IsUsableName(entry.Value()))
                        {
                            part.WeaponPartSkinID = entry.Value();
                            break;
                        }
                    }
                }

                part.WeaponPartSkinPaintingID = FName{};
                if (painting)
                {
                    for (const auto& entry : painting->SlotAndWeaponPartSkinPaintingIDMap)
                    {
                        if (entry.Key() == slot && IsUsableName(entry.Value()))
                        {
                            part.WeaponPartSkinPaintingID = entry.Value();
                            break;
                        }
                    }
                }

                if (const json* partJson = FindPartJson(weaponJson, slot))
                {
                    const std::string explicitSkin = partJson->value("weaponPartSkinId", "");
                    const std::string explicitPainting =
                        partJson->value("weaponPartSkinPaintingId", "");
                    if (!explicitSkin.empty())
                        part.WeaponPartSkinID = NameFromString(explicitSkin);
                    if (!explicitPainting.empty())
                        part.WeaponPartSkinPaintingID = NameFromString(explicitPainting);
                }

                if (!IsUsableName(part.WeaponPartSkinID))
                    part.WeaponPartSkinID = NameFromString("PartOri");
                if (!IsUsableName(part.WeaponPartSkinPaintingID))
                    part.WeaponPartSkinPaintingID = NameFromString("PTOriginal");
            }

            // The suite ornament is also a baseline.  A non-empty archive
            // weapon_ornament (including WO-NONE) is the player's explicit
            // override and therefore wins.
            config.OrnamentID = suite ? suite->WeaponOrnamentId : FName{};
            const std::string explicitOrnament = weaponJson->value("ornamentId", "");
            if (!explicitOrnament.empty())
                config.OrnamentID = NameFromString(explicitOrnament);
            return ApplyResult::Applied;
        }

        ApplyResult ApplyWeaponIfEffective(
            APBCharacter* character,
            const FPBWeaponNetworkConfig& target,
            EPBCharacterSlotType slot,
            const FPBInventoryNetworkConfig& effectiveInventory,
            int preferredIndex)
        {
            if (!HasWeaponConfig(target) || !IsUsableName(target.WeaponID))
                return ApplyResult::Applied;

            FName effectiveItem{};
            if (!TryGetSlotItem(effectiveInventory, slot, effectiveItem) ||
                !SameName(effectiveItem, target.WeaponID))
            {
                // A FieldMod inventory selected a different weapon. Its native
                // runtime/default config has precedence over the baseline.
                return ApplyResult::Applied;
            }

            APBWeapon* weapon = FindWeaponForConfig(character, target, preferredIndex);
            if (!weapon)
            {
                // Inventory actors are created incrementally. A missing target
                // slot, null actor, or not-yet-replicated ID is still Pending;
                // only a ready actor with a different valid identity is a
                // terminal mismatch for this spawn.
                if (preferredIndex < 0 || character->Inventory.Num() <= preferredIndex)
                    return ApplyResult::Pending;
                APBWeapon* preferred = character->Inventory[preferredIndex];
                if (!preferred || !IsUsableName(preferred->PartNetworkConfig.WeaponID))
                    return ApplyResult::Pending;
                return ApplyResult::IdentityMismatch;
            }

            FPBWeaponNetworkConfig applied = target;
            if (!IsUsableName(applied.OrnamentID))
                applied.OrnamentID = weapon->PartNetworkConfig.OrnamentID;
            weapon->InitWeapon(applied, false);
            RefreshWeaponRuntimeVisuals(weapon);
            MarkActorForReplication(weapon);
            return ApplyResult::Applied;
        }

        template <typename ConfigType>
        bool EffectiveSlotMatches(
            const FPBInventoryNetworkConfig& inventory,
            EPBCharacterSlotType slot,
            const ConfigType& config,
            const FName& configId)
        {
            (void)config;
            FName effectiveItem{};
            return TryGetSlotItem(inventory, slot, effectiveItem) && SameName(effectiveItem, configId);
        }
    }

    UPBFieldModManager* GetFieldModManager()
    {
        UWorld* currentWorld = UWorld::GetWorld();
        if (!currentWorld) return nullptr;
        for (UObject* object : getObjectsOfClass(UPBFieldModManager::StaticClass(), false))
        {
            if (object && !object->IsDefaultObject() && object->Outer == currentWorld)
                return static_cast<UPBFieldModManager*>(object);
        }
        return nullptr;
    }

    FieldModCacheState InspectFieldModCache(
        const FName& roleId,
        const FPBInventoryNetworkConfig& expected)
    {
        return InspectFieldModCacheInternal(roleId, expected);
    }

    APBPlayerController* GetLocalPlayerController()
    {
        UWorld* world = UWorld::GetWorld();
        if (!world || !world->OwningGameInstance) return nullptr;
        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* playerController = static_cast<APBPlayerController*>(object);
            if (playerController->PBGameInstance == world->OwningGameInstance) return playerController;
        }
        return nullptr;
    }

    APBCharacter* GetLocalCharacter()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        return playerController ? GetControllerCharacter(playerController) : nullptr;
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
            if (!object || object->IsDefaultObject()) continue;
            auto* playerController = static_cast<APBPlayerController*>(object);
            if (GetControllerCharacter(playerController) == character) return playerController;
        }
        return nullptr;
    }

    bool IsCharacterAlive(APBCharacter* character)
    {
        if (!character) return false;
        try { return character->IsAlive(); }
        catch (...) { return false; }
    }

    std::string ResolveCharacterRoleId(APBCharacter* character)
    {
        if (!character) return {};
        APBPlayerController* playerController = FindPlayerControllerForCharacter(character);
        if (playerController && playerController->PBPlayerState &&
            !IsBlankName(playerController->PBPlayerState->UsageCharacterID))
        {
            return NameToString(playerController->PBPlayerState->UsageCharacterID);
        }
        return !IsBlankName(character->CharacterID) ? NameToString(character->CharacterID) : "";
    }

    std::string ResolveLiveCharacterRoleId(APBCharacter* character)
    {
        if (!character) return {};
        if (!IsBlankName(character->CharacterID)) return NameToString(character->CharacterID);
        return ResolveCharacterRoleId(character);
    }

    bool TryBuildRoleInventory(
        const json& snapshot,
        const std::string& roleId,
        FPBInventoryNetworkConfig& outInventory,
        std::string& outDetail)
    {
        outInventory.CharacterSlots.Clear();
        outInventory.InventoryItems.Clear();

        if (roleId.empty() || !RoleExists(snapshot, roleId))
        {
            outDetail = "role-not-found";
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            outDetail = "role-config-invalid";
            return false;
        }

        const auto& source = roleConfig.InventoryData;
        if (source.CharacterSlots.Num() <= 0 ||
            source.CharacterSlots.Num() != source.InventoryItems.Num() ||
            source.CharacterSlots.Num() > 16)
        {
            outDetail = "inventory-array-invalid";
            return false;
        }

        std::unordered_set<int> seenSlots;
        for (int i = 0; i < source.CharacterSlots.Num(); ++i)
        {
            const auto slot = source.CharacterSlots[i];
            const int slotValue = static_cast<int>(slot);
            if (slotValue <= static_cast<int>(EPBCharacterSlotType::None) ||
                slotValue >= static_cast<int>(EPBCharacterSlotType::EPBCharacterSlotType_MAX) ||
                !IsUsableName(source.InventoryItems[i]) ||
                !seenSlots.insert(slotValue).second)
            {
                outDetail = "inventory-entry-invalid";
                outInventory.CharacterSlots.Clear();
                outInventory.InventoryItems.Clear();
                return false;
            }
            outInventory.CharacterSlots.Add(slot);
            outInventory.InventoryItems.Add(source.InventoryItems[i]);
        }

        outDetail = "inventory-valid";
        return true;
    }

    ApplyResult PreSpawnApplyInventory(
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory,
        APBPlayerController* playerController,
        std::string& outDetail)
    {
        if (!playerController)
        {
            outDetail = "target-controller-missing";
            return ApplyResult::Invalid;
        }

        const FName roleName = NameFromString(roleId);
        if (IsBlankName(roleName))
        {
            outDetail = "role-name-invalid";
            return ApplyResult::Invalid;
        }

        try
        {
            playerController->ServerPreOrderInventory(roleName, inventory);

            const FieldModCacheState cacheState =
                InspectFieldModCacheInternal(roleName, inventory);
            const bool verified = cacheState == FieldModCacheState::Match;
            const char* cacheDetail = "mismatch";
            switch (cacheState)
            {
            case FieldModCacheState::ManagerMissing: cacheDetail = "missing"; break;
            case FieldModCacheState::RoleMissing: cacheDetail = "role-missing"; break;
            case FieldModCacheState::Match: cacheDetail = "verified"; break;
            case FieldModCacheState::Mismatch: cacheDetail = "mismatch"; break;
            }
            std::ostringstream detail;
            detail << "role=" << roleId << ", slots=" << inventory.CharacterSlots.Num()
                   << ", fieldmod=" << cacheDetail;
            outDetail = detail.str();
            return verified ? ApplyResult::Applied : ApplyResult::Pending;
        }
        catch (...)
        {
            outDetail = "preorder-rpc-exception";
            return ApplyResult::Invalid;
        }
    }

    ApplyResult TryBuildNativeDefaultInventory(
        const std::string& roleId,
        FPBInventoryNetworkConfig& outInventory,
        std::string& outDetail)
    {
        outInventory.CharacterSlots.Clear();
        outInventory.InventoryItems.Clear();
        if (roleId.empty())
        {
            outDetail = "native-default-target-invalid";
            return ApplyResult::Invalid;
        }

        UEngineSubsystem* subsystem = USubsystemBlueprintLibrary::GetEngineSubsystem(
            UPBDataTableManager::StaticClass());
        if (!subsystem || !subsystem->IsA(UPBDataTableManager::StaticClass()))
        {
            outDetail = "native-default-table-manager-pending";
            return ApplyResult::Pending;
        }

        auto* tables = static_cast<UPBDataTableManager*>(subsystem);
        UDataTable* characterTable = ResolveDataTable(tables->CharacterDefinitionDataTable);
        if (!characterTable)
        {
            outDetail = "native-default-character-table-pending";
            return ApplyResult::Pending;
        }

        const auto* row = FindDefinitionRow<FPBCharacterDefinitionRow>(
            characterTable, NameFromString(roleId), "PBCharacterDefinitionRow");
        if (!row)
        {
            outDetail = "native-default-role-missing";
            return ApplyResult::Invalid;
        }

        UPBCharacterDefaultConfig* defaults = row->DefaultConfig.Get();
        if (!defaults)
        {
            TSoftObjectPtr<UObject> generic{};
            static_cast<FSoftObjectPtr&>(generic) =
                static_cast<const FSoftObjectPtr&>(row->DefaultConfig);
            UObject* loaded = UKismetSystemLibrary::LoadAsset_Blocking(generic);
            if (loaded && loaded->IsA(UPBCharacterDefaultConfig::StaticClass()))
                defaults = static_cast<UPBCharacterDefaultConfig*>(loaded);
        }
        if (!defaults)
        {
            outDetail = "native-default-asset-pending";
            return ApplyResult::Pending;
        }

        struct DefaultSlot
        {
            EPBCharacterSlotType Slot;
            const FName* Item;
        };
        const DefaultSlot slots[] = {
            { EPBCharacterSlotType::FirstWeapon, &defaults->FirstWeaponID },
            { EPBCharacterSlotType::SecondWeapon, &defaults->SecondWeaponID },
            { EPBCharacterSlotType::LeftPod, &defaults->LeftPodID },
            { EPBCharacterSlotType::RightPod, &defaults->RightPodID },
            { EPBCharacterSlotType::MeleeWeapon, &defaults->MeleeWeaponID },
            { EPBCharacterSlotType::Mobility, &defaults->MobilityID },
        };

        for (const DefaultSlot& slot : slots)
        {
            // Some native roles intentionally leave an optional pod/secondary
            // slot empty. Omitting that slot from the replacement config is
            // the engine representation of "none"; the remaining entries
            // must still be aligned and deterministic.
            if (!slot.Item || !IsUsableName(*slot.Item)) continue;
            outInventory.CharacterSlots.Add(slot.Slot);
            outInventory.InventoryItems.Add(*slot.Item);
        }
        if (outInventory.CharacterSlots.Num() <= 0)
        {
            outDetail = "native-default-inventory-empty";
            return ApplyResult::Invalid;
        }

        outDetail = "native-default-inventory-built";
        return ApplyResult::Applied;
    }

    ApplyResult PreSpawnApplyNativeDefault(
        const std::string& roleId,
        APBPlayerController* playerController,
        std::string& outDetail)
    {
        if (!playerController)
        {
            outDetail = "native-default-target-invalid";
            return ApplyResult::Invalid;
        }

        FPBInventoryNetworkConfig inventory{};
        const ApplyResult built =
            TryBuildNativeDefaultInventory(roleId, inventory, outDetail);
        if (built != ApplyResult::Applied) return built;

        return PreSpawnApplyInventory(roleId, inventory, playerController, outDetail);
    }

    ApplyResult PreSpawnApplyRole(
        const json& snapshot,
        const std::string& roleId,
        APBPlayerController* playerController,
        std::string& outDetail)
    {
        FPBInventoryNetworkConfig inventory{};
        if (!TryBuildRoleInventory(snapshot, roleId, inventory, outDetail))
            return ApplyResult::Invalid;

        return PreSpawnApplyInventory(roleId, inventory, playerController, outDetail);
    }

    bool PreSpawnApply(
        const json& snapshot,
        APBPlayerController* preferredController,
        std::string& outDetail)
    {
        if (!snapshot.is_object() || !snapshot.contains("roles") ||
            !snapshot["roles"].is_array() || snapshot["roles"].empty() ||
            !snapshot["roles"][0].is_object())
        {
            outDetail = "snapshot-role-empty";
            return false;
        }
        const std::string roleId = snapshot["roles"][0].value("roleId", "");
        return PreSpawnApplyRole(snapshot, roleId, preferredController, outDetail) == ApplyResult::Applied;
    }

    void PushPreSpawnInventory(APBPlayerController* playerController)
    {
        (void)playerController;
    }

    bool ApplyLauncherConfig(APBLauncher* launcher, const FPBLauncherNetworkConfig& config, bool& outChanged)
    {
        if (!HasLauncherConfig(config)) return true;
        if (!launcher || !SameName(launcher->SavedData.ID, config.ID)) return false;
        FPBLauncherNetworkConfig applied = config;
        if (!IsUsableName(applied.SkinID)) applied.SkinID = launcher->SavedData.SkinID;
        launcher->SavedData = applied;
        launcher->OnRep_SavedData();
        MarkActorForReplication(launcher);
        outChanged = true;
        return true;
    }

    bool ApplyMeleeConfig(APBMeleeWeapon* meleeWeapon, const FPBMeleeWeaponNetworkConfig& config, bool& outChanged)
    {
        if (!HasMeleeConfig(config)) return true;
        if (!meleeWeapon || !SameName(meleeWeapon->MeleeNetworkConfig.ID, config.ID)) return false;
        FPBMeleeWeaponNetworkConfig applied = config;
        if (!IsUsableName(applied.SkinID))
            applied.SkinID = meleeWeapon->MeleeNetworkConfig.SkinID;
        meleeWeapon->MeleeNetworkConfig = applied;
        meleeWeapon->OnRep_MeleeNetworkConfig();
        MarkActorForReplication(meleeWeapon);
        outChanged = true;
        return true;
    }

    bool ApplyMobilityConfig(APBCharacter* character, const FPBMobilityModuleNetworkConfig& config, bool& outChanged)
    {
        if (!HasMobilityConfig(config)) return true;
        if (!character || !character->CurrentMobilityModule ||
            !SameName(character->CurrentMobilityModule->SavedData.MobilityModuleID, config.MobilityModuleID))
        {
            return false;
        }
        character->CurrentMobilityModule->SavedData = config;
        character->OnRep_CurrentMobilityModule();
        MarkActorForReplication(character->CurrentMobilityModule);
        MarkActorForReplication(character);
        outChanged = true;
        return true;
    }

    ApplyResult PostSpawnApply(
        APBCharacter* character,
        const json& snapshot,
        bool runtimeOverrideActive)
    {
        if (!character) return ApplyResult::Invalid;

        try
        {
            const std::string roleId = ResolveLiveCharacterRoleId(character);
            FPBRoleNetworkConfig roleConfig{};
            if (roleId.empty() || !TryResolveRoleConfig(snapshot, roleId, roleConfig))
                return ApplyResult::Invalid;

            const FPBInventoryNetworkConfig& effective = roleConfig.InventoryData;
            if (effective.CharacterSlots.Num() != effective.InventoryItems.Num())
                return ApplyResult::Invalid;

            ApplyResult result = ApplyResult::Applied;
            bool changed = false;

            if (HasCharacterAppearance(roleConfig.CharacterData))
            {
                FPBCharacterNetworkConfig appearance = roleConfig.CharacterData;
                if (appearance.SkinIDArray.Num() == 0)
                {
                    appearance.SkinClassArray = character->CharacterSkinConfig.SkinClassArray;
                    appearance.SkinIDArray = character->CharacterSkinConfig.SkinIDArray;
                }
                if (!IsUsableName(appearance.SkinPaintingID))
                    appearance.SkinPaintingID = character->CharacterSkinConfig.SkinPaintingID;

                character->CharacterSkinConfig = appearance;
                character->OnRep_CharacterSkinConfig();
                if (auto* skinManager =
                    const_cast<UPBSkinManager*>(UPBSkinManager::GetPBSkinManager()))
                {
                    skinManager->RefreshCharacterSkin(character, appearance);
                }
                MarkActorForReplication(character);
                changed = true;
            }

            // An accepted in-match FieldMod override owns all equipment
            // details in that case; even an unchanged weapon ID may carry new
            // parts/skins, so applying baseline configs would violate runtime
            // precedence. Character cosmetics remain safe and were applied
            // above.
            if (runtimeOverrideActive) return ApplyResult::Applied;

            const json* roleJson = FindRoleJson(snapshot, roleId);
            const ApplyResult firstSuiteResult = ExpandWeaponSuite(
                FindWeaponJson(roleJson, roleConfig.FirstWeaponPartData.WeaponID),
                roleConfig.FirstWeaponPartData);
            const ApplyResult secondSuiteResult = ExpandWeaponSuite(
                FindWeaponJson(roleJson, roleConfig.SecondWeaponPartData.WeaponID),
                roleConfig.SecondWeaponPartData);
            result = MergeResult(result, firstSuiteResult);
            result = MergeResult(result, secondSuiteResult);

            // Never feed an unvalidated suite/painting into InitWeapon.  A
            // Pending table is retried; an invalid definition leaves the live
            // actor untouched while the remaining independently validated
            // equipment may still be applied.
            if (firstSuiteResult == ApplyResult::Applied)
            {
                result = MergeResult(result, ApplyWeaponIfEffective(
                    character, roleConfig.FirstWeaponPartData,
                    EPBCharacterSlotType::FirstWeapon, effective, 0));
            }
            if (secondSuiteResult == ApplyResult::Applied)
            {
                result = MergeResult(result, ApplyWeaponIfEffective(
                    character, roleConfig.SecondWeaponPartData,
                    EPBCharacterSlotType::SecondWeapon, effective, 1));
            }

            if (HasMeleeConfig(roleConfig.MeleeWeaponData) &&
                EffectiveSlotMatches(effective, EPBCharacterSlotType::MeleeWeapon,
                    roleConfig.MeleeWeaponData, roleConfig.MeleeWeaponData.ID))
            {
                if (!character->CurrentMeleeWeapon) result = MergeResult(result, ApplyResult::Pending);
                else if (!SameName(character->CurrentMeleeWeapon->MeleeNetworkConfig.ID,
                    roleConfig.MeleeWeaponData.ID))
                    result = MergeResult(result, ApplyResult::IdentityMismatch);
                else if (!ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed))
                    result = MergeResult(result, ApplyResult::Invalid);
            }

            auto applyLauncher = [&](APBLauncher* launcher,
                const FPBLauncherNetworkConfig& config,
                EPBCharacterSlotType slot)
            {
                if (!HasLauncherConfig(config) ||
                    !EffectiveSlotMatches(effective, slot, config, config.ID)) return;
                if (!launcher) result = MergeResult(result, ApplyResult::Pending);
                else if (!SameName(launcher->SavedData.ID, config.ID))
                    result = MergeResult(result, ApplyResult::IdentityMismatch);
                else if (!ApplyLauncherConfig(launcher, config, changed))
                    result = MergeResult(result, ApplyResult::Invalid);
            };
            applyLauncher(character->CurrentLeftLauncher, roleConfig.LeftLauncherData,
                EPBCharacterSlotType::LeftPod);
            applyLauncher(character->CurrentRightLauncher, roleConfig.RightLauncherData,
                EPBCharacterSlotType::RightPod);

            if (HasMobilityConfig(roleConfig.MobilityModuleData) &&
                EffectiveSlotMatches(effective, EPBCharacterSlotType::Mobility,
                    roleConfig.MobilityModuleData, roleConfig.MobilityModuleData.MobilityModuleID))
            {
                if (!character->CurrentMobilityModule) result = MergeResult(result, ApplyResult::Pending);
                else if (!SameName(character->CurrentMobilityModule->SavedData.MobilityModuleID,
                    roleConfig.MobilityModuleData.MobilityModuleID))
                    result = MergeResult(result, ApplyResult::IdentityMismatch);
                else if (!ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed))
                    result = MergeResult(result, ApplyResult::Invalid);
            }

            (void)changed;
            return result;
        }
        catch (...)
        {
            return ApplyResult::Invalid;
        }
    }

    APBWeapon* FindWeaponForConfig(
        APBCharacter* character,
        const FPBWeaponNetworkConfig& config,
        int preferredIndex)
    {
        if (!character || !IsUsableName(config.WeaponID)) return nullptr;
        if (preferredIndex >= 0 && preferredIndex < character->Inventory.Num())
        {
            APBWeapon* preferred = character->Inventory[preferredIndex];
            if (preferred && SameName(preferred->PartNetworkConfig.WeaponID, config.WeaponID))
                return preferred;
        }
        for (int i = 0; i < character->Inventory.Num(); ++i)
        {
            APBWeapon* weapon = character->Inventory[i];
            if (weapon && SameName(weapon->PartNetworkConfig.WeaponID, config.WeaponID)) return weapon;
        }
        return nullptr;
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
