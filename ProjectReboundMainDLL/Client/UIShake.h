#pragma once
#include "../SDK.hpp"

// Called from ProcessEventHookClient when Object is APBCharacter.
// Rate-limited diagnostic for sprint shake root cause investigation.
void HandleUICharacterClientEvent(SDK::UObject* Object, SDK::UFunction* Function, void* Parms,
                                   const std::string& funcName);
