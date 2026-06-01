// Debug.cpp
// LogManager — single worker thread drains a lock-free-ish queue and writes
// to stdout.  All four log functions push into this queue; the thread does
// the actual I/O so the game thread never blocks on pipe writes.
#include "Debug.h"
#include "../Utility/Utility.h"
#include <iostream>
#include <chrono>
#include <deque>
#include <iomanip>
#include <mutex>
#include <condition_variable>
#include <sstream>
#include <filesystem>
#include <thread>
#include <Windows.h>
#include <cstdio>
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"

using namespace SDK;

// ---- globals ---------------------------------------------------------------

std::mutex LogMutex;
std::string LogFilePath;
bool ServerDebugLogEnabled = false;
bool ClientDebugLogEnabled = false;
std::ofstream clientLogFile;

// ---- LogManager (thread + queue) ------------------------------------------

namespace
{
    struct LogEntry
    {
        std::string msg;
        bool immediate;  // flush right after this entry (ServerLog / ClientLog)
    };

    std::deque<LogEntry> g_Queue;
    std::mutex g_QueueMutex;
    std::condition_variable g_QueueCv;
    std::thread g_Worker;
    bool g_WorkerRunning = false;

    void WorkerLoop()
    {
        std::deque<LogEntry> local;
        int sinceFlush = 0;

        while (true)
        {
            // Drain the shared queue into a local deque.
            {
                std::unique_lock<std::mutex> lock(g_QueueMutex);
                g_QueueCv.wait(lock, [] {
                    return !g_Queue.empty() || !g_WorkerRunning;
                });

                if (g_Queue.empty() && !g_WorkerRunning)
                    return;

                local.swap(g_Queue);
            }

            // Write entries from local deque.
            for (auto &entry : local)
            {
                std::cout << entry.msg << "\n";
                ++sinceFlush;

                if (entry.immediate || sinceFlush >= 30)
                {
                    std::cout << std::flush;
                    sinceFlush = 0;
                }
            }
            local.clear();
        }
    }

    void EnsureLogThread()
    {
        if (!g_WorkerRunning)
        {
            g_WorkerRunning = true;
            g_Worker = std::thread(WorkerLoop);
        }
    }

    void ShutdownLogThread()
    {
        {
            std::lock_guard<std::mutex> lock(g_QueueMutex);
            g_WorkerRunning = false;
        }
        g_QueueCv.notify_one();
        if (g_Worker.joinable())
            g_Worker.join();
    }

    void PushEntry(const std::string &msg, bool immediate)
    {
        EnsureLogThread();
        {
            std::lock_guard<std::mutex> lock(g_QueueMutex);
            g_Queue.push_back({msg, immediate});
        }
        g_QueueCv.notify_one();
    }
}

// ---- timestamp helper ------------------------------------------------------

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

// ---- public API ------------------------------------------------------------

void ServerLog(const std::string &msg)
{
    PushEntry(msg, true);  // always-on, immediate flush
}

void ServerDebugLog(const std::string &msg)
{
    if (!ServerDebugLogEnabled) return;
    PushEntry(msg, false); // diagnostic, batch flush
}

void ClientLog(const std::string &msg)
{
    PushEntry(msg, true);  // always-on client, immediate flush
}

void ClientDebugLog(const std::string &msg)
{
    if (!ClientDebugLogEnabled) return;

    PushEntry(msg, false); // diagnostic, batch flush

    if (clientLogFile.is_open())
        clientLogFile << msg << "\n";
}
void InitDebugConsole()
{
    AllocConsole();

    // Disable buffering
    setvbuf(stdout, NULL, _IONBF, 0);

    // Redirect stdout manually
    FILE *fDummy;
    freopen_s(&fDummy, "CONOUT$", "w", stdout);
    freopen_s(&fDummy, "CONOUT$", "w", stderr);

    std::wcout.clear();
    std::cout.clear();

    ClientLog("[DEBUG] Console initialized");
}

void EnableUnrealConsole()
{
    SDK::UInputSettings::GetDefaultObj()->ConsoleKeys[0].KeyName =
        SDK::UKismetStringLibrary::Conv_StringToName(L"F2");

    /* Creates a new UObject of class-type specified by Engine->ConsoleClass */
    SDK::UObject *NewObject =
        SDK::UGameplayStatics::SpawnObject(
            UEngine::GetEngine()->ConsoleClass,
            UEngine::GetEngine()->GameViewport);

    /* The Object we created is a subclass of UConsole, so this cast is **safe**. */
    UEngine::GetEngine()->GameViewport->ViewportConsole =
        static_cast<SDK::UConsole *>(NewObject);

    ClientDebugLog("[DEBUG] Unreal Console => F2");
}

void DebugLocateSubsystems()
{
    ClientDebugLog("Locating Subsystems");

    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    ClientDebugLog(armories.empty() ? "[MISSING] UPBArmoryManager"
        : ("[FOUND] UPBArmoryManager at " + std::to_string(reinterpret_cast<uintptr_t>(armories.back()))));

    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    ClientDebugLog(fieldMods.empty() ? "[MISSING] UPBFieldModManager"
        : ("[FOUND] UPBFieldModManager at " + std::to_string(reinterpret_cast<uintptr_t>(fieldMods.back()))));

    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    ClientDebugLog(partMgrs.empty() ? "[MISSING] UPBWeaponPartManager"
        : ("[FOUND] UPBWeaponPartManager at " + std::to_string(reinterpret_cast<uintptr_t>(partMgrs.back()))));

    ClientDebugLog("END PHASE 1.1");
}

void DebugDumpSubsystemsToFile()
{
    std::ofstream out("subsystems_dump.txt", std::ios::trunc);
    if (!out.is_open())
        return;

    out << "=== SUBSYSTEM DUMP ===\n\n";

    // ----------------------------------------------------
    // 1) Armory Manager
    // ----------------------------------------------------
    auto armories = getObjectsOfClass(UPBArmoryManager::StaticClass(), false);
    if (!armories.empty())
    {
        UPBArmoryManager *Armory = (UPBArmoryManager *)armories.back();
        out << "[UPBArmoryManager] " << Armory << "\n";

        out << "  Armorys.OwnedItems:\n";
        for (int i = 0; i < Armory->Armorys.OwnedItems.Num(); ++i)
        {
            const FPBItem &item = Armory->Armorys.OwnedItems[i];
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

    // ----------------------------------------------------
    // 2) Field Mod Manager
    // ----------------------------------------------------
    auto fieldMods = getObjectsOfClass(UPBFieldModManager::StaticClass(), false);
    if (!fieldMods.empty())
    {
        UPBFieldModManager *FieldMod = (UPBFieldModManager *)fieldMods.back();
        out << "[UPBFieldModManager] " << FieldMod << "\n";

        out << "  CharacterPreOrderingInventoryConfigs:\n";
        for (auto &pair : FieldMod->CharacterPreOrderingInventoryConfigs)
        {
            // Correct SDK access: Key() and Value()
            std::string roleId = pair.Key().ToString();
            const FPBInventoryNetworkConfig &cfg = pair.Value();

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

    // ----------------------------------------------------
    // 3) Weapon Part Manager
    // ----------------------------------------------------
    auto partMgrs = getObjectsOfClass(UPBWeaponPartManager::StaticClass(), false);
    if (!partMgrs.empty())
    {
        UPBWeaponPartManager *PartMgr = (UPBWeaponPartManager *)partMgrs.back();
        out << "[UPBWeaponPartManager] " << PartMgr << "\n";

        out << "  WeaponSlotMap (keys only):\n";
        for (auto &pair : PartMgr->WeaponSlotMap)
        {
            // Correct SDK access: Key() and Value()
            APBWeapon *weapon = pair.Key();
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

    UPBWeaponPartManager *PartMgr = (UPBWeaponPartManager *)partMgrs.back();
    out << "[UPBWeaponPartManager] " << PartMgr << "\n\n";

    out << "WeaponSlotMap:\n";

    for (auto &pair : PartMgr->WeaponSlotMap)
    {
        APBWeapon *weapon = pair.Key();          // <-- FIXED
        FWeaponSlotPartInfo info = pair.Value(); // <-- FIXED

        std::string weaponName = weapon ? weapon->GetFullName() : "NULL";
        out << "  Weapon=" << weaponName << "\n";

        // Iterate TMap<EPBPartSlotType, UPartDataHolderComponent*>
        for (auto &kvp : info.TypePartMap)
        {
            EPBPartSlotType slotType = kvp.Key();           // <-- FIXED
            UPartDataHolderComponent *holder = kvp.Value(); // <-- FIXED

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

// hotkey dump
void HotkeyThread()
{
    while (true)
    {
        // F5 pressed
        if (GetAsyncKeyState(VK_F5) & 0x8000)
        {
            DebugDumpSubsystemsToFile();
            ClientDebugLog("[CLIENT] Auto-enter Shooting Range...");
            DebugDumpWeaponPartsToFile();
            ClientDebugLog("[CLIENT] Auto-enter Shooting Range...");
            // simple debounce so it doesn't spam while held
            Sleep(300);
        }

        // F9 pressed
        if (GetAsyncKeyState(VK_F9) & 0x8000)
        {
            UPBLocalPlayer *LP = nullptr;
            auto *GI = UWorld::GetWorld()->OwningGameInstance;

            if (GI && GI->LocalPlayers.Num() > 0)
            {
                LP = (UPBLocalPlayer *)GI->LocalPlayers[0];
                if (LP)
                {
                    ClientDebugLog("[CLIENT] Auto-enter Shooting Range...");
                    LP->GoToRange(0.0f);
                }
            }
            Sleep(300);
        }
        if (GetAsyncKeyState(VK_F10) & 0x8000)
        {
            try
            {
                // ------------------------------------------------------------
                // 1. Deactivate top menu widget (PBMainMenuManager_BP)
                // ------------------------------------------------------------
                {
                    auto mgrs = getObjectsOfClass(UPBMainMenuManager_BP_C::StaticClass(), false);
                    if (!mgrs.empty())
                    {
                        UPBMainMenuManager_BP_C *mgr = (UPBMainMenuManager_BP_C *)mgrs.back();
                        UCommonActivatableWidget *widget = nullptr;
                        mgr->GetTopMenuWidget(&widget);

                        if (widget)
                        {
                            widget->DeactivateWidget();
                            ClientDebugLog("[CLIENT] F10: Deactivated top menu widget");
                        }
                        else
                        {
                            ClientDebugLog("[CLIENT] F10: No top menu widget");
                        }
                    }
                    else
                    {
                        ClientDebugLog("[CLIENT] F10: No PBMainMenuManager_BP found");
                    }
                }

                // ------------------------------------------------------------
                // 2. Hide LoginGate (the real blocker)
                // ------------------------------------------------------------
                {
                    auto gates = getObjectsOfClass(UUMG_LoginGate_C::StaticClass(), false);
                    for (auto *obj : gates)
                    {
                        UUMG_LoginGate_C *gate = (UUMG_LoginGate_C *)obj;
                        gate->SetVisibility(ESlateVisibility::Collapsed);
                        ClientDebugLog("[CLIENT] F10: Collapsed LoginGate");
                    }
                }
            }
            catch (...)
            {
                ClientDebugLog("[CLIENT] F10 handler failed (exception)");
            }

            Sleep(300);
        }
        Sleep(10);
    }
}