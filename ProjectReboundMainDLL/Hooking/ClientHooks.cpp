// ClientHooks.cpp
// Client-side hooks: ProcessEventHookClient, ClientDeathCrashHook.

#include "ClientHooks.h"
#include "HookCore.h"
#include "../Logging/LogManager.h"
#include "../Client/SideMountFixClient.h"
#include "../Client/UIShake.h"
#include "../Client/AutoConnect.h"
#include "../API/APIInternal.h"
#include "../Loadout/LoadoutManager.h"
#include <Windows.h>
#include <chrono>
#include <thread>

using namespace SDK;

extern LoadoutManager* gLoadoutManager;

static thread_local int gClientProcessEventSuppressionDepth = 0;

extern "C" void PayloadPushClientProcessEventSuppression()
{
    ++gClientProcessEventSuppressionDepth;
}

extern "C" void PayloadPopClientProcessEventSuppression()
{
    if (gClientProcessEventSuppressionDepth > 0)
        --gClientProcessEventSuppressionDepth;
}

// ======================================================
//  ProcessEventHookClient — client-side dispatch
// ======================================================

void ProcessEventHookClient(UObject *Object, UFunction *Function, void *Parms)
{
    if (gClientProcessEventSuppressionDepth > 0)
    {
        ProcessEventClient.call(Object, Function, Parms);
        return;
    }

    const FCachedProcessEventInfo& EventInfo = GetProcessEventInfo(Function);

    if (gLoadoutManager)
    {
        static auto nextClientTick = std::chrono::steady_clock::now();
        const auto now = std::chrono::steady_clock::now();
        if (now >= nextClientTick)
        {
            nextClientTick = now + std::chrono::seconds(1);
            gLoadoutManager->TickClient();
        }
        gLoadoutManager->OnClientProcessEventPre(Object, EventInfo.FullName, Parms);
    }

    // Froce space to login
    if (EventInfo.ClientKind == EClientProcessEventKind::EnterGameConstruct)
    {
        ClientDebugLog("[LOGIN] EnterGame Construct forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000); // small delay so widget is fully active
                PressSpace(); })
            .detach();
    }

    if (EventInfo.ClientKind == EClientProcessEventKind::EnterGameActivated)
    {
        ClientDebugLog("[LOGIN] EnterGame Activated forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000);
                PressSpace(); })
            .detach();
    }

    // Detect login complete via MainMenuBase Construct
    if (EventInfo.ClientKind == EClientProcessEventKind::MainMenuConstruct)
    {
        if (!LoginCompleted)
        {
            LoginCompleted = true;
            if (gLoadoutManager)
                gLoadoutManager->NotifyMenuConstructed();
        }
    }

    if (EventInfo.ClientKind == EClientProcessEventKind::ConnectMatchServerTimeout)
    {
        const std::string objectName = Object ? std::string(Object->GetFullName()) : "NULL";
        ClientDebugLog("[PE] " + objectName + " - " + EventInfo.FullName);

        ConnectToMatch();
    }

    // --- Launcher event handling (delegated to SideMountFixClient) ---
    if (Object && Object->IsA(APBLauncher::StaticClass()))
    {
        if (HandleLauncherClientEvent(Object, Function, Parms, EventInfo.FullName))
            return;
    }

    // --- Projectile event handling (delegated to SideMountFixClient) ---
    if (Object && Object->IsA(APBProjectile::StaticClass()))
    {
        HandleProjectileClientEvent(Object, EventInfo.FullName);
    }

    // --- Sprint shake diagnostic (UIShake) ---
    if (Object && Object->IsA(APBCharacter::StaticClass()))
    {
        HandleUICharacterClientEvent(Object, Function, Parms, EventInfo.FullName);
    }

    ProcessEventClient.call(Object, Function, Parms);

    if (gLoadoutManager)
        gLoadoutManager->OnClientProcessEventPost(Object, EventInfo.FullName, Parms);
}

// ======================================================
//  ClientDeathCrash — prevent crash on client death
// ======================================================

__int64 ClientDeathCrashHook(__int64 a1)
{
    return 0;
}
