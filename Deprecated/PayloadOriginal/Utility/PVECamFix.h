// PVECamFix.h
// PvE intro camera fix — polls the MatchIntro LevelSequence, then runs a
// per-tick Possess window after the cinematic ends to re-attach cameras.

#ifndef PVECAMFIX_H_STKDF
#define PVECAMFIX_H_STKDF
#pragma once

namespace SDK { class UNetDriver; }

// Call every tick from TickFlushHook.  Handles all PvE intro camera logic.
void PVECamFix_Tick(SDK::UNetDriver *NetDriver, float DeltaTime);

#endif //PVECAMFIX_H_STKDF
