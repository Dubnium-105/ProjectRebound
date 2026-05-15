#include "UIFix.h"
#include "../SDK/ProjectBoundary_classes.hpp"
#include "../SDK/Engine_classes.hpp"
#include "../Debug/Debug.h"
#include <string>
#include <unordered_map>
#include <cmath>

using namespace SDK;

// Compile-time toggle. Set to false to strip all UI-FIX logging + fix.
static constexpr bool UIFixDebugEnabled = true;

// ======================================================
// Client-side character event handler — sprint shake fix
// ======================================================

void HandleUICharacterClientEvent(UObject* Object, UFunction* Function, void* Parms,
                                   const std::string& funcName)
{
    if (!UIFixDebugEnabled)
        return;

    // ReceiveTick fires every frame for actors with BP tick via ProcessEvent.
    if (funcName.find("ReceiveTick") == std::string::npos)
        return;

    auto* Char = static_cast<APBCharacter*>(Object);
    if (!Char || !Char->IsLocallyControlled())
        return;

    uint8 bIsRunning = Char->bIsRunning;
    ECharacterStatus charStatus = Char->GetCurrentCharacterStatus();

    // --- Per-object state tracking ---
    static std::unordered_map<UObject*, uint8> s_LastBIsRunning;
    uint8 last = 0xFF;
    {
        auto it = s_LastBIsRunning.find(Object);
        if (it != s_LastBIsRunning.end())
            last = it->second;
    }
    bool bChanged = (last != 0xFF) && (last != bIsRunning);
    s_LastBIsRunning[Object] = bIsRunning;

    if (bChanged)
    {
        ClientDebugLog("[UI-FIX-TRANSITION] " + std::string(Char->GetFullName())
                  + "  bIsRunning " + std::to_string((int)last)
                  + " -> " + std::to_string((int)bIsRunning));
    }

    // --------------------------------------------------
    // CamCache override.
    //
    // The native weapon handling tick writes accumulated shake values into
    // APBPlayerCameraManager::CameraModifiers_Cache each frame.  On a DS
    // client those values are corrupted — they persist (and grow) across
    // sprint cycles and leak into Idle / SlowlyMoving states, whereas on
    // standalone those states always read zero.
    //
    // We bypass the native output entirely and drive the cache ourselves:
    //   - Idle / SlowlyMoving while NOT sprinting → forced to zero.
    //   - Running (bIsRunning==1)                → synthetic shake that
    //     matches the standalone amplitude (~Loc 0.02, ~Rot Yaw 0.06).
    //   - Caught, Dash, Braking, …               → left at the native
    //     value (those code-paths appear correct or are untested).
    // --------------------------------------------------
    AController* Ctrl = Char->GetController();
    if (!Ctrl)
        goto log;

    {
        APlayerController* PC = static_cast<APlayerController*>(Ctrl);
        if (!PC || !PC->PlayerCameraManager)
            goto log;

        auto* CamMgr = static_cast<APBPlayerCameraManager*>(PC->PlayerCameraManager);

        if (bIsRunning == 0)
        {
            // States that should have zero CamCache when not running.
            if (charStatus == ECharacterStatus::Idle ||
                charStatus == ECharacterStatus::SlowlyMoving)
            {
                CamMgr->CameraModifiers_CacheRelativeLocation = {};
                CamMgr->CameraModifiers_CacheRelativateRotation = {};
            }
            // Caught, Dash, Braking etc. — leave native values alone.
        }
        else // bIsRunning == 1
        {
            // Synthetic running shake approximating standalone behaviour.
            // Time is approximated via frame counter (~60 fps) to avoid SDK
            // function-call overhead.  Frequencies are staggered so the three
            // axes do not move in lockstep.
            static int s_ShakeFrame = 0;
            s_ShakeFrame++;
            float t = static_cast<float>(s_ShakeFrame) * 0.016f;

            FVector loc;
            loc.X = std::sin(t * 9.5f)  * 0.015f;
            loc.Y = std::sin(t * 11.2f) * 0.020f;
            loc.Z = std::sin(t * 10.1f) * 0.018f;

            FRotator rot;
            rot.Pitch = std::sin(t * 8.7f)  * 0.04f;
            rot.Yaw   = std::sin(t * 10.8f) * 0.06f;
            rot.Roll  = std::sin(t * 12.3f) * 0.002f;

            CamMgr->CameraModifiers_CacheRelativeLocation = loc;
            CamMgr->CameraModifiers_CacheRelativateRotation = rot;
        }
    }

log:
    // --- Rate limiting (per-object) ---
    static std::unordered_map<UObject*, int> s_FrameCounters;
    int& counter = s_FrameCounters[Object];
    counter++;
    if (counter % 60 != 0 && !bChanged)
        return;

    const char* statusNames[] = {"Idle", "SlowlyMoving", "Running", "Dash", "Braking", "Caught", "StatusMax", "MAX"};

    // CameraModifiers_Cache diagnostic (reads the NEW value after our override)
    std::string cacheStr = "CamCache=?";
    {
        AController* Ctrl2 = Char->GetController();
        if (Ctrl2)
        {
            APlayerController* PC2 = static_cast<APlayerController*>(Ctrl2);
            if (PC2 && PC2->PlayerCameraManager)
            {
                auto* CamMgr = static_cast<APBPlayerCameraManager*>(PC2->PlayerCameraManager);
                FVector loc = CamMgr->CameraModifiers_CacheRelativeLocation;
                FRotator rot = CamMgr->CameraModifiers_CacheRelativateRotation;
                cacheStr = "CamCache Loc=(" + std::to_string(loc.X) + "," + std::to_string(loc.Y) + "," + std::to_string(loc.Z)
                         + ") Rot=(" + std::to_string(rot.Pitch) + "," + std::to_string(rot.Yaw) + "," + std::to_string(rot.Roll) + ")";
            }
        }
    }

    // Camera + weapon mesh frame-delta diagnostic.
    // Native weapon handling writes directly to FirstPersonCameraComponent
    // and weapon Mesh1P transforms; if those deltas are non-zero while idle
    // on a DS client, the same upstream corruption is hitting all three sinks.
    std::string cameraStr = "Cam=?";
    std::string weaponStr = "Weapon=?";
    static std::unordered_map<UObject*, FVector> s_PrevCameraLoc;
    static std::unordered_map<UObject*, FRotator> s_PrevCameraRot;
    static std::unordered_map<UObject*, FVector> s_PrevWeaponLoc;
    static std::unordered_map<UObject*, FRotator> s_PrevWeaponRot;

    if (Char->FirstPersonCameraComponent)
    {
        FVector camLoc = Char->FirstPersonCameraComponent->K2_GetComponentLocation();
        FRotator camRot = Char->FirstPersonCameraComponent->K2_GetComponentRotation();
        auto cl = s_PrevCameraLoc.find(Object);
        auto cr = s_PrevCameraRot.find(Object);
        if (cl != s_PrevCameraLoc.end() && cr != s_PrevCameraRot.end())
        {
            FVector dLoc = {camLoc.X - cl->second.X, camLoc.Y - cl->second.Y, camLoc.Z - cl->second.Z};
            FRotator dRot = {camRot.Pitch - cr->second.Pitch, camRot.Yaw - cr->second.Yaw, camRot.Roll - cr->second.Roll};
            cameraStr = "CamD LocD=(" + std::to_string(dLoc.X) + "," + std::to_string(dLoc.Y) + "," + std::to_string(dLoc.Z)
                      + ") RotD=(" + std::to_string(dRot.Pitch) + "," + std::to_string(dRot.Yaw) + "," + std::to_string(dRot.Roll) + ")";
        }
        s_PrevCameraLoc[Object] = camLoc;
        s_PrevCameraRot[Object] = camRot;
    }

    if (Char->CurrentWeapon && Char->CurrentWeapon->Mesh1P)
    {
        FVector wpLoc = Char->CurrentWeapon->Mesh1P->K2_GetComponentLocation();
        FRotator wpRot = Char->CurrentWeapon->Mesh1P->K2_GetComponentRotation();
        auto wl = s_PrevWeaponLoc.find(Object);
        auto wr = s_PrevWeaponRot.find(Object);
        if (wl != s_PrevWeaponLoc.end() && wr != s_PrevWeaponRot.end())
        {
            FVector dLoc = {wpLoc.X - wl->second.X, wpLoc.Y - wl->second.Y, wpLoc.Z - wl->second.Z};
            FRotator dRot = {wpRot.Pitch - wr->second.Pitch, wpRot.Yaw - wr->second.Yaw, wpRot.Roll - wr->second.Roll};
            weaponStr = "WpD LocD=(" + std::to_string(dLoc.X) + "," + std::to_string(dLoc.Y) + "," + std::to_string(dLoc.Z)
                      + ") RotD=(" + std::to_string(dRot.Pitch) + "," + std::to_string(dRot.Yaw) + "," + std::to_string(dRot.Roll) + ")";
        }
        s_PrevWeaponLoc[Object] = wpLoc;
        s_PrevWeaponRot[Object] = wpRot;
    }

    ClientDebugLog("[UI-FIX] " + std::string(Char->GetFullName())
              + "  bIsRunning=" + std::to_string((int)bIsRunning)
              + "  CharStatus=" + std::to_string((int)charStatus)
              + "(" + statusNames[static_cast<int>(charStatus) & 0x7] + ")"
              + "  " + cacheStr
              + "  " + cameraStr
              + "  " + weaponStr);
}
