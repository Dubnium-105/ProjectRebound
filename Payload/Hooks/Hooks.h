// Hooks.h
#pragma once

#include "../Admission/StrictRosterPolicy.h"

#include <string>
#include <optional>

void InitMessageBoxHook();
void InitServerHooks(bool forceDedicatedMode = true);
void InitClientHook();
void InitClientArchiveHooks();
// True only after the fixed executable serializer and the exact NMT_Login
// field2 callsite both pass their byte gates and the inline hook is installed.
// This capability does not imply platform possession or authority proof.
bool IsStrictRosterNativeClientGrantInjectionReady();
bool InitStrictRosterAdmissionHooks(
    StrictRoster::Policy* policy,
    bool authorityGameServer);
// Unregisters the official Steam callback before an explicit DLL unload.
void ShutdownStrictRosterPlatformAuth();
void SetStrictRosterLocalHostSeat(
    const StrictRoster::SeatDecision& decision);
void ClearStrictRosterLocalHostSeat();
void ClearStrictRosterControllerSeats();
enum class StrictRosterNativeTeardownRequestResult
{
    NotRequested,
    WorldReturnToMenuRequested,
    DedicatedProcessExitRequested,
};
// Ask the native controller path to return every current seat to the menu.
// The caller must still observe the old UWorld/UNetDriver teardown before it
// reports NativeCleared; this function is only the teardown request boundary.
StrictRosterNativeTeardownRequestResult RequestStrictRosterNativeWorldTeardown();
// Reads the local platform identity from the native PlayerState UniqueId.
// This is intentionally not sourced from the IPC request or UI metadata.
bool TryGetStrictRosterLocalPlatformId(std::string& platformId);
// Generates the opaque nonce that binds one native handshake to its scoped
// backend reservation/confirmation.  Failure must fail closed.
std::optional<std::string> GenerateStrictRosterNativeConnectionNonce();
nlohmann::json QueueStrictRosterAdmissionReservation(
    const nlohmann::json& arguments);
nlohmann::json QueueStrictRosterConnectionConfirmation(
    const nlohmann::json& arguments);
nlohmann::json QueueStrictRosterAdmissionRelease(
    const nlohmann::json& arguments);
void PumpStrictRosterBackendReceipts();
