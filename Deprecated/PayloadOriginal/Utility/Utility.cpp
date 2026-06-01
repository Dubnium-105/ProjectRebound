// Utility.cpp
#include "Utility.h"
#include "../ServerLogic/ServerLogic.h"
#include <Windows.h>
#include <iostream>
#include "../SDK/Engine_parameters.hpp"

std::vector<SDK::UObject *> getObjectsOfClass(SDK::UClass *theClass, bool includeDefault)
{
    std::vector<SDK::UObject *> ret = std::vector<SDK::UObject *>();

    if (IsServerShutdownRequested()) {
        OutputDebugStringA("[EXIT-GUARD] getObjectsOfClass skipped (shutdown)\n");
        return ret;
    }

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
    if (IsServerShutdownRequested()) {
        OutputDebugStringA("[EXIT-GUARD] GetLastOfType skipped (shutdown)\n");
        return nullptr;
    }

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