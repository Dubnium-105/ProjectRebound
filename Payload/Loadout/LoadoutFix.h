#pragma once
#include "../SDK.hpp"

// Called from ProcessEventHookClient for equip-related BP callbacks.
// Forces EPBEquipErrorCode to NoError(0) — the metaserver has already
// persisted the equipment correctly, but Native keeps firing UnknowError(4)
// because the original game server sent a different response format.
void HandleEquipErrorSwallow(SDK::UObject* Object, SDK::UFunction* Function, void* Parms,
                              const std::string& funcName);
void LoadoutFix_FlushRefresh();
void LoadoutFix_FetchAndLog();
void LoadoutFix_ForceRefresh();
