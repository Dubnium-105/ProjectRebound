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

namespace
{
    struct BoundaryWindowSearch
    {
        DWORD ProcessId = 0;
        HWND Window = nullptr;
    };

    BOOL CALLBACK FindBoundaryGameWindow(HWND window, LPARAM parameter)
    {
        auto* const search = reinterpret_cast<BoundaryWindowSearch*>(parameter);
        if (!search || !IsWindowVisible(window))
            return TRUE;

        DWORD processId = 0;
        GetWindowThreadProcessId(window, &processId);
        if (processId != search->ProcessId)
            return TRUE;

        wchar_t className[128]{};
        GetClassNameW(window, className, static_cast<int>(_countof(className)));
        if (_wcsicmp(className, L"UnrealWindow") == 0)
        {
            search->Window = window;
            return FALSE;
        }

        // Keep a title-bound fallback for the pinned build, but never select
        // the AllocConsole window whose title is the executable path.
        wchar_t title[256]{};
        GetWindowTextW(window, title, static_cast<int>(_countof(title)));
        if (_wcsicmp(className, L"ConsoleWindowClass") != 0 &&
            wcsstr(title, L"Boundary") != nullptr)
        {
            search->Window = window;
            return FALSE;
        }
        return TRUE;
    }
}

// Deliver auto-login only to this process's Unreal window. A debug console is
// intentionally created by -debuglog and can otherwise steal foreground
// SendInput, leaving the actual EnterGame widget untouched.
void PressSpace()
{
    BoundaryWindowSearch search{GetCurrentProcessId(), nullptr};
    EnumWindows(FindBoundaryGameWindow, reinterpret_cast<LPARAM>(&search));
    if (!search.Window)
        return;

    const DWORD currentThread = GetCurrentThreadId();
    const DWORD windowThread = GetWindowThreadProcessId(search.Window, nullptr);
    const bool attached = currentThread != windowThread && windowThread != 0 &&
        AttachThreadInput(currentThread, windowThread, TRUE) != FALSE;
    ShowWindow(search.Window, SW_RESTORE);
    BringWindowToTop(search.Window);
    SetForegroundWindow(search.Window);
    SetFocus(search.Window);
    if (attached)
        AttachThreadInput(currentThread, windowThread, FALSE);
    if (GetForegroundWindow() != search.Window)
        return;

    INPUT input{};
    input.type = INPUT_KEYBOARD;
    input.ki.wVk = VK_SPACE;
    if (SendInput(1, &input, sizeof(INPUT)) != 1)
        return;
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
