#include "../Hooks/ServerHookPolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    const auto dedicated = ServerHookPolicy::BuildInstallPlan(true);
    Expect(dedicated.ForceServerOnlyObjectLoading,
        "dedicated bootstrap must retain server-only object eligibility overrides");
    Expect(dedicated.ForceDedicatedNetMode,
        "dedicated bootstrap must retain the pinned NetMode overrides");
    Expect(!dedicated.GuardRemotePlayerViewportLayers,
        "dedicated bootstrap has no local viewport layer to guard");

    const auto listen = ServerHookPolicy::BuildInstallPlan(false);
    Expect(!listen.ForceServerOnlyObjectLoading,
        "listen authority must preserve native client/server object eligibility");
    Expect(!listen.ForceDedicatedNetMode,
        "listen authority must preserve native listen NetMode");
    Expect(listen.GuardRemotePlayerViewportLayers,
        "listen authority must guard remote controllers from client HUD layers");

    Expect(ServerHookPolicy::ShouldForwardPlayerViewportLayerRequest(
            true, false, false),
        "the native null-controller no-op remains forwarded");
    Expect(ServerHookPolicy::ShouldForwardPlayerViewportLayerRequest(
            true, true, true),
        "the listen host local player keeps its viewport layer");
    Expect(!ServerHookPolicy::ShouldForwardPlayerViewportLayerRequest(
            true, true, false),
        "a listen host remote controller cannot enter PBGameViewportClient");
    Expect(ServerHookPolicy::ShouldForwardPlayerViewportLayerRequest(
            false, true, false),
        "disabled guard preserves the dedicated/native call path");

    Expect(ServerHookPolicy::ShouldRegisterListenHostParticipant(
            true, true, true, false),
        "a current listen-host local controller must enter the match quorum");
    Expect(!ServerHookPolicy::ShouldRegisterListenHostParticipant(
            false, true, true, false),
        "dedicated servers must not synthesize a local participant");
    Expect(!ServerHookPolicy::ShouldRegisterListenHostParticipant(
            true, false, true, false),
        "a stale controller outside the current GameState must be rejected");
    Expect(!ServerHookPolicy::ShouldRegisterListenHostParticipant(
            true, true, false, false),
        "a remote listen controller must remain on the native PostLogin path");
    Expect(!ServerHookPolicy::ShouldRegisterListenHostParticipant(
            true, true, true, true),
        "listen-host registration must be idempotent");

    Expect(ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, true, false, false, true, true),
        "a synthesized current listen host with a committed role is recovered");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            false, true, true, true, true, false, false, true, true),
        "dedicated authorities never recover a listen-host role");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, false, true, true, true, false, false, true, true),
        "ordinary remote participants never receive host recovery");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, false, true, true, false, false, true, true),
        "a stale listen controller outside the current world is rejected");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, false, true, false, false, true, true),
        "late joins remain on their normal role-selection path");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, false, false, false, true, true),
        "recovery waits until the shared role-selection flow is open");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, true, true, false, true, true),
        "an already-confirmed host is not replayed");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, true, false, true, true, true),
        "listen-host recovery is attempted at most once per world");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, true, false, false, false, true),
        "the PlayerState must report a selected role");
    Expect(!ServerHookPolicy::ShouldRecoverListenHostRoleConfirmation(
            true, true, true, true, true, false, false, true, false),
        "None and empty roles cannot satisfy host recovery");

    std::cout << "server hook policy tests passed\n";
    return 0;
}
