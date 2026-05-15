// UserNameFix.h
// PostLogin Steam name resolution + scoreboard ID override.
// All logic lives in UserNameFix.cpp so Hooks.cpp stays thin.

#ifndef USERNAMEFIX_H_STKDF
#define USERNAMEFIX_H_STKDF
#pragma once

#include "../SDK.hpp"

// Call from PostLogin hook.  Spawns an async worker thread that resolves the
// Steam64ID to a persona name and pushes the result into a pending queue.
void UserNameFix_OnPostLogin(SDK::AGameMode *GameMode, SDK::APBPlayerController *PC);

// Call from TickFlushHook every frame.  Drains the pending name-change queue
// and writes resolved names (and a custom scoreboard ID) on the game thread.
void UserNameFix_DrainPending();

#endif //USERNAMEFIX_H_STKDF
