#pragma once

// ======================================================
//  LoadoutShowroomApplication - client armory display bridge
// ======================================================
//  Applies metaserver loadout snapshots to showroom display actors.

#include <string>

#include "../Libs/json.hpp"
#include "../SDK.hpp"

namespace LoadoutShowroomApplication
{
    using json = nlohmann::json;

    struct ApplyResult
    {
        int Scanned = 0;
        int Matched = 0;
        int Applied = 0;
        int Refreshed = 0;
    };

    SDK::UPBShowRoomManager* GetShowRoomManager();

    ApplyResult ApplySnapshotToShowRoom(
        const json& snapshot,
        bool forceRefresh,
        const std::string& reason);

    bool ApplySnapshotToInventoryActor(
        SDK::APBDisplayActor* displayActor,
        const json& snapshot,
        const std::string& roleId,
        const std::string& itemId,
        bool refresh);

    SDK::APBDisplayCharacter* ApplySnapshotToDisplayCharacterActor(
        SDK::APBDisplayCharacter* displayCharacter,
        const json& snapshot,
        const std::string& roleId,
        bool allowReplacement);

    bool ApplySnapshotToCharacterSlot(
        const json& snapshot,
        const std::string& roleId,
        SDK::EPBCharacterSlotType slotType,
        bool refresh);

    SDK::APBDisplayActor* ApplySnapshotToCharacterSlotActor(
        SDK::APBDisplayCharacter* displayCharacter,
        const json& snapshot,
        const std::string& roleId,
        SDK::EPBCharacterSlotType slotType,
        bool refresh);

    bool SpawnInventoryPreview(
        const std::string& roleId,
        const std::string& itemId,
        const json* snapshot = nullptr);
}
