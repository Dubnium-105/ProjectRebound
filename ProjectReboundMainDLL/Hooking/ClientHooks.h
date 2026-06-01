#pragma once

#include "../SDK.hpp"

// ======================================================
//  ClientHooks — client-side ProcessEvent dispatch
//  + ClientDeathCrash fix.
// ======================================================

void ProcessEventHookClient(SDK::UObject* Object, SDK::UFunction* Function, void* Parms);
__int64 ClientDeathCrashHook(__int64 a1);
