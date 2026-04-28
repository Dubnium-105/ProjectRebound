// Utility.h
#pragma once
#include <vector>
#include "../SDK.hpp"

std::vector<SDK::UObject *> getObjectsOfClass(SDK::UClass *theClass, bool includeDefault);
SDK::UObject *GetLastOfType(SDK::UClass *theClass, bool includeDefault);
void PressSpace();

// Shared helpers for subsystem access
namespace SDK
{
    class UPBFieldModManager;
    class APBPlayerController;
    class APBCharacter;
}
SDK::UPBFieldModManager* GetFieldModManager();
SDK::APBPlayerController* GetLocalPlayerController();
SDK::APBCharacter* GetLocalCharacter();