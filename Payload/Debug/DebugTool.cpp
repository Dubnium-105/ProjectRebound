#include "DebugTool.h"
#include "Debug.h"
#include <Windows.h>
#include <iostream>

void DebugTool::ExecuteHotkey(int vkCode)
{
    switch (vkCode)
    {
    case VK_F6:
        ClientLog("[DEBUG] F6: Dump subsystems");
        DebugDumpSubsystemsToFile();
        break;
    case VK_F7:
        ClientLog("[DEBUG] F7: Dump weapon parts");
        DebugDumpWeaponPartsToFile();
        break;
    default:
        break;
    }
}

void DebugTool::ExecuteChat(const std::string& payload)
{
    ClientLog("[DEBUG] Chat command: " + payload);
}

nlohmann::json DebugTool::ExecuteJson(const nlohmann::json& args)
{
    ClientLog("[DEBUG] JSON command: " + args.dump());
    return nlohmann::json{{"ok", true}};
}
