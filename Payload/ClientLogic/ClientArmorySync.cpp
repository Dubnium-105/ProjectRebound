#include "ClientArmorySync.h"

#include "../Debug/Debug.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"

#include <chrono>
#include <sstream>

using namespace SDK;

extern "C" void PayloadPushClientProcessEventSuppression();
extern "C" void PayloadPopClientProcessEventSuppression();

namespace
{
    constexpr auto RetryInterval = std::chrono::milliseconds(500);

    std::chrono::steady_clock::time_point nextAttemptAt{};
    bool syncInProgress = false;

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

        ScopedClientProcessEventSuppression(
            const ScopedClientProcessEventSuppression&) = delete;
        ScopedClientProcessEventSuppression& operator=(
            const ScopedClientProcessEventSuppression&) = delete;
    };

    UPBDataTableManager* FindDataTableManager()
    {
        UEngineSubsystem* subsystem =
            USubsystemBlueprintLibrary::GetEngineSubsystem(
                UPBDataTableManager::StaticClass());
        return subsystem && subsystem->IsA(UPBDataTableManager::StaticClass())
            ? static_cast<UPBDataTableManager*>(subsystem)
            : nullptr;
    }

    UPBArmoryManager* FindArmoryManager(UObject* context)
    {
        UGameInstanceSubsystem* subsystem =
            USubsystemBlueprintLibrary::GetGameInstanceSubsystem(
                context, UPBArmoryManager::StaticClass());
        return subsystem && subsystem->IsA(UPBArmoryManager::StaticClass())
            ? static_cast<UPBArmoryManager*>(subsystem)
            : nullptr;
    }

    UDataTable* ResolveItemTypeTable(UPBDataTableManager* manager)
    {
        if (!manager)
            return nullptr;
        if (UDataTable* loaded = manager->ItemTypeMappingDataTable.Get())
            return loaded;

        TSoftObjectPtr<UObject> generic{};
        static_cast<FSoftObjectPtr&>(generic) =
            static_cast<const FSoftObjectPtr&>(
                manager->ItemTypeMappingDataTable);
        UObject* loaded = UKismetSystemLibrary::LoadAsset_Blocking(generic);
        return loaded && loaded->IsA(UDataTable::StaticClass())
            ? static_cast<UDataTable*>(loaded)
            : nullptr;
    }

    bool HasCompleteRuntimeItems(
        const FPBArmory& armory,
        int expectedCount)
    {
        // UPBArmoryManager::HasItem compares only FPBItem::ID. Count is
        // inventory metadata and the native archive path is free to reset it.
        return armory.OwnedItems.Num() == expectedCount;
    }

    bool SynchronizeClientArmory()
    {
        if (syncInProgress)
            return false;

        syncInProgress = true;
        struct ResetInProgress
        {
            ~ResetInProgress() { syncInProgress = false; }
        } resetInProgress;

        UWorld* world = UWorld::GetWorld();
        if (!world || !world->OwningGameInstance ||
            world->OwningGameInstance->LocalPlayers.Num() == 0)
        {
            return false;
        }

        UGameInstance* gameInstance = world->OwningGameInstance;
        auto* localPlayer = static_cast<UPBLocalPlayer*>(
            gameInstance->LocalPlayers[0]);
        if (!localPlayer)
            return false;

        ScopedClientProcessEventSuppression suppressProcessEventHooks;
        UPBArmoryManager* armoryManager = FindArmoryManager(localPlayer);
        UPBDataTableManager* dataTableManager = FindDataTableManager();
        if (!armoryManager || !dataTableManager)
            return false;

        UDataTable* itemTypeTable = ResolveItemTypeTable(dataTableManager);
        if (!itemTypeTable || !itemTypeTable->RowMap.IsValid())
            return false;

        const int expectedCount = itemTypeTable->RowMap.Num();
        if (expectedCount <= 0)
            return false;

        UPBPersistentUser* persistentUser = localPlayer->PersistentUser;
        const bool managerComplete = HasCompleteRuntimeItems(
            armoryManager->Armorys, expectedCount);
        if (managerComplete)
            return true;

        TArray<FName> itemIds;
        TArray<FPBItem> runtimeItems;
        itemIds.Reserve(expectedCount);
        runtimeItems.Reserve(expectedCount);

        for (const auto& row : itemTypeTable->RowMap)
        {
            const FName itemId = row.Key();
            if (itemId.ComparisonIndex <= 0)
                continue;

            itemIds.Add(itemId);
            FPBItem item{};
            item.ID = itemId;
            item.Count = 1;
            item.bIsNew = false;
            runtimeItems.Add(item);
        }

        if (itemIds.Num() != expectedCount ||
            runtimeItems.Num() != expectedCount)
        {
            return false;
        }

        const int previousManagerCount =
            armoryManager->Armorys.OwnedItems.Num();
        armoryManager->Armorys.OwnedItems = runtimeItems;
        armoryManager->Armorys.NewItemCounter = 0;

        if (armoryManager->DefaultConfig)
            armoryManager->DefaultConfig->OwnedItems = itemIds;

        if (persistentUser)
        {
            persistentUser->ArmorySaved.OwnedItems = itemIds;
            persistentUser->Armorys.OwnedItems = runtimeItems;
            persistentUser->Armorys.NewItemCounter = 0;
        }

        std::ostringstream message;
        message << "[ARMORY] Native ownership synchronized: "
                << previousManagerCount << " -> " << expectedCount
                << " items";
        ClientLog(message.str());
        return true;
    }
}

void ResetClientArmorySync()
{
    nextAttemptAt = {};
}

void PumpClientArmorySync()
{
    const auto now = std::chrono::steady_clock::now();
    if (now < nextAttemptAt)
        return;
    nextAttemptAt = now + RetryInterval;
    SynchronizeClientArmory();
}

void PrepareClientArmoryEntry()
{
    nextAttemptAt = std::chrono::steady_clock::now() + RetryInterval;
    SynchronizeClientArmory();
}
