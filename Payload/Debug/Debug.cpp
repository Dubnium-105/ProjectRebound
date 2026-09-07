// Debug.cpp
#include "Debug.h"
#include "DebugTool.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Utility/Utility.h"
#include <Windows.h>
#include <iostream>
#include <fstream>
#include <chrono>
#include <sstream>
#include <iomanip>
#include <cstdio>

using namespace SDK;

bool ClientDebugLogEnabled = false;
std::ofstream clientLogFile;

std::string CurrentTimestamp()
{
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);

    std::tm tm{};
    localtime_s(&tm, &t);

    std::ostringstream oss;
    oss << std::put_time(&tm, "%Y%m%d_%H%M%S");
    return oss.str();
}

void Log(const std::string& msg)
{
    std::cout << msg << std::endl;
}

void ClientLog(const std::string& msg)
{
    std::cout << msg << std::endl;

    if (ClientDebugLogEnabled && clientLogFile.is_open())
    {
        clientLogFile << msg << std::endl;
        clientLogFile.flush();
    }
}

void InitDebugConsole()
{
    AllocConsole();

    setvbuf(stdout, NULL, _IONBF, 0);

    FILE* fDummy;
    freopen_s(&fDummy, "CONOUT$", "w", stdout);
    freopen_s(&fDummy, "CONOUT$", "w", stderr);

    std::wcout.clear();
    std::cout.clear();

    std::cout << "[DEBUG] Console initialized" << std::endl;
}

void EnableUnrealConsole()
{
    // The pinned executable also enters this helper on a dedicated bootstrap.
    // Dedicated worlds have no viewport, so the old unconditional assignment
    // at UGameViewportClient::ViewportConsole (offset 0x40) crashed before
    // the strict listener could fail closed.  Keep the debug console as an
    // optional client facility and leave the authority path alive when the
    // native frontend objects do not exist.
    UEngine* const engine = UEngine::GetEngine();
    if (!engine || !engine->GameViewport || !engine->ConsoleClass)
    {
        ClientLog("[DEBUG] Unreal Console unavailable: no game viewport");
        return;
    }

    SDK::UInputSettings* const inputSettings =
        SDK::UInputSettings::GetDefaultObj();
    if (!inputSettings || inputSettings->ConsoleKeys.Num() == 0)
    {
        ClientLog("[DEBUG] Unreal Console unavailable: no console key settings");
        return;
    }

    inputSettings->ConsoleKeys[0].KeyName =
        SDK::UKismetStringLibrary::Conv_StringToName(L"F2");

    SDK::UObject* const newObject =
        SDK::UGameplayStatics::SpawnObject(
            engine->ConsoleClass,
            engine->GameViewport);
    if (!newObject)
    {
        ClientLog("[DEBUG] Unreal Console unavailable: spawn failed");
        return;
    }

    engine->GameViewport->ViewportConsole =
        static_cast<SDK::UConsole*>(newObject);

    ClientLog("[DEBUG] Unreal Console => F2");
}

void DebugLocateSubsystems()
{
    std::cout << "\nLocating Subsystems\n";

    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    if (!armories.empty())
        std::cout << "[FOUND] UPBArmoryManager at " << armories.back() << std::endl;
    else
        std::cout << "[MISSING] UPBArmoryManager" << std::endl;

    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    if (!fieldMods.empty())
        std::cout << "[FOUND] UPBFieldModManager at " << fieldMods.back() << std::endl;
    else
        std::cout << "[MISSING] UPBFieldModManager" << std::endl;

    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (!partMgrs.empty())
        std::cout << "[FOUND] UPBWeaponPartManager at " << partMgrs.back() << std::endl;
    else
        std::cout << "[MISSING] UPBWeaponPartManager" << std::endl;

    std::cout << "END PHASE 1.1\n\n";
}

void DebugDumpSubsystemsToFile()
{
    std::ofstream out("subsystems_dump.txt", std::ios::trunc);
    if (!out.is_open())
        return;

    out << "=== SUBSYSTEM DUMP ===\n\n";

    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    if (!armories.empty())
    {
        UPBArmoryManager* Armory = (UPBArmoryManager*)armories.back();
        out << "[UPBArmoryManager] " << Armory << "\n";

        out << "  Armorys.OwnedItems:\n";
        for (int i = 0; i < Armory->Armorys.OwnedItems.Num(); ++i)
        {
            const FPBItem& item = Armory->Armorys.OwnedItems[i];
            std::string id = item.ID.ToString();

            out << "    [" << i << "] ID=" << id
                << " Count=" << item.Count
                << " bIsNew=" << (item.bIsNew ? "true" : "false") << "\n";
        }
        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBArmoryManager\n\n";
    }

    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    if (!fieldMods.empty())
    {
        UPBFieldModManager* FieldMod = (UPBFieldModManager*)fieldMods.back();
        out << "[UPBFieldModManager] " << FieldMod << "\n";

        out << "  CharacterPreOrderingInventoryConfigs:\n";
        for (auto& pair : FieldMod->CharacterPreOrderingInventoryConfigs)
        {
            std::string roleId = pair.Key().ToString();
            const FPBInventoryNetworkConfig& cfg = pair.Value();

            out << "    RoleID=" << roleId << "\n";

            for (int i = 0; i < cfg.CharacterSlots.Num(); ++i)
            {
                int slot = (int)cfg.CharacterSlots[i];
                std::string itemId = "";

                if (i < cfg.InventoryItems.Num())
                    itemId = cfg.InventoryItems[i].ToString();

                out << "      Slot[" << i << "] Type=" << slot
                    << " Item=" << itemId << "\n";
            }

            out << "\n";
        }
        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBFieldModManager\n\n";
    }

    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (!partMgrs.empty())
    {
        UPBWeaponPartManager* PartMgr = (UPBWeaponPartManager*)partMgrs.back();
        out << "[UPBWeaponPartManager] " << PartMgr << "\n";

        out << "  WeaponSlotMap (keys only):\n";
        for (auto& pair : PartMgr->WeaponSlotMap)
        {
            APBWeapon* weapon = pair.Key();
            std::string name = weapon ? weapon->GetFullName() : "NULL";

            out << "    Weapon=" << name << "\n";
        }

        out << "\n";
    }
    else
    {
        out << "[MISSING] UPBWeaponPartManager\n\n";
    }

    out << "=== END SUBSYSTEM DUMP ===\n";
    out.close();
}

void DebugDumpWeaponPartsToFile()
{
    std::ofstream out("weapon_parts_dump.txt", std::ios::trunc);
    if (!out.is_open())
        return;

    out << "=== WEAPON PARTS DUMP ===\n\n";

    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (partMgrs.empty())
    {
        out << "[MISSING] UPBWeaponPartManager\n";
        return;
    }

    UPBWeaponPartManager* PartMgr = (UPBWeaponPartManager*)partMgrs.back();
    out << "[UPBWeaponPartManager] " << PartMgr << "\n\n";

    out << "WeaponSlotMap:\n";

    for (auto& pair : PartMgr->WeaponSlotMap)
    {
        APBWeapon* weapon = pair.Key();
        FWeaponSlotPartInfo info = pair.Value();

        std::string weaponName = weapon ? weapon->GetFullName() : "NULL";
        out << "  Weapon=" << weaponName << "\n";

        for (auto& kvp : info.TypePartMap)
        {
            EPBPartSlotType slotType = kvp.Key();
            UPartDataHolderComponent* holder = kvp.Value();

            std::string partId = "NONE";
            if (holder)
            {
                FName id = holder->GetPartID();
                partId = id.ToString();
            }

            out << "    SlotType=" << (int)slotType
                << " PartID=" << partId << "\n";
        }

        out << "\n";
    }

    out << "=== END WEAPON PARTS DUMP ===\n";
    out.close();
}

void HotkeyThread()
{
    while (true)
    {
        if (GetAsyncKeyState(VK_F5) & 0x8000)
        {
            DebugDumpSubsystemsToFile();
            ClientLog("[CLIENT] Auto-enter Shooting Range...");
            DebugDumpWeaponPartsToFile();
            ClientLog("[CLIENT] Auto-enter Shooting Range...");
            Sleep(300);
        }

        if (GetAsyncKeyState(VK_F9) & 0x8000)
        {
            UPBLocalPlayer* LP = nullptr;
            auto* GI = UWorld::GetWorld()->OwningGameInstance;

            if (GI && GI->LocalPlayers.Num() > 0)
            {
                LP = (UPBLocalPlayer*)GI->LocalPlayers[0];
                if (LP)
                {
                    ClientLog("[CLIENT] Auto-enter Shooting Range...");
                    LP->GoToRange(0.0f);
                }
            }
            Sleep(300);
        }
        Sleep(10);
    }
}

void HotkeyThreadWithDebugTool()
{
    while (true)
    {
        if (GetAsyncKeyState(VK_F5) & 0x8000)
        {
            DebugDumpSubsystemsToFile();
            DebugDumpWeaponPartsToFile();
            ClientLog("[CLIENT] Dumped subsystems and weapon parts to file");
            Sleep(300);
        }

        if (GetAsyncKeyState(VK_F9) & 0x8000)
        {
            UPBLocalPlayer* LP = nullptr;
            auto* GI = UWorld::GetWorld()->OwningGameInstance;

            if (GI && GI->LocalPlayers.Num() > 0)
            {
                LP = (UPBLocalPlayer*)GI->LocalPlayers[0];
                if (LP)
                {
                    ClientLog("[CLIENT] Auto-enter Shooting Range...");
                    LP->GoToRange(0.0f);
                }
            }
            Sleep(300);
        }

        Sleep(10);
    }
}
