#include "SideMountFixServer.h"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Logging/LogManager.h"
#include <iostream>
#include <string>
#include <unordered_set>

using namespace SDK;

// ======================================================
// Server-side launcher event handler
// ======================================================

bool HandleLauncherServerEvent(UObject* Object, UFunction* Function, void* Parms,
                               const std::string& funcName)
{
    ServerDebugLog("[POD] " + funcName);

    if (funcName.find("ServerFiring") != std::string::npos)
    {
        auto* Pod = static_cast<APBLauncher*>(Object);
        ServerDebugLog("[POD-FIRE] AmmoInClip=" + std::to_string(Pod->Magazine.AmmoInClip)
            + " TotalAmmo=" + std::to_string(Pod->Magazine.TotalAmmo)
            + " bIsFiring=" + std::to_string((int)Pod->bIsFiring)
            + " bPendingFiring=" + std::to_string((int)Pod->bPendingFiring)
            + " BurstCtr=" + std::to_string(Pod->BurstCounter)
            + " bFireCtrl=" + std::to_string((int)Pod->bIsFireControlEnabled)
            + " State=" + std::to_string((int)Pod->CurrentState));

        if (!Pod->HasInfiniteAmmo() && Pod->Magazine.AmmoInClip == 0)
        {
            ServerDebugLog("[POD-FIRE] BLOCKED - empty clip");
            return true;
        }
    }

    if (funcName.find("K2_Standby") != std::string::npos)
    {
        auto* Pod = static_cast<APBLauncher*>(Object);
        ServerDebugLog("[POD-STANDBY] bIsFiring=" + std::to_string((int)Pod->bIsFiring)
            + " bPendingFiring=" + std::to_string((int)Pod->bPendingFiring)
            + " BurstCtr=" + std::to_string(Pod->BurstCounter)
            + " bFireCtrl=" + std::to_string((int)Pod->bIsFireControlEnabled)
            + " State=" + std::to_string((int)Pod->CurrentState));
    }

    return false;
}

// ======================================================
// Debug helpers — gated by -serverdebuglog / -clientdebuglog at runtime.
// Enable to diagnose launcher state, direction, components.
// ======================================================

#if 0 // Debug section — not compiled. Set #if 1 to activate.

// Prints every UActorComponent name + class on a launcher.
// Use when a mesh is blocking the view and you need to identify which component.
// Called once per launcher (static set prevents duplicates).
static void DebugDumpComponents(APBLauncher* Pod)
{
    static std::unordered_set<APBLauncher*> s_Dumped;
    if (s_Dumped.count(Pod)) return;
    s_Dumped.insert(Pod);

    ClientDebugLog("[DEBUG-DUMP] Launcher: " + Pod->GetFullName());
    auto AllComps = Pod->K2_GetComponentsByClass(UActorComponent::StaticClass());
    for (int i = 0; i < AllComps.Num(); i++)
    {
        auto* C = AllComps[i];
        if (C)
            ClientDebugLog("[DEBUG-DUMP]   Comp[" + std::to_string(i) + "]: " + C->GetName()
                      + "  Class=" + C->Class->GetName());
    }
}

// Logs the Origin/ShootDir from ServerFiring RPC parameters (before sending).
// Use to diagnose GetAdjustedAim returning bad direction values when
// IsLocallyControlled() returns false on the DS client.
static void DebugLogServerFiringDirection(APBLauncher* Pod, void* Parms)
{
    auto* fp = static_cast<Params::PBLauncher_ServerFiring*>(Parms);
    ClientDebugLog("[DEBUG-FIRE-DIR] Origin="
              + std::to_string(fp->Origin.X) + "," + std::to_string(fp->Origin.Y) + "," + std::to_string(fp->Origin.Z)
              + "  Dir=" + std::to_string(fp->ShootDir.X) + "," + std::to_string(fp->ShootDir.Y) + "," + std::to_string(fp->ShootDir.Z));
}

// Overrides the ShootDir in ServerFiring params with the player's actual
// control rotation. Use when DebugLogServerFiringDirection shows a fixed/wrong
// direction (e.g. (1,0,0) or (-1,0,0)) instead of the player's aim.
static void DebugOverrideShootDir(APBLauncher* Pod, void* Parms)
{
    APawn* Pwn = Pod->FireComponent ? Pod->FireComponent->GetOwnerPawn() : nullptr;
    if (!Pwn) return;
    AController* Ctrl = Pwn->GetController();
    if (!Ctrl) return;

    auto* fp = static_cast<Params::PBLauncher_ServerFiring*>(Parms);
    FVector NewDir = UKismetMathLibrary::GetForwardVector(Ctrl->GetControlRotation());
    ClientDebugLog("[DEBUG-DIR-FIX] Override: "
              + std::to_string(fp->ShootDir.X) + "," + std::to_string(fp->ShootDir.Y) + "," + std::to_string(fp->ShootDir.Z)
              + " -> " + std::to_string(NewDir.X) + "," + std::to_string(NewDir.Y) + "," + std::to_string(NewDir.Z));
    fp->ShootDir = NewDir;
}

// Logs extended K2_Ready state (BurstCount, bEnableBurst, Role, etc.).
// Use to identify which flag is blocking ServerFiring when BP skips it.
static void DebugLogReadyExtended(APBLauncher* Pod)
{
    ClientDebugLog("[DEBUG-READY] " + Pod->GetFullName()
              + "  AmmoInClip=" + std::to_string(Pod->Magazine.AmmoInClip)
              + " TotalAmmo=" + std::to_string(Pod->Magazine.TotalAmmo)
              + " HasAmmo=" + std::to_string(Pod->HasAmmoInClip())
              + " bIsFiring=" + std::to_string((int)Pod->bIsFiring)
              + " bPendingFiring=" + std::to_string((int)Pod->bPendingFiring)
              + " BurstCtr=" + std::to_string(Pod->BurstCounter)
              + " BurstCnt=" + std::to_string(Pod->FireConfig.BurstCount)
              + " bEnBurst=" + std::to_string((int)Pod->FireConfig.bEnableBurst)
              + " bAutoFire=" + std::to_string((int)Pod->FireConfig.bEnableAutoFire)
              + " bInForce=" + std::to_string((int)Pod->bInForceState)
              + " bFireCtrl=" + std::to_string((int)Pod->bIsFireControlEnabled)
              + " bInProjCtrl=" + std::to_string((int)Pod->bInProjectileControlMode)
              + " bInSpecial=" + std::to_string((int)Pod->bInSpecialMode)
              + " Role=" + std::to_string((int)Pod->Role)
              + " State=" + std::to_string((int)Pod->CurrentState));
}

#endif // Debug section
