// Hooks.h
#pragma once

#include "../Admission/StrictRosterPolicy.h"

void InitMessageBoxHook();
void InitServerHooks(bool forceDedicatedMode = true);
void InitClientHook();
void InitClientArchiveHooks();
bool InitStrictRosterAdmissionHooks(StrictRoster::Policy* policy);
void SetStrictRosterLocalHostSeat(
    const StrictRoster::SeatDecision& decision);
void ClearStrictRosterLocalHostSeat();
