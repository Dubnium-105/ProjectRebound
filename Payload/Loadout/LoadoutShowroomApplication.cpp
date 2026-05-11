// ======================================================
//  LoadoutShowroomApplication - client armory display bridge
// ======================================================

#include "LoadoutShowroomApplication.h"
#include "LoadoutSerializer.h"

#include <algorithm>
#include <string>
#include <vector>

#include "../Debug/Debug.h"

using namespace SDK;

extern std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
extern UObject* GetLastOfType(UClass* theClass, bool includeDefault);

namespace LoadoutShowroomApplication
{
    using namespace LoadoutSerializer;

    namespace
    {
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

        std::string ResolveDisplayCharacterRoleId(APBDisplayCharacter* displayCharacter)
        {
            if (!displayCharacter) return "";
            std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
            if (IsBlankText(roleId)) roleId = NameToString(displayCharacter->ItemId);
            return IsBlankText(roleId) ? "" : roleId;
        }

        bool ApplyRoleConfigToDisplayCharacter(
            APBDisplayCharacter* displayCharacter,
            const FPBRoleNetworkConfig& roleConfig,
            bool forceRefresh,
            ApplyResult& result)
        {
            if (!displayCharacter) return false;

            const bool changed = !SameRoleConfig(displayCharacter->RoleConfig, roleConfig);
            if (!changed && !forceRefresh) return false;

            displayCharacter->RoleConfig = roleConfig;
            ++result.Applied;

            if (changed || forceRefresh)
            {
                try
                {
                    displayCharacter->K2_RefreshDisplayActor();
                    ++result.Refreshed;
                }
                catch (...) {}
            }
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

    bool SpawnInventoryPreview(const std::string& roleId, const std::string& itemId)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId)) return false;
        UPBShowRoomManager* manager = GetShowRoomManager();
        if (!manager) return false;

        try
        {
            manager->SpawnInventory(NameFromString(roleId), NameFromString(itemId));
            return true;
        }
        catch (...)
        {
            return false;
        }
    }
}
