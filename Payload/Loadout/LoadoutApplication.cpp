#include "LoadoutApplication.h"
#include "LoadoutStatePolicy.h"
#include "LoadoutSerializer.h"

#include <algorithm>
#include <cstdint>
#include <cstring>
#include <iomanip>
#include <sstream>
#include <string>
#include <unordered_set>
#include <vector>

#include "../Debug/Debug.h"

using namespace SDK;

extern std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
extern UObject* GetLastOfType(UClass* theClass, bool includeDefault);
extern uintptr_t BaseAddress;

namespace LoadoutApplication
{
    using namespace LoadoutSerializer;

    namespace
    {
        constexpr std::ptrdiff_t FieldModPreOrderingOffset = 0x6C0;
        constexpr std::ptrdiff_t FieldModEquippingOffset = 0x6E0;
        constexpr std::ptrdiff_t FieldModExpandedRolesOffset = 0x700;
        constexpr std::ptrdiff_t FieldModAllowedRolesOffset = 0x750;
        constexpr std::ptrdiff_t FieldModOwnedQuotasOffset = 0x7A0;

        // These are the exact writes performed by APBPlayerState's constructor
        // for the pinned build's native TSet headers. Bytes +0x10/+0x18 are
        // allocator-owned inline state and are intentionally left untouched.
        void ResetNativeSetHeader(
            std::uint8_t* playerStateBytes,
            std::ptrdiff_t offset)
        {
            *reinterpret_cast<std::uint64_t*>(playerStateBytes + offset + 0x00) = 0;
            *reinterpret_cast<std::uint64_t*>(playerStateBytes + offset + 0x08) = 0;
            *reinterpret_cast<std::uint64_t*>(playerStateBytes + offset + 0x20) = 0;
            *reinterpret_cast<std::int32_t*>(playerStateBytes + offset + 0x28) = 0;
            *reinterpret_cast<std::int32_t*>(playerStateBytes + offset + 0x2C) = 0x80;
            *reinterpret_cast<std::int32_t*>(playerStateBytes + offset + 0x30) = -1;
            *reinterpret_cast<std::int32_t*>(playerStateBytes + offset + 0x34) = 0;
            *reinterpret_cast<std::uint64_t*>(playerStateBytes + offset + 0x40) = 0;
            *reinterpret_cast<std::int32_t*>(playerStateBytes + offset + 0x48) = 0;
        }

        bool IsNativeSetHeaderConstructorEmpty(
            const std::uint8_t* playerStateBytes,
            std::ptrdiff_t offset)
        {
            return
                *reinterpret_cast<const std::uint64_t*>(
                    playerStateBytes + offset + 0x00) == 0 &&
                *reinterpret_cast<const std::uint64_t*>(
                    playerStateBytes + offset + 0x08) == 0 &&
                *reinterpret_cast<const std::uint64_t*>(
                    playerStateBytes + offset + 0x20) == 0 &&
                *reinterpret_cast<const std::int32_t*>(
                    playerStateBytes + offset + 0x28) == 0 &&
                *reinterpret_cast<const std::int32_t*>(
                    playerStateBytes + offset + 0x2C) == 0x80 &&
                *reinterpret_cast<const std::int32_t*>(
                    playerStateBytes + offset + 0x30) == -1 &&
                *reinterpret_cast<const std::int32_t*>(
                    playerStateBytes + offset + 0x34) == 0 &&
                *reinterpret_cast<const std::uint64_t*>(
                    playerStateBytes + offset + 0x40) == 0 &&
                *reinterpret_cast<const std::int32_t*>(
                    playerStateBytes + offset + 0x48) == 0;
        }

        class ScopedRoleInventoryStorage
        {
        public:
            explicit ScopedRoleInventoryStorage(
                FPBFieldModRoleGameSavedNetworkConfig& saved)
                : saved_(saved)
            {
            }

            ~ScopedRoleInventoryStorage()
            {
                // Generated TArray frees the outer allocation only; release
                // the two nested arrays copied into each inventory value.
                for (auto& inventory : saved_.RoleInventoryNetworkConfigArray)
                {
                    inventory.CharacterSlots.Free();
                    inventory.InventoryItems.Free();
                }
            }

            ScopedRoleInventoryStorage(const ScopedRoleInventoryStorage&) = delete;
            ScopedRoleInventoryStorage& operator=(
                const ScopedRoleInventoryStorage&) = delete;

        private:
            FPBFieldModRoleGameSavedNetworkConfig& saved_;
        };

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

            std::vector<LoadoutStatePolicy::InventoryEntry> leftEntries;
            std::vector<LoadoutStatePolicy::InventoryEntry> rightEntries;
            leftEntries.reserve(left.CharacterSlots.Num());
            rightEntries.reserve(right.CharacterSlots.Num());
            for (int i = 0; i < left.CharacterSlots.Num(); ++i)
            {
                leftEntries.push_back({
                    static_cast<int>(left.CharacterSlots[i]),
                    NameToString(left.InventoryItems[i]),
                });
                rightEntries.push_back({
                    static_cast<int>(right.CharacterSlots[i]),
                    NameToString(right.InventoryItems[i]),
                });
            }
            return LoadoutStatePolicy::SameInventoryEntries(
                leftEntries, rightEntries);
        }

        bool SameWeaponConfig(
            const FPBWeaponNetworkConfig& left,
            const FPBWeaponNetworkConfig& right)
        {
            if (!SameName(left.WeaponID, right.WeaponID) ||
                !SameName(left.WeaponClassID, right.WeaponClassID) ||
                !SameName(left.OrnamentID, right.OrnamentID) ||
                left.WeaponPartSlotTypeArray.Num() != right.WeaponPartSlotTypeArray.Num() ||
                left.WeaponPartConfigs.Num() != right.WeaponPartConfigs.Num())
            {
                return false;
            }

            const int count = left.WeaponPartConfigs.Num();
            for (int index = 0; index < count; ++index)
            {
                const auto& leftPart = left.WeaponPartConfigs[index];
                const auto& rightPart = right.WeaponPartConfigs[index];
                if (left.WeaponPartSlotTypeArray[index] !=
                        right.WeaponPartSlotTypeArray[index] ||
                    !SameName(leftPart.WeaponPartID, rightPart.WeaponPartID) ||
                    !SameName(leftPart.WeaponPartSkinID, rightPart.WeaponPartSkinID) ||
                    !SameName(leftPart.WeaponPartSpecialSkinID,
                        rightPart.WeaponPartSpecialSkinID) ||
                    !SameName(leftPart.WeaponPartSkinPaintingID,
                        rightPart.WeaponPartSkinPaintingID))
                {
                    return false;
                }
            }
            return true;
        }

        std::string HashWeaponConfig(const FPBWeaponNetworkConfig& config)
        {
            std::uint64_t hash = 1469598103934665603ULL;
            const auto appendByte = [&hash](std::uint8_t value)
            {
                hash ^= value;
                hash *= 1099511628211ULL;
            };
            const auto appendText = [&appendByte](const std::string& value)
            {
                for (const unsigned char character : value) appendByte(character);
                appendByte(0xFF);
            };

            appendText(NameToString(config.WeaponID));
            appendText(NameToString(config.WeaponClassID));
            appendText(NameToString(config.OrnamentID));
            const int count = (std::min)(
                config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
            for (int index = 0; index < count; ++index)
            {
                appendByte(static_cast<std::uint8_t>(
                    config.WeaponPartSlotTypeArray[index]));
                const auto& part = config.WeaponPartConfigs[index];
                appendText(NameToString(part.WeaponPartID));
                appendText(NameToString(part.WeaponPartSkinID));
                appendText(NameToString(part.WeaponPartSpecialSkinID));
                appendText(NameToString(part.WeaponPartSkinPaintingID));
            }

            std::ostringstream output;
            output << std::hex << std::setw(16) << std::setfill('0') << hash;
            return output.str();
        }

        FPBWeaponNetworkConfig MergeWeaponConfig(
            const FPBWeaponNetworkConfig& live,
            const FPBWeaponNetworkConfig& target)
        {
            // WeaponArchiveV2 intentionally omits empty definition slots and
            // the runtime receiver slot. Start from the native instance so
            // those slots remain present, then overlay every archived slot.
            FPBWeaponNetworkConfig result = live;
            if (IsUsableName(target.WeaponID)) result.WeaponID = target.WeaponID;
            if (IsUsableName(target.WeaponClassID))
                result.WeaponClassID = target.WeaponClassID;
            if (IsUsableName(target.OrnamentID)) result.OrnamentID = target.OrnamentID;

            const int targetCount = (std::min)(
                target.WeaponPartSlotTypeArray.Num(), target.WeaponPartConfigs.Num());
            for (int targetIndex = 0; targetIndex < targetCount; ++targetIndex)
            {
                const EPBPartSlotType targetSlot =
                    target.WeaponPartSlotTypeArray[targetIndex];
                bool replaced = false;
                const int resultCount = (std::min)(
                    result.WeaponPartSlotTypeArray.Num(),
                    result.WeaponPartConfigs.Num());
                for (int resultIndex = 0; resultIndex < resultCount; ++resultIndex)
                {
                    if (result.WeaponPartSlotTypeArray[resultIndex] != targetSlot)
                        continue;
                    result.WeaponPartConfigs[resultIndex] =
                        target.WeaponPartConfigs[targetIndex];
                    replaced = true;
                    break;
                }
                if (!replaced)
                {
                    result.WeaponPartSlotTypeArray.Add(targetSlot);
                    result.WeaponPartConfigs.Add(target.WeaponPartConfigs[targetIndex]);
                }
            }
            return result;
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

        PlayerStateInventoryState InspectPlayerStateInventoryInternal(
            APBPlayerController* playerController,
            const FName& roleId,
            const FPBInventoryNetworkConfig& expected,
            bool equipping)
        {
            if (!playerController)
                return PlayerStateInventoryState::PlayerStateMissing;
            APBPlayerState* playerState = playerController->PBPlayerState;
            if (!playerState && playerController->PlayerState &&
                playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
            {
                playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
            }
            if (!playerState)
                return PlayerStateInventoryState::PlayerStateMissing;

            constexpr std::ptrdiff_t PreOrderingOffset = 0x6C0;
            constexpr std::ptrdiff_t EquippingOffset = 0x6E0;
            const auto mapOffset = equipping ? EquippingOffset : PreOrderingOffset;
            const auto* inventories =
                reinterpret_cast<const FPBFieldModRoleGameSavedNetworkConfig*>(
                reinterpret_cast<const std::uint8_t*>(playerState) + mapOffset);
            if (!inventories)
                return PlayerStateInventoryState::RoleMissing;

            const int roleCount = inventories->RoleArray.Num();
            const int inventoryCount =
                inventories->RoleInventoryNetworkConfigArray.Num();
            if (roleCount <= 0 || roleCount != inventoryCount || roleCount > 32 ||
                !inventories->RoleArray.IsValid() ||
                !inventories->RoleInventoryNetworkConfigArray.IsValid())
            {
                return PlayerStateInventoryState::RoleMissing;
            }
            for (int index = 0; index < roleCount; ++index)
            {
                if (inventories->RoleArray[index] != roleId) continue;
                return SameInventory(
                    inventories->RoleInventoryNetworkConfigArray[index], expected)
                    ? PlayerStateInventoryState::Match
                    : PlayerStateInventoryState::Mismatch;
            }
            return PlayerStateInventoryState::RoleMissing;
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
            const json* weaponJson,
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

            const std::string beforeHash = HashWeaponConfig(
                weapon->PartNetworkConfig);
            FPBWeaponNetworkConfig applied = MergeWeaponConfig(
                weapon->PartNetworkConfig, target);
            const ApplyResult expansionResult = ExpandWeaponSuite(
                weaponJson, applied);
            if (expansionResult != ApplyResult::Applied) return expansionResult;

            const std::string targetHash = HashWeaponConfig(applied);

            // InitWeapon only copies InPartSaved while the live
            // PartNetworkConfig has no WeaponClassID. Spawned match weapons
            // are already initialized, so calling InitWeapon(target) alone
            // simply rebuilds their default config. Write the replicated
            // property first and invoke its exact native RepNotify path.
            weapon->PartNetworkConfig = applied;
            weapon->OnRep_PartNetworkConfig();
            MarkActorForReplication(weapon);

            const std::string afterHash = HashWeaponConfig(
                weapon->PartNetworkConfig);
            const bool copied = SameWeaponConfig(
                weapon->PartNetworkConfig, applied);
            ClientLog("[LOADOUT] stage=weapon-detail-overlay slot=" +
                std::to_string(static_cast<int>(slot)) +
                " weapon=" + NameToString(applied.WeaponID) +
                " before_hash=" + beforeHash +
                " target_hash=" + targetHash +
                " after_hash=" + afterHash +
                " result=" + (copied ? "applied" : "copy-mismatch"));
            return copied ? ApplyResult::Applied : ApplyResult::Invalid;
        }

    }

    PlayerStateInventoryState InspectPlayerStateInventory(
        APBPlayerController* playerController,
        const FName& roleId,
        const FPBInventoryNetworkConfig& expected,
        bool equipping)
    {
        return InspectPlayerStateInventoryInternal(
            playerController, roleId, expected, equipping);
    }

    bool TryResolvePlayerStateInventoryRoleName(
        APBPlayerController* playerController,
        const std::string& roleId,
        FName& outRoleName)
    {
        if (!playerController || roleId.empty()) return false;
        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
        }
        if (!playerState) return false;

        const auto* config = reinterpret_cast<
            const FPBFieldModRoleGameSavedNetworkConfig*>(
                reinterpret_cast<const std::uint8_t*>(playerState) +
                FieldModPreOrderingOffset);
        const int roleCount = config->RoleArray.Num();
        if (roleCount <= 0 || roleCount > 32 ||
            roleCount != config->RoleInventoryNetworkConfigArray.Num() ||
            !config->RoleArray.IsValid() ||
            !config->RoleInventoryNetworkConfigArray.IsValid())
        {
            return false;
        }
        for (int index = 0; index < roleCount; ++index)
        {
            if (NameToString(config->RoleArray[index]) != roleId) continue;
            outRoleName = config->RoleArray[index];
            return true;
        }
        return false;
    }

    bool NormalizeSeamlessPlayerStateInventoryContainers(
        APBPlayerController* playerController,
        std::string& outDetail)
    {
        if (!playerController)
        {
            outDetail = "controller-missing";
            return false;
        }
        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
        }
        if (!playerState)
        {
            outDetail = "player-state-missing";
            return false;
        }

        static_assert(sizeof(FPBFieldModRoleGameSavedNetworkConfig) == 0x20);
        auto* const bytes = reinterpret_cast<std::uint8_t*>(playerState);

        // The source-world cleanup destructs the two visible FieldMod configs
        // and three adjacent native sets even though PlayerState survives the
        // seamless travel. Recreate the exact constructor state before asking
        // the pinned build's own ClientInitFieldMod body to populate them.
        std::memset(bytes + FieldModPreOrderingOffset, 0,
            sizeof(FPBFieldModRoleGameSavedNetworkConfig));
        std::memset(bytes + FieldModEquippingOffset, 0,
            sizeof(FPBFieldModRoleGameSavedNetworkConfig));
        ResetNativeSetHeader(bytes, FieldModExpandedRolesOffset);
        ResetNativeSetHeader(bytes, FieldModAllowedRolesOffset);
        ResetNativeSetHeader(bytes, FieldModOwnedQuotasOffset);
        outDetail = "fieldmod-containers-native-empty";
        return true;
    }

    bool SeedSeamlessPlayerStateInventoryRoles(
        APBPlayerController* playerController,
        const std::vector<std::pair<
            std::string, const FPBInventoryNetworkConfig*>>& roles,
        std::string& outDetail)
    {
        if (!playerController || roles.empty() || roles.size() > 32)
        {
            outDetail = "seed-input-invalid";
            return false;
        }

        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
        }
        if (!playerState)
        {
            outDetail = "seed-player-state-missing";
            return false;
        }

        for (const auto& [roleId, inventory] : roles)
        {
            if (roleId.empty() || roleId == "None" || !inventory)
            {
                outDetail = "seed-role-invalid";
                return false;
            }
        }

        static_assert(sizeof(FPBFieldModRoleGameSavedNetworkConfig) == 0x20);
        constexpr std::uint8_t emptyContainer[0x20]{};
        auto* const bytes = reinterpret_cast<std::uint8_t*>(playerState);

        // Never Clear or append to retired native storage. Only proceed from
        // the exact constructor state established at the destination boundary.
        for (const std::ptrdiff_t offset : {
            FieldModPreOrderingOffset, FieldModEquippingOffset})
        {
            if (std::memcmp(bytes + offset, emptyContainer,
                sizeof(emptyContainer)) != 0)
            {
                outDetail = "seed-containers-not-default-empty";
                return false;
            }
        }
        if (!IsNativeSetHeaderConstructorEmpty(
                bytes, FieldModExpandedRolesOffset) ||
            !IsNativeSetHeaderConstructorEmpty(
                bytes, FieldModAllowedRolesOffset) ||
            !IsNativeSetHeaderConstructorEmpty(
                bytes, FieldModOwnedQuotasOffset))
        {
            outDetail = "seed-native-sets-not-constructor-empty";
            return false;
        }

        try
        {
            FPBFieldModRoleGameSavedNetworkConfig saved{};
            ScopedRoleInventoryStorage releaseNestedStorage(saved);
            TArray<FName> roleIds;
            TArray<int32> ownedQuotas;
            for (const auto& [roleId, inventory] : roles)
            {
                int ownedQuota = 0;
                std::string quotaDetail;
                if (!TryResolveRoleOwnedQuota(
                        roleId, ownedQuota, quotaDetail))
                {
                    outDetail = roleId + ": " + quotaDetail;
                    return false;
                }

                const FName roleName = NameFromString(roleId);
                saved.RoleArray.Add(roleName);
                saved.RoleInventoryNetworkConfigArray.AddZeroed(*inventory);
                roleIds.Add(roleName);
                ownedQuotas.Add(ownedQuota);
            }

            const int roleCount = static_cast<int>(roles.size());
            if (saved.RoleArray.Num() != roleCount ||
                saved.RoleInventoryNetworkConfigArray.Num() != roleCount ||
                roleIds.Num() != roleCount || ownedQuotas.Num() != roleCount)
            {
                outDetail = "seed-array-alignment-failed";
                return false;
            }

            // RVA 0x165D130 is the implementation body, not the RPC thunk.
            // Calling the body on authority reconstructs +0x700/+0x7A0 in the
            // same way as the game's original client initialization without
            // forwarding the call back over the network.
            using ClientInitFieldModNativeFn = void(__fastcall*)(
                APBPlayerState*,
                const FPBFieldModRoleGameSavedNetworkConfig&,
                const TArray<FName>&,
                const TArray<int32>&);
            const auto clientInitFieldMod =
                reinterpret_cast<ClientInitFieldModNativeFn>(
                    BaseAddress + 0x165D130);
            clientInitFieldMod(playerState, saved, roleIds, ownedQuotas);

            const auto* preOrdering = reinterpret_cast<
                const FPBFieldModRoleGameSavedNetworkConfig*>(
                    bytes + FieldModPreOrderingOffset);
            const auto* equipping = reinterpret_cast<
                const FPBFieldModRoleGameSavedNetworkConfig*>(
                    bytes + FieldModEquippingOffset);
            const int expandedRoleCount =
                *reinterpret_cast<const std::int32_t*>(
                    bytes + FieldModExpandedRolesOffset + 0x08);
            const int quotaCount =
                *reinterpret_cast<const std::int32_t*>(
                    bytes + FieldModOwnedQuotasOffset + 0x08);
            if (preOrdering->RoleArray.Num() != roleCount ||
                equipping->RoleArray.Num() != roleCount ||
                expandedRoleCount != roleCount || quotaCount != roleCount)
            {
                outDetail = "native-client-init-fieldmod-verify-failed";
                return false;
            }
        }
        catch (...)
        {
            outDetail = "seed-container-exception";
            return false;
        }

        outDetail = "native-client-init-fieldmod-applied";
        return true;
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
            const std::string itemId = NameToString(source.InventoryItems[i]);
            if (slotValue <= static_cast<int>(EPBCharacterSlotType::None) ||
                slotValue >= static_cast<int>(EPBCharacterSlotType::EPBCharacterSlotType_MAX) ||
                itemId.empty() ||
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

    bool TryResolveRoleOwnedQuota(
        const std::string& roleId,
        int& outOwnedQuota,
        std::string& outDetail)
    {
        outOwnedQuota = 0;
        if (roleId.empty())
        {
            outDetail = "role-quota-target-invalid";
            return false;
        }

        UEngineSubsystem* subsystem = USubsystemBlueprintLibrary::GetEngineSubsystem(
            UPBDataTableManager::StaticClass());
        if (!subsystem || !subsystem->IsA(UPBDataTableManager::StaticClass()))
        {
            outDetail = "role-quota-table-manager-pending";
            return false;
        }

        auto* tables = static_cast<UPBDataTableManager*>(subsystem);
        UDataTable* characterTable = ResolveDataTable(tables->CharacterDefinitionDataTable);
        if (!characterTable)
        {
            outDetail = "role-quota-character-table-pending";
            return false;
        }

        const auto* row = FindDefinitionRow<FPBCharacterDefinitionRow>(
            characterTable, NameFromString(roleId), "PBCharacterDefinitionRow");
        if (!row || row->OwnedQuota <= 0)
        {
            outDetail = "role-quota-definition-invalid";
            return false;
        }

        outOwnedQuota = row->OwnedQuota;
        outDetail = "role-quota-valid";
        return true;
    }

    ApplyResult PreSpawnApplyInventory(
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory,
        APBPlayerController* playerController,
        std::string& outDetail,
        const FName* exactRoleName)
    {
        if (!playerController)
        {
            outDetail = "target-controller-missing";
            return ApplyResult::Invalid;
        }

        const FName roleName = exactRoleName && !IsBlankName(*exactRoleName)
            ? *exactRoleName
            : NameFromString(roleId);
        if (IsBlankName(roleName))
        {
            outDetail = "role-name-invalid";
            return ApplyResult::Invalid;
        }

        try
        {
            playerController->ServerPreOrderInventory(roleName, inventory);

            const PlayerStateInventoryState state =
                InspectPlayerStateInventoryInternal(
                    playerController, roleName, inventory, false);
            const bool verified = state == PlayerStateInventoryState::Match;
            const char* stateDetail = "mismatch";
            switch (state)
            {
            case PlayerStateInventoryState::PlayerStateMissing:
                stateDetail = "player-state-missing"; break;
            case PlayerStateInventoryState::RoleMissing:
                stateDetail = "role-missing"; break;
            case PlayerStateInventoryState::Match:
                stateDetail = "verified"; break;
            case PlayerStateInventoryState::Mismatch:
                stateDetail = "mismatch"; break;
            }
            std::ostringstream detail;
            detail << "role=" << roleId << ", slots=" << inventory.CharacterSlots.Num()
                   << ", player_state_preordering=" << stateDetail;
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

    ApplyResult PostSpawnApply(
        APBCharacter* character,
        const json& snapshot,
        const FPBInventoryNetworkConfig& expectedInventory)
    {
        if (!character) return ApplyResult::Invalid;

        try
        {
            const std::string roleId = ResolveLiveCharacterRoleId(character);
            FPBRoleNetworkConfig roleConfig{};
            if (roleId.empty() || !TryResolveRoleConfig(snapshot, roleId, roleConfig))
                return ApplyResult::Invalid;

            if (expectedInventory.CharacterSlots.Num() != expectedInventory.InventoryItems.Num())
                return ApplyResult::Invalid;

            APBPlayerController* const playerController =
                FindPlayerControllerForCharacter(character);
            if (!playerController) return ApplyResult::Pending;
            APBPlayerState* const playerState = playerController->PBPlayerState;
            if (!playerState) return ApplyResult::Pending;
            const std::string selectedRole = NameToString(playerState->SelectedCharacterID);
            const std::string possessedRole = NameToString(playerState->PossessedCharacterId);
            if (selectedRole != roleId || possessedRole != roleId)
                return ApplyResult::IdentityMismatch;

            ApplyResult result = ApplyResult::Applied;

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
            }

            const json* roleJson = FindRoleJson(snapshot, roleId);
            result = MergeResult(result, ApplyWeaponIfEffective(
                character, roleConfig.FirstWeaponPartData,
                FindWeaponJson(roleJson, roleConfig.FirstWeaponPartData.WeaponID),
                EPBCharacterSlotType::FirstWeapon, expectedInventory, 0));
            result = MergeResult(result, ApplyWeaponIfEffective(
                character, roleConfig.SecondWeaponPartData,
                FindWeaponJson(roleJson, roleConfig.SecondWeaponPartData.WeaponID),
                EPBCharacterSlotType::SecondWeapon, expectedInventory, 1));
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

    void MarkActorForReplication(AActor* actor)
    {
        if (!actor) return;
        actor->FlushNetDormancy();
        actor->ForceNetUpdate();
    }
}
