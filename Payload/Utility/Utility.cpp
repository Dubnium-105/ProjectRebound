// Utility.cpp
#include "Utility.h"
#include <Windows.h>
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"

std::vector<SDK::UObject *> getObjectsOfClass(SDK::UClass *theClass, bool includeDefault)
{
    std::vector<SDK::UObject *> ret = std::vector<SDK::UObject *>();

    for (int i = 0; i < SDK::UObject::GObjects->Num(); i++)
    {
        SDK::UObject *Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject() && !includeDefault)
            continue;

        if (Obj->IsA(theClass))
        {
            ret.push_back(Obj);
        }
    }

    return ret;
}

SDK::UObject *GetLastOfType(SDK::UClass *theClass, bool includeDefault)
{
    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
    {
        SDK::UObject *Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject() && !includeDefault)
            continue;

        if (Obj->IsA(theClass))
        {
            return Obj;
        }
    }

    return nullptr;
}

// Force press space when autoconnect so it wont stuck to wait for player to press
void PressSpace()
{
    INPUT input{};
    input.type = INPUT_KEYBOARD;
    input.ki.wVk = VK_SPACE;

    SendInput(1, &input, sizeof(INPUT));

    // Key up
    input.ki.dwFlags = KEYEVENTF_KEYUP;
    SendInput(1, &input, sizeof(INPUT));
}

SDK::UPBFieldModManager* GetFieldModManager()
{
    SDK::UObject* object = GetLastOfType(SDK::UPBFieldModManager::StaticClass(), false);
    return object ? static_cast<SDK::UPBFieldModManager*>(object) : nullptr;
}

SDK::APBPlayerController* GetLocalPlayerController()
{
    SDK::UWorld* world = SDK::UWorld::GetWorld();
    if (!world || !world->OwningGameInstance)
        return nullptr;

    for (SDK::UObject* object : getObjectsOfClass(SDK::APBPlayerController::StaticClass(), false))
    {
        SDK::APBPlayerController* pc = static_cast<SDK::APBPlayerController*>(object);
        if (pc && pc->PBGameInstance == world->OwningGameInstance)
            return pc;
    }
    return nullptr;
}

SDK::APBCharacter* GetLocalCharacter()
{
    SDK::APBPlayerController* pc = GetLocalPlayerController();
    if (pc && pc->PBCharacter)
        return pc->PBCharacter;
    return nullptr;
}