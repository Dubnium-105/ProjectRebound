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

    bool SpawnInventoryPreview(
        const std::string& roleId,
        const std::string& itemId);
}
