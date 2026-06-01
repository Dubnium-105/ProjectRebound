#include "SideMountFixClient.h"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Logging/LogManager.h"
#include <iostream>
#include <string>
#include <unordered_set>

using namespace SDK;

// ======================================================
// Client-side launcher event handler
// ======================================================

bool HandleLauncherClientEvent(UObject* Object, UFunction* Function, void* Parms,
                               const std::string& funcName)
{
    auto* Pod = static_cast<APBLauncher*>(Object);

    // --------------------------------------------------
    // OnRep_PendingState: state machine fix.
    // Native OnRep_PendingState skips state transitions + flag clearing when
    // IsLocallyControlled() returns false (DS client). We force both here.
    // --------------------------------------------------
    if (funcName.find("OnRep_PendingState") != std::string::npos)
    {
        uint8 pending = static_cast<uint8>(Pod->PendingState);
        uint8 current = static_cast<uint8>(Pod->CurrentState);
        ClientDebugLog("[POD-CLIENT-REP] OnRep_PendingState  PendingState=" + std::to_string((int)pending)
                  + " CurrentState=" + std::to_string((int)current));

        if (pending != current)
        {
            Pod->CurrentState = static_cast<EPBLauncherState>(pending);
            ClientDebugLog("[POD-CLIENT-FIX] CurrentState " + std::to_string((int)current)
                      + "->" + std::to_string((int)pending));
        }

        switch (pending)
        {
        case 0: // Standby — fire complete, clear flags that would be stuck at 1
            Pod->bIsFiring = false;
            Pod->bPendingFiring = false;
            Pod->BurstCounter = 0;
            Pod->bIsFireControlEnabled = false;
            Pod->K2_Standby();
            // Hide aim line (server may skip Undeploy state, collapsing 2→0 same tick)
            if (Pod->IsA(APBLauncher_Deploy_BP_C::StaticClass()))
            {
                auto* DP = static_cast<APBLauncher_Deploy_BP_C*>(Pod);
                if (DP->ProjectilePathTracer)
                    DP->ProjectilePathTracer->OnHidden_Event();
            }
            break;
        case 1: Pod->K2_Deploying();   break;
        case 2:
            Pod->K2_Undeploying();
            if (Pod->IsA(APBLauncher_Deploy_BP_C::StaticClass()))
            {
                auto* DP = static_cast<APBLauncher_Deploy_BP_C*>(Pod);
                if (DP->ProjectilePathTracer)
                    DP->ProjectilePathTracer->OnHidden_Event();
            }
            break;
        case 3: // Ready — ensure clean slate before BP checks bIsFiring
            Pod->bIsFiring = false;
            Pod->bPendingFiring = false;
            Pod->K2_Ready();
            break;
        case 4: // Reloading — snapshot/motion-sensor launchers override
                // K2_ASingleAmmoReloaded to show the FakeProjectileMesh (dummy round).
                // Parent default is a no-op, safe to call on all launcher types.
            Pod->K2_Reloading();
            Pod->K2_ASingleAmmoReloaded();
            break;
        case 5: Pod->K2_Handup();      break;
        }

        return false;
    }

    // --------------------------------------------------
    // ServerFiring: block dud calls from the BP's async retrigger timer.
    // The native fire code sets a 0.25s retrigger timer. When it fires, the
    // clip is already empty (consumed by the first ServerFiring). Without this
    // block, the dud creates a zero-velocity projectile that clutters the client.
    // --------------------------------------------------
    if (funcName.find("ServerFiring") != std::string::npos)
    {
        if (!Pod->HasInfiniteAmmo() && Pod->Magazine.AmmoInClip == 0)
        {
            ClientDebugLog("[POD-CLIENT-FIRE] BLOCKED - empty clip");
            return true;
        }
        ClientDebugLog("[POD-CLIENT-FIRE] AmmoInClip=" + std::to_string(Pod->Magazine.AmmoInClip)
                  + " TotalAmmo=" + std::to_string(Pod->Magazine.TotalAmmo)
                  + " State=" + std::to_string((int)Pod->CurrentState));
        return false;
    }

    // --------------------------------------------------
    // K2_Ready: diagnostic — last chance before BP calls ServerFiring.
    // --------------------------------------------------
    if (funcName.find("K2_Ready") != std::string::npos)
    {
        ClientDebugLog("[POD-CLIENT-READY] " + Pod->GetFullName()
                  + "  AmmoInClip=" + std::to_string(Pod->Magazine.AmmoInClip)
                  + " TotalAmmo=" + std::to_string(Pod->Magazine.TotalAmmo)
                  + " HasAmmo=" + std::to_string(Pod->HasAmmoInClip())
                  + " bIsFiring=" + std::to_string((int)Pod->bIsFiring)
                  + " bPendingFiring=" + std::to_string((int)Pod->bPendingFiring)
                  + " BurstCtr=" + std::to_string(Pod->BurstCounter)
                  + " State=" + std::to_string((int)Pod->CurrentState));
        return false;
    }

    // --------------------------------------------------
    // Visual effects
    // --------------------------------------------------
    if (funcName.find("K2_Fired") != std::string::npos || funcName.find("K2_SimuilateFire") != std::string::npos)
    {
        ClientDebugLog("[POD-CLIENT-VFX] " + funcName);
        return false;
    }

    // --------------------------------------------------
    // OnRep diagnostics
    // --------------------------------------------------
    if (funcName.find("OnRep_Magazine") != std::string::npos)
    {
        ClientDebugLog("[POD-CLIENT-REP] OnRep_Magazine  AmmoInClip=" + std::to_string(Pod->Magazine.AmmoInClip)
                  + " TotalAmmo=" + std::to_string(Pod->Magazine.TotalAmmo));
        return false;
    }

    if (funcName.find("OnRep_") != std::string::npos)
    {
        ClientDebugLog("[POD-CLIENT-REP] " + funcName);
        return false;
    }

    ClientDebugLog("[POD-CLIENT] " + funcName);
    return false;
}

// ======================================================
// Client-side projectile event handler
// ======================================================

void HandleProjectileClientEvent(UObject* Object, const std::string& funcName)
{
    auto* P = static_cast<APBProjectile*>(Object);

    if (funcName.find("OnRep_") != std::string::npos)
    {
        ClientDebugLog("[PROJ-CLIENT] " + funcName
                  + "  Vel=" + std::to_string(P->MovementComp ? P->MovementComp->Velocity.X : 0)
                  + "," + std::to_string(P->MovementComp ? P->MovementComp->Velocity.Y : 0)
                  + "," + std::to_string(P->MovementComp ? P->MovementComp->Velocity.Z : 0)
                  + "  Speed=" + std::to_string(P->GetSpeed())
                  + "  bExploded=" + std::to_string((int)P->bExploded)
                  + "  bReplicates=" + std::to_string((int)P->bReplicates)
                  + "  LifeSpan=" + std::to_string(P->InitialLifeSpan));
    }

    // OnRep_Exploded fires but the BP handler (MulticastExplode/K2_Explode) has
    // IsLocallyControlled checks that skip visual effects on the DS client.
    // Force MulticastExplode to play explosion particles / impact templates.
    if (funcName.find("OnRep_Exploded") != std::string::npos && P->bExploded)
    {
        ClientDebugLog("[PROJ-CLIENT-FIX] Forcing MulticastExplode");
        FHitResult DummyHit{};
        P->MulticastExplode(DummyHit);
    }
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
