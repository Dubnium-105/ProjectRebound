#pragma once
#include "../SDK.hpp"

// Called from client-side ProcessEventHookClient when Object is APBLauncher.
// Handles state machine fix, flag clearing, K2_ calls, diagnostics, dud blocking.
// Returns true if the event was consumed (skip ProcessEventClient.call).
bool HandleLauncherClientEvent(SDK::UObject* Object, SDK::UFunction* Function, void* Parms,
                               const std::string& funcName);

// Called from client-side ProcessEventHookClient when Object is APBProjectile.
// Handles diagnostics + explosion visual fix (OnRep_Exploded -> MulticastExplode).
void HandleProjectileClientEvent(SDK::UObject* Object, const std::string& funcName);
