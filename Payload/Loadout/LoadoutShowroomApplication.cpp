// ======================================================
//  LoadoutShowroomApplication - client armory display bridge
// ======================================================

#include "LoadoutShowroomApplication.h"
#include "LoadoutSerializer.h"

#include <algorithm>
#include <string>
#include <unordered_map>
#include <vector>

#include "../Debug/Debug.h"

using namespace SDK;

extern std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
extern UObject* GetLastOfType(UClass* theClass, bool includeDefault);
extern "C" void PayloadPushClientProcessEventSuppression();
extern "C" void PayloadPopClientProcessEventSuppression();

namespace LoadoutShowroomApplication
{
    using namespace LoadoutSerializer;

    namespace
    {
        class ScopedClientProcessEventSuppression
        {
        public:
            ScopedClientProcessEventSuppression()
            {
                PayloadPushClientProcessEventSuppression();
            }

            ~ScopedClientProcessEventSuppression()
            {
                PayloadPopClientProcessEventSuppression();
            }

            ScopedClientProcessEventSuppression(const ScopedClientProcessEventSuppression&) = delete;
            ScopedClientProcessEventSuppression& operator=(const ScopedClientProcessEventSuppression&) = delete;
        };

        bool SameName(const FName& left, const FName& right)
        {
            return left.ComparisonIndex == right.ComparisonIndex && left.Number == right.Number;
        }

        bool SameNameArray(const TArray<FName>& left, const TArray<FName>& right)
        {
            if (left.Num() != right.Num()) return false;
            for (int i = 0; i < left.Num(); ++i)
            {
                if (!SameName(left[i], right[i])) return false;
            }
            return true;
        }

        bool SameSlotArray(const TArray<EPBPartSlotType>& left, const TArray<EPBPartSlotType>& right)
        {
            if (left.Num() != right.Num()) return false;
            for (int i = 0; i < left.Num(); ++i)
            {
                if (left[i] != right[i]) return false;
            }
            return true;
        }

        bool SameCharacterSlotArray(const TArray<EPBCharacterSlotType>& left, const TArray<EPBCharacterSlotType>& right)
        {
            if (left.Num() != right.Num()) return false;
            for (int i = 0; i < left.Num(); ++i)
            {
                if (left[i] != right[i]) return false;
            }
            return true;
        }

        bool SameWeaponPartArray(
            const TArray<FPBWeaponPartNetworkConfig>& left,
            const TArray<FPBWeaponPartNetworkConfig>& right)
        {
            if (left.Num() != right.Num()) return false;
            for (int i = 0; i < left.Num(); ++i)
            {
                if (!SameName(left[i].WeaponPartID, right[i].WeaponPartID) ||
                    !SameName(left[i].WeaponPartSkinID, right[i].WeaponPartSkinID) ||
                    !SameName(left[i].WeaponPartSpecialSkinID, right[i].WeaponPartSpecialSkinID) ||
                    !SameName(left[i].WeaponPartSkinPaintingID, right[i].WeaponPartSkinPaintingID))
                {
                    return false;
                }
            }
            return true;
        }

        bool SameCharacterConfig(const FPBCharacterNetworkConfig& left, const FPBCharacterNetworkConfig& right)
        {
            if (!SameName(left.SkinPaintingID, right.SkinPaintingID)) return false;
            if (left.SkinClassArray.Num() != right.SkinClassArray.Num()) return false;
            for (int i = 0; i < left.SkinClassArray.Num(); ++i)
            {
                if (left.SkinClassArray[i] != right.SkinClassArray[i]) return false;
            }
            return SameNameArray(left.SkinIDArray, right.SkinIDArray);
        }

        bool SameWeaponConfig(const FPBWeaponNetworkConfig& left, const FPBWeaponNetworkConfig& right)
        {
            return SameName(left.WeaponID, right.WeaponID) &&
                SameName(left.WeaponClassID, right.WeaponClassID) &&
                SameName(left.OrnamentID, right.OrnamentID) &&
                SameSlotArray(left.WeaponPartSlotTypeArray, right.WeaponPartSlotTypeArray) &&
                SameWeaponPartArray(left.WeaponPartConfigs, right.WeaponPartConfigs);
        }

        bool SameInventoryConfig(const FPBInventoryNetworkConfig& left, const FPBInventoryNetworkConfig& right)
        {
            return SameCharacterSlotArray(left.CharacterSlots, right.CharacterSlots) &&
                SameNameArray(left.InventoryItems, right.InventoryItems);
        }

        bool SameRoleConfig(const FPBRoleNetworkConfig& left, const FPBRoleNetworkConfig& right)
        {
            return SameName(left.CharacterID, right.CharacterID) &&
                SameCharacterConfig(left.CharacterData, right.CharacterData) &&
                SameWeaponConfig(left.FirstWeaponPartData, right.FirstWeaponPartData) &&
                SameWeaponConfig(left.SecondWeaponPartData, right.SecondWeaponPartData) &&
                SameName(left.MeleeWeaponData.ID, right.MeleeWeaponData.ID) &&
                SameName(left.MeleeWeaponData.SkinID, right.MeleeWeaponData.SkinID) &&
                SameName(left.LeftLauncherData.ID, right.LeftLauncherData.ID) &&
                SameName(left.LeftLauncherData.SkinID, right.LeftLauncherData.SkinID) &&
                SameName(left.RightLauncherData.ID, right.RightLauncherData.ID) &&
                SameName(left.RightLauncherData.SkinID, right.RightLauncherData.SkinID) &&
                SameName(left.MobilityModuleData.MobilityModuleID, right.MobilityModuleData.MobilityModuleID) &&
                SameInventoryConfig(left.InventoryData, right.InventoryData);
        }

        std::string NameSignature(const FName& value)
        {
            return NameToString(value);
        }

        void AppendNameArraySignature(std::string& signature, const TArray<FName>& values)
        {
            signature += "[";
            for (int i = 0; i < values.Num(); ++i)
            {
                if (i > 0) signature += ",";
                signature += NameSignature(values[i]);
            }
            signature += "]";
        }

        void AppendSlotArraySignature(std::string& signature, const TArray<EPBPartSlotType>& values)
        {
            signature += "[";
            for (int i = 0; i < values.Num(); ++i)
            {
                if (i > 0) signature += ",";
                signature += std::to_string(static_cast<int>(values[i]));
            }
            signature += "]";
        }

        void AppendCharacterSlotArraySignature(std::string& signature, const TArray<EPBCharacterSlotType>& values)
        {
            signature += "[";
            for (int i = 0; i < values.Num(); ++i)
            {
                if (i > 0) signature += ",";
                signature += std::to_string(static_cast<int>(values[i]));
            }
            signature += "]";
        }

        void AppendWeaponSignature(std::string& signature, const FPBWeaponNetworkConfig& config)
        {
            signature += NameSignature(config.WeaponID);
            signature += "/";
            signature += NameSignature(config.WeaponClassID);
            signature += "/";
            signature += NameSignature(config.OrnamentID);
            AppendSlotArraySignature(signature, config.WeaponPartSlotTypeArray);
            signature += "{";
            for (int i = 0; i < config.WeaponPartConfigs.Num(); ++i)
            {
                if (i > 0) signature += ",";
                signature += NameSignature(config.WeaponPartConfigs[i].WeaponPartID);
                signature += ":";
                signature += NameSignature(config.WeaponPartConfigs[i].WeaponPartSkinID);
                signature += ":";
                signature += NameSignature(config.WeaponPartConfigs[i].WeaponPartSkinPaintingID);
                signature += ":";
                signature += NameSignature(config.WeaponPartConfigs[i].WeaponPartSpecialSkinID);
            }
            signature += "}";
        }

        std::string RoleConfigSignature(const FPBRoleNetworkConfig& config)
        {
            std::string signature;
            signature.reserve(512);
            signature += NameSignature(config.CharacterID);
            signature += "|fw=";
            AppendWeaponSignature(signature, config.FirstWeaponPartData);
            signature += "|sw=";
            AppendWeaponSignature(signature, config.SecondWeaponPartData);
            signature += "|melee=";
            signature += NameSignature(config.MeleeWeaponData.ID);
            signature += "|left=";
            signature += NameSignature(config.LeftLauncherData.ID);
            signature += "|right=";
            signature += NameSignature(config.RightLauncherData.ID);
            signature += "|mobility=";
            signature += NameSignature(config.MobilityModuleData.MobilityModuleID);
            signature += "|slots=";
            AppendCharacterSlotArraySignature(signature, config.InventoryData.CharacterSlots);
            signature += "|items=";
            AppendNameArraySignature(signature, config.InventoryData.InventoryItems);
            return signature;
        }

        std::unordered_map<APBDisplayCharacter*, std::string> gAppliedRoleConfigSignatures;
        std::unordered_map<APBDisplayCharacter*, bool> gRetiredDisplayCharacters;

        void RefreshDisplayActor(APBDisplayActor* displayActor)
        {
            if (!displayActor) return;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                displayActor->K2_RefreshDisplayActor();
            }
            catch (...) {}
        }

        bool ApplyWeaponConfigToDisplayWeapon(
            APBDisplayWeapon* displayWeapon,
            const FPBWeaponNetworkConfig& weaponConfig,
            bool refresh)
        {
            if (!displayWeapon) return false;
            const bool changed =
                !SameName(displayWeapon->ItemId, weaponConfig.WeaponID) ||
                !SameWeaponConfig(displayWeapon->WeaponPartConfig, weaponConfig);

            displayWeapon->ItemId = weaponConfig.WeaponID;
            displayWeapon->WeaponPartConfig = weaponConfig;
            if (refresh && changed) RefreshDisplayActor(displayWeapon);
            return changed;
        }

        bool ResolveWeaponConfigForInventoryActor(
            const json& snapshot,
            const std::string& roleId,
            const std::string& itemId,
            FPBWeaponNetworkConfig& outConfig)
        {
            if (IsBlankText(roleId) || IsBlankText(itemId)) return false;

            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) return false;

            if (NameToString(roleConfig.FirstWeaponPartData.WeaponID) == itemId)
            {
                outConfig = roleConfig.FirstWeaponPartData;
                return true;
            }

            if (NameToString(roleConfig.SecondWeaponPartData.WeaponID) == itemId)
            {
                outConfig = roleConfig.SecondWeaponPartData;
                return true;
            }

            outConfig = {};
            outConfig.WeaponID = NameFromString(itemId);
            return !IsBlankName(outConfig.WeaponID);
        }

        bool IsWeaponSlot(EPBCharacterSlotType slotType)
        {
            return slotType == EPBCharacterSlotType::FirstWeapon ||
                slotType == EPBCharacterSlotType::SecondWeapon;
        }

        std::string ResolveDisplayCharacterRoleId(APBDisplayCharacter* displayCharacter)
        {
            if (!displayCharacter) return "";
            std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
            if (IsBlankText(roleId)) roleId = NameToString(displayCharacter->ItemId);
            return IsBlankText(roleId) ? "" : roleId;
        }

        APBDisplayCharacter* FindDisplayCharacterForRole(const std::string& roleId)
        {
            if (IsBlankText(roleId)) return nullptr;

            for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
            {
                if (!object || object->IsDefaultObject()) continue;
                auto* displayCharacter = static_cast<APBDisplayCharacter*>(object);
                if (gRetiredDisplayCharacters.find(displayCharacter) != gRetiredDisplayCharacters.end())
                    continue;
                if (ResolveDisplayCharacterRoleId(displayCharacter) == roleId)
                    return displayCharacter;
            }
            return nullptr;
        }

        UPBShowRoomManager* ResolveShowRoomManager()
        {
            UObject* object = GetLastOfType(UPBShowRoomManager::StaticClass(), false);
            return object ? static_cast<UPBShowRoomManager*>(object) : nullptr;
        }

        void RetireDisplayActor(APBDisplayActor* displayActor)
        {
            if (!displayActor || displayActor->IsDefaultObject()) return;
            try { displayActor->SetActorHiddenInGame(true); }
            catch (...) {}
            try { displayActor->SetActorEnableCollision(false); }
            catch (...) {}
            try
            {
                if (displayActor->DisplayActorBody)
                    displayActor->DisplayActorBody->SetVisibility(false, true);
            }
            catch (...) {}
            try { displayActor->K2_DestroyActor(); }
            catch (...) {}
        }

        void ReplaceActorInDisplayList(
            TArray<APBDisplayActor*>& actors,
            APBDisplayActor* previous,
            APBDisplayActor* replacement)
        {
            if (!actors.IsValid() || !previous || !replacement) return;
            for (int i = 0; i < actors.Num(); ++i)
            {
                if (actors[i] == previous)
                    actors[i] = replacement;
            }
        }

        void ReplaceActorInDisplayMap(
            TMap<FName, APBDisplayActor*>& actors,
            APBDisplayActor* previous,
            APBDisplayActor* replacement)
        {
            if (!actors.IsValid() || !previous || !replacement) return;
            for (auto it = UC::begin(actors); it != UC::end(actors); ++it)
            {
                if (it->Value() == previous)
                    it->Value() = replacement;
            }
        }

        void ReplaceShowRoomActorReferences(
            APBDisplayCharacter* displayCharacter,
            APBDisplayActor* previous,
            APBDisplayActor* replacement)
        {
            if (!displayCharacter || !previous || !replacement || previous == replacement) return;

            ReplaceActorInDisplayList(displayCharacter->ChildDisplayActors, previous, replacement);
            ReplaceActorInDisplayMap(displayCharacter->ChildDisplayActorMap, previous, replacement);

            UPBShowRoomManager* manager = ResolveShowRoomManager();
            if (!manager) return;

            ReplaceActorInDisplayList(manager->CacheActorArray, previous, replacement);
            ReplaceActorInDisplayMap(manager->CacheActorMap, previous, replacement);
            if (manager->ViewTarget == previous)
                manager->ViewTarget = replacement;
        }

        void ReplaceShowRoomTopLevelActorReferences(
            APBDisplayActor* previous,
            APBDisplayActor* replacement)
        {
            if (!previous || !replacement || previous == replacement) return;

            UPBShowRoomManager* manager = ResolveShowRoomManager();
            if (!manager) return;

            ReplaceActorInDisplayList(manager->CacheActorArray, previous, replacement);
            ReplaceActorInDisplayMap(manager->CacheActorMap, previous, replacement);
            if (manager->ViewTarget == previous)
                manager->ViewTarget = replacement;
        }

        void SnapReplacementToPreviousActor(APBDisplayActor* replacement, APBDisplayActor* previous)
        {
            if (!replacement || !previous) return;

            try
            {
                FHitResult hit{};
                replacement->K2_SetActorLocationAndRotation(
                    previous->K2_GetActorLocation(),
                    previous->K2_GetActorRotation(),
                    false,
                    &hit,
                    true);
            }
            catch (...) {}

            try
            {
                replacement->SetActorScale3D(previous->GetActorScale3D());
            }
            catch (...) {}
        }

        void SnapReplacementToPreviousWeapon(APBDisplayWeapon* replacement, APBDisplayWeapon* previous)
        {
            if (!replacement || !previous) return;

            try
            {
                FHitResult hit{};
                replacement->K2_SetActorLocationAndRotation(
                    previous->K2_GetActorLocation(),
                    previous->K2_GetActorRotation(),
                    false,
                    &hit,
                    true);
            }
            catch (...) {}

            try
            {
                USceneComponent* previousRoot = previous->K2_GetRootComponent();
                USceneComponent* replacementRoot = replacement->K2_GetRootComponent();
                if (previousRoot && replacementRoot)
                {
                    USceneComponent* parent = previousRoot->GetAttachParent();
                    if (parent)
                    {
                        replacementRoot->K2_AttachToComponent(
                            parent,
                            previousRoot->GetAttachSocketName(),
                            EAttachmentRule::SnapToTarget,
                            EAttachmentRule::SnapToTarget,
                            EAttachmentRule::SnapToTarget,
                            false);
                        return;
                    }
                }
            }
            catch (...) {}

            try
            {
                AActor* parentActor = previous->GetAttachParentActor();
                if (parentActor)
                {
                    replacement->K2_AttachToActor(
                        parentActor,
                        previous->GetAttachParentSocketName(),
                        EAttachmentRule::SnapToTarget,
                        EAttachmentRule::SnapToTarget,
                        EAttachmentRule::SnapToTarget,
                        false);
                }
            }
            catch (...) {}
        }

        APBDisplayWeapon* SpawnReplacementWeapon(
            APBDisplayCharacter* displayCharacter,
            APBDisplayWeapon* previous,
            const FPBWeaponNetworkConfig& weaponConfig)
        {
            if (!displayCharacter || IsBlankName(weaponConfig.WeaponID)) return nullptr;

            UPBShowRoomManager* manager = ResolveShowRoomManager();
            if (!manager)
            {
                ClientLog("[LOADOUT] Showroom weapon replace failed: no showroom manager target=" +
                    NameToString(weaponConfig.WeaponID));
                return nullptr;
            }

            APBDisplayActor* spawned = nullptr;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                spawned = manager->SpawnWeapon(displayCharacter->RoleConfig.CharacterID, weaponConfig.WeaponID);
            }
            catch (...) {}

            if (!spawned || !spawned->IsA(APBDisplayWeapon::StaticClass()))
            {
                ClientLog("[LOADOUT] Showroom weapon replace failed: spawn returned " +
                    std::string(spawned ? spawned->GetFullName() : "null") +
                    " target=" + NameToString(weaponConfig.WeaponID));
                return nullptr;
            }

            auto* replacement = static_cast<APBDisplayWeapon*>(spawned);
            ApplyWeaponConfigToDisplayWeapon(replacement, weaponConfig, true);
            try { replacement->SetOwner(displayCharacter); }
            catch (...) {}
            SnapReplacementToPreviousWeapon(replacement, previous);
            RefreshDisplayActor(replacement);
            if (previous != replacement)
                RetireDisplayActor(previous);
            return replacement;
        }

        bool ApplyWeaponSlotToDisplayCharacter(
            APBDisplayCharacter* displayCharacter,
            EPBCharacterSlotType slotType,
            const FPBWeaponNetworkConfig& weaponConfig,
            bool refresh)
        {
            if (!displayCharacter || !IsWeaponSlot(slotType) || IsBlankName(weaponConfig.WeaponID))
                return false;

            APBDisplayWeapon*& weaponRef =
                slotType == EPBCharacterSlotType::FirstWeapon
                ? displayCharacter->DisplayFirstWeapon
                : displayCharacter->DisplaySecondWeapon;

            APBDisplayWeapon* current = weaponRef;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                APBDisplayActor* slotChild = displayCharacter->GetChildByCharacterSlot(slotType);
                if (slotChild && slotChild->IsA(APBDisplayWeapon::StaticClass()))
                    current = static_cast<APBDisplayWeapon*>(slotChild);
            }
            catch (...) {}

            const bool needsReplacement =
                !current ||
                !SameName(current->ItemId, weaponConfig.WeaponID);

            bool changed = false;
            if (needsReplacement)
            {
                const std::string oldItem = current ? NameToString(current->ItemId) : "null";
                APBDisplayWeapon* replacement = SpawnReplacementWeapon(
                    displayCharacter,
                    current,
                    weaponConfig);
                if (replacement)
                {
                    ReplaceShowRoomActorReferences(displayCharacter, current, replacement);
                    weaponRef = replacement;
                    changed = true;
                    ClientLog("[LOADOUT] Showroom weapon slot replaced: role=" +
                        NameToString(displayCharacter->RoleConfig.CharacterID) +
                        " slot=" + std::to_string(static_cast<int>(slotType)) +
                        " old=" + oldItem +
                        " new=" + NameToString(weaponConfig.WeaponID) +
                        " actor=" + replacement->GetFullName());
                }
                else
                {
                    ClientLog("[LOADOUT] Showroom weapon slot replace failed: role=" +
                        NameToString(displayCharacter->RoleConfig.CharacterID) +
                        " slot=" + std::to_string(static_cast<int>(slotType)) +
                        " old=" + oldItem +
                        " new=" + NameToString(weaponConfig.WeaponID));
                }
            }

            if (weaponRef)
            {
                changed = ApplyWeaponConfigToDisplayWeapon(weaponRef, weaponConfig, refresh) || changed;
            }

            return changed;
        }

        bool DisplayCharacterWeaponMismatch(
            APBDisplayCharacter* displayCharacter,
            const FPBRoleNetworkConfig& roleConfig)
        {
            if (!displayCharacter) return false;

            if (!displayCharacter->DisplayFirstWeapon ||
                !SameName(displayCharacter->DisplayFirstWeapon->ItemId, roleConfig.FirstWeaponPartData.WeaponID))
            {
                return true;
            }

            if (!displayCharacter->DisplaySecondWeapon ||
                !SameName(displayCharacter->DisplaySecondWeapon->ItemId, roleConfig.SecondWeaponPartData.WeaponID))
            {
                return true;
            }

            return false;
        }

        APBDisplayCharacter* SpawnReplacementDisplayCharacter(
            APBDisplayCharacter* previous,
            const FPBRoleNetworkConfig& roleConfig)
        {
            if (!previous || IsBlankName(roleConfig.CharacterID)) return nullptr;

            UWorld* world = UWorld::GetWorld();
            if (!world) return nullptr;

            TSubclassOf<APBDisplayCharacter> displayClass = previous->Class;
            UPBShowRoomManager* manager = ResolveShowRoomManager();
            if (manager && manager->DisplayCharacterClass)
                displayClass = manager->DisplayCharacterClass;

            APBDisplayCharacter* replacement = nullptr;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                replacement = UPBDisplayActorLibrary::SpawnDisplayCharacter(
                    world,
                    displayClass,
                    roleConfig,
                    false);
            }
            catch (...) {}

            if (!replacement)
            {
                ClientLog("[LOADOUT] Showroom character replace failed: spawn returned null role=" +
                    NameToString(roleConfig.CharacterID));
                return nullptr;
            }

            replacement->RoleConfig = roleConfig;
            replacement->ItemId = roleConfig.CharacterID;
            SnapReplacementToPreviousActor(replacement, previous);
            ApplyWeaponSlotToDisplayCharacter(
                replacement,
                EPBCharacterSlotType::FirstWeapon,
                roleConfig.FirstWeaponPartData,
                true);
            ApplyWeaponSlotToDisplayCharacter(
                replacement,
                EPBCharacterSlotType::SecondWeapon,
                roleConfig.SecondWeaponPartData,
                true);

            const std::string oldActorName = previous->GetFullName();
            const std::string newActorName = replacement->GetFullName();
            ReplaceShowRoomTopLevelActorReferences(previous, replacement);
            gAppliedRoleConfigSignatures[replacement] = RoleConfigSignature(roleConfig);
            gRetiredDisplayCharacters[previous] = true;
            RetireDisplayActor(previous);

            ClientLog("[LOADOUT] Showroom character replaced from snapshot: role=" +
                NameToString(roleConfig.CharacterID) +
                " oldActor=" + oldActorName +
                " newActor=" + newActorName);
            return replacement;
        }

        bool ApplyRoleConfigToDisplayCharacter(
            APBDisplayCharacter* displayCharacter,
            const FPBRoleNetworkConfig& roleConfig,
            bool forceRefresh,
            ApplyResult& result)
        {
            if (!displayCharacter) return false;
            if (gRetiredDisplayCharacters.find(displayCharacter) != gRetiredDisplayCharacters.end())
                return false;

            const std::string signature = RoleConfigSignature(roleConfig);
            const auto signatureIt = gAppliedRoleConfigSignatures.find(displayCharacter);
            const bool alreadyApplied =
                signatureIt != gAppliedRoleConfigSignatures.end() &&
                signatureIt->second == signature;
            const bool changed = !alreadyApplied && !SameRoleConfig(displayCharacter->RoleConfig, roleConfig);
            const bool weaponMismatch = DisplayCharacterWeaponMismatch(displayCharacter, roleConfig);
            if ((!forceRefresh && alreadyApplied) ||
                (alreadyApplied && !changed && !weaponMismatch))
            {
                return false;
            }

            displayCharacter->RoleConfig = roleConfig;
            displayCharacter->ItemId = roleConfig.CharacterID;
            ++result.Applied;

            if (weaponMismatch)
            {
                if (SpawnReplacementDisplayCharacter(displayCharacter, roleConfig))
                {
                    ++result.Refreshed;
                    return true;
                }
            }

            if (changed || forceRefresh || !alreadyApplied || weaponMismatch)
            {
                ApplyWeaponSlotToDisplayCharacter(
                    displayCharacter,
                    EPBCharacterSlotType::FirstWeapon,
                    roleConfig.FirstWeaponPartData,
                    true);
                ApplyWeaponSlotToDisplayCharacter(
                    displayCharacter,
                    EPBCharacterSlotType::SecondWeapon,
                    roleConfig.SecondWeaponPartData,
                    true);
                ++result.Refreshed;
            }

            gAppliedRoleConfigSignatures[displayCharacter] = signature;
            return true;
        }
    }

    UPBShowRoomManager* GetShowRoomManager()
    {
        UObject* object = GetLastOfType(UPBShowRoomManager::StaticClass(), false);
        return object ? static_cast<UPBShowRoomManager*>(object) : nullptr;
    }

    ApplyResult ApplySnapshotToShowRoom(
        const json& snapshot,
        bool forceRefresh,
        const std::string& reason)
    {
        ApplyResult result{};
        if (!snapshot.is_object() || !snapshot.contains("roles")) return result;

        for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            APBDisplayCharacter* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            if (gRetiredDisplayCharacters.find(displayCharacter) != gRetiredDisplayCharacters.end())
                continue;
            ++result.Scanned;

            const std::string roleId = ResolveDisplayCharacterRoleId(displayCharacter);
            if (roleId.empty()) continue;

            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) continue;
            ++result.Matched;

            ApplyRoleConfigToDisplayCharacter(displayCharacter, roleConfig, forceRefresh, result);
        }

        if (result.Applied > 0)
        {
            ClientLog("[LOADOUT] Showroom applied: reason=" + reason +
                " scanned=" + std::to_string(result.Scanned) +
                " matched=" + std::to_string(result.Matched) +
                " applied=" + std::to_string(result.Applied) +
                " refreshed=" + std::to_string(result.Refreshed));
        }
        return result;
    }

    bool ApplySnapshotToInventoryActor(
        APBDisplayActor* displayActor,
        const json& snapshot,
        const std::string& roleId,
        const std::string& itemId,
        bool refresh)
    {
        if (!displayActor || displayActor->IsDefaultObject() || IsBlankText(itemId)) return false;

        if (displayActor->IsA(APBDisplayWeapon::StaticClass()))
        {
            FPBWeaponNetworkConfig weaponConfig{};
            if (ResolveWeaponConfigForInventoryActor(snapshot, roleId, itemId, weaponConfig))
            {
                return ApplyWeaponConfigToDisplayWeapon(
                    static_cast<APBDisplayWeapon*>(displayActor),
                    weaponConfig,
                    refresh);
            }
        }

        const FName itemName = NameFromString(itemId);
        if (IsBlankName(itemName)) return false;

        const bool changed = !SameName(displayActor->ItemId, itemName);
        displayActor->ItemId = itemName;
        if (refresh && changed) RefreshDisplayActor(displayActor);
        return changed;
    }

    APBDisplayCharacter* ApplySnapshotToDisplayCharacterActor(
        APBDisplayCharacter* displayCharacter,
        const json& snapshot,
        const std::string& roleId,
        bool allowReplacement)
    {
        if (!displayCharacter || displayCharacter->IsDefaultObject()) return nullptr;

        std::string effectiveRoleId = roleId;
        if (IsBlankText(effectiveRoleId))
            effectiveRoleId = ResolveDisplayCharacterRoleId(displayCharacter);
        if (IsBlankText(effectiveRoleId)) return nullptr;

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, effectiveRoleId, roleConfig)) return nullptr;

        ApplyResult result{};
        APBDisplayCharacter* before = displayCharacter;
        if (!allowReplacement)
        {
            const bool wasRetired =
                gRetiredDisplayCharacters.find(displayCharacter) != gRetiredDisplayCharacters.end();
            if (wasRetired) return nullptr;

            displayCharacter->RoleConfig = roleConfig;
            displayCharacter->ItemId = roleConfig.CharacterID;
            ApplyWeaponSlotToDisplayCharacter(
                displayCharacter,
                EPBCharacterSlotType::FirstWeapon,
                roleConfig.FirstWeaponPartData,
                true);
            ApplyWeaponSlotToDisplayCharacter(
                displayCharacter,
                EPBCharacterSlotType::SecondWeapon,
                roleConfig.SecondWeaponPartData,
                true);
            gAppliedRoleConfigSignatures[displayCharacter] = RoleConfigSignature(roleConfig);
            return displayCharacter;
        }

        if (!ApplyRoleConfigToDisplayCharacter(displayCharacter, roleConfig, true, result))
            return nullptr;

        if (gRetiredDisplayCharacters.find(before) != gRetiredDisplayCharacters.end())
            return FindDisplayCharacterForRole(effectiveRoleId);
        return before;
    }

    bool ApplySnapshotToCharacterSlot(
        const json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        bool refresh)
    {
        if (!IsWeaponSlot(slotType) || IsBlankText(roleId)) return false;

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) return false;

        APBDisplayCharacter* displayCharacter = FindDisplayCharacterForRole(roleId);
        APBDisplayActor* actor = ApplySnapshotToCharacterSlotActor(
            displayCharacter,
            snapshot,
            roleId,
            slotType,
            refresh);
        return actor != nullptr;
    }

    APBDisplayActor* ApplySnapshotToCharacterSlotActor(
        APBDisplayCharacter* displayCharacter,
        const json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        bool refresh)
    {
        if (!displayCharacter || !IsWeaponSlot(slotType)) return nullptr;

        std::string effectiveRoleId = roleId;
        if (IsBlankText(effectiveRoleId))
            effectiveRoleId = ResolveDisplayCharacterRoleId(displayCharacter);
        if (IsBlankText(effectiveRoleId)) return nullptr;

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, effectiveRoleId, roleConfig)) return nullptr;

        displayCharacter->RoleConfig = roleConfig;
        displayCharacter->ItemId = roleConfig.CharacterID;

        const FPBWeaponNetworkConfig& weaponConfig =
            slotType == EPBCharacterSlotType::FirstWeapon
            ? roleConfig.FirstWeaponPartData
            : roleConfig.SecondWeaponPartData;

        ApplyWeaponSlotToDisplayCharacter(
            displayCharacter,
            slotType,
            weaponConfig,
            refresh);

        APBDisplayWeapon* weapon =
            slotType == EPBCharacterSlotType::FirstWeapon
            ? displayCharacter->DisplayFirstWeapon
            : displayCharacter->DisplaySecondWeapon;
        if (weapon && !SameName(weapon->ItemId, weaponConfig.WeaponID))
            ApplyWeaponConfigToDisplayWeapon(weapon, weaponConfig, refresh);

        return weapon;
    }

    bool SpawnInventoryPreview(
        const std::string& roleId,
        const std::string& itemId,
        const json* snapshot)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId)) return false;
        UPBShowRoomManager* manager = GetShowRoomManager();
        if (!manager) return false;

        try
        {
            ScopedClientProcessEventSuppression suppressProcessEventHooks;
            const FName itemName = NameFromString(itemId);
            APBDisplayActor* displayActor = manager->SpawnInventory(NameFromString(roleId), itemName);
            if (displayActor && snapshot)
            {
                ApplySnapshotToInventoryActor(displayActor, *snapshot, roleId, itemId, true);
            }
            if (displayActor)
            {
                manager->ViewTargetID = itemName;
                manager->ViewTarget = displayActor;
                manager->SetViewTarget(displayActor);
                manager->SetViewTargetByID(itemName);
                manager->FocusItem(itemName);
            }
            return displayActor != nullptr;
        }
        catch (...)
        {
            return false;
        }
    }
}
