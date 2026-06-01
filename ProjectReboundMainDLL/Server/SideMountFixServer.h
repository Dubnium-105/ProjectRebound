#pragma once
#include "../SDK.hpp"

// Called from server-side ProcessEventHook when Object is APBLauncher.
// Handles diagnostics + dud ServerFiring blocking.
// Returns true if the event was consumed (skip ProcessEvent.call).
bool HandleLauncherServerEvent(SDK::UObject* Object, SDK::UFunction* Function, void* Parms,
                               const std::string& funcName);
