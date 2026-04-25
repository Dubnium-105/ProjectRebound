#pragma once

// ======================================================
//  LoadoutManager - loadout snapshot/export/runtime facade
// ======================================================
//
//  Responsibilities:
//    Keep the full loadout pipeline behind one public manager, including
//    local snapshot preload, menu-side export triggers, live match apply,
//    and ProcessEvent-scoped weapon-definition overrides.
//
//  Usage:
//    1. Construct one long-lived instance during Payload startup.
//    2. Call PreloadSnapshot() before server/client runtime work starts.
//    3. Forward ProcessEvent pre/post callbacks from the hook layer.
//    4. Forward TickClient()/TickServer() from the existing worker/tick hooks.
//    5. Forward menu-role selection and menu-constructed signals when observed.
//
//  Design principles:
//    - Hook sites stay thin: they only forward events into this facade.
//    - Internal state stays private so later loadout fixes do not spread back
//      into dllmain.cpp.
//    - The public API exposes ordering-sensitive entry points explicitly:
//      callers must pair Pre/Post hooks correctly.

#include <memory>
#include <string>

// Forward declarations kept in the SDK namespace for consistency with the
// generated SDK types used by the hook layer.
namespace SDK
{
    class FName;
    class UObject;
}

class LoadoutManager
{
public:
    // ------------------------------------------------------------------
    //  Construction / lifetime
    // ------------------------------------------------------------------

    LoadoutManager();
    ~LoadoutManager();

    LoadoutManager(const LoadoutManager&) = delete;
    LoadoutManager& operator=(const LoadoutManager&) = delete;
    LoadoutManager(LoadoutManager&&) noexcept;
    LoadoutManager& operator=(LoadoutManager&&) noexcept;

    // ------------------------------------------------------------------
    //  Public facade - startup/menu signals
    // ------------------------------------------------------------------

    // @brief Preload the persisted loadout snapshot from disk.
    //        Call this early so later menu/live apply logic starts with the
    //        latest sidecar state already cached.
    void PreloadSnapshot();

    // @brief Notify the manager that the main menu has been constructed.
    //        This opens the window for delayed initial snapshot capture and
    //        any guarded menu-side reapply logic.
    void NotifyMenuConstructed();

    // @brief Remember the last role explicitly selected in the menu.
    //        This is used as a higher-quality hint than raw selectedRoleId
    //        fields when matching the local player back to the right snapshot.
    void RememberMenuSelectedRole(const SDK::FName& roleId);

    // ------------------------------------------------------------------
    //  Public facade - ProcessEvent hook bridge
    // ------------------------------------------------------------------

    // @brief Client-side ProcessEvent pre-hook entry.
    //        Call before the original ProcessEvent so temporary weapon
    //        definition overrides are visible to native code during execution.
    void OnClientProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief Client-side ProcessEvent post-hook entry.
    //        Must be paired with OnClientProcessEventPre() so temporary
    //        overrides are restored and export triggers can be scheduled.
    void OnClientProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief Server-side ProcessEvent pre-hook entry.
    //        Mirrors the client path but drives authoritative match-side
    //        snapshot apply and scoped override setup.
    void OnServerProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief Server-side ProcessEvent post-hook entry.
    //        Restores scoped mutations and lets authoritative apply observe
    //        the final native results of the original call.
    void OnServerProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);

    // ------------------------------------------------------------------
    //  Public facade - worker/tick bridge
    // ------------------------------------------------------------------

    // @brief Advance client-side asynchronous export/apply work.
    void TickClient();

    // @brief Advance server-side authoritative apply work.
    void TickServer();

private:
    // PIMPL keeps JSON-heavy state, thread-local detail, and large helper
    // structures out of this public header.
    class Impl;
    std::unique_ptr<Impl> impl_;
};
