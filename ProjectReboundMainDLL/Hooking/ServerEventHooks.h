#pragma once

#include "../SDK.hpp"

// ======================================================
//  ServerEventHooks — event-driven server hooks:
//  ProcessEventHook dispatch, Notify hooks, OnFireWeapon.
// ======================================================

void ProcessEventHook(SDK::UObject* Object, SDK::UFunction* Function, void* Parms);
bool NotifyActorDestroyedHook(SDK::UWorld* World, SDK::AActor* Actor, bool SomeShit, bool SomeShit2);
__int64 NotifyAcceptingConnectionHook(SDK::UObject* obj);
char NotifyControlMessageHook(unsigned __int64 ScuffedShit, __int64 a2, uint8_t a3, __int64 a4);
void* OnFireWeapon(SDK::APBWeapon* Weapon);
