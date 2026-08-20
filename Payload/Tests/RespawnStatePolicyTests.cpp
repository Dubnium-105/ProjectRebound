#include "../ServerLogic/RespawnStatePolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    using RespawnStatePolicy::ExplicitRequestAction;

    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }

    void TestUnmanagedAndPermittedCallsPassThrough()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            false, false, false, false, true) ==
            ExplicitRequestAction::PassThrough,
            "unmanaged native restart must remain untouched");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, true, false, false, true) ==
            ExplicitRequestAction::PassThrough,
            "manager fallback under a permit must not be re-queued");
    }

    void TestOnlyAwaitingInputAcceptsExplicitIntent()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, false, true, true) ==
            ExplicitRequestAction::Deny,
            "duplicate F during an active spawn must be denied");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, false, true) ==
            ExplicitRequestAction::Deny,
            "an invalid lifecycle must not swallow-and-pretend to queue");
    }

    void TestABSelectsOnlyTheDispatchWiring()
    {
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, true, false) ==
            ExplicitRequestAction::QueueAndSuppress,
            "A must preserve the legacy replacement chain");
        Expect(RespawnStatePolicy::DecideExplicitRequest(
            true, false, true, true, true) ==
            ExplicitRequestAction::QueueAndForwardNative,
            "B must forward the exact explicit native request");
    }

    void TestManagedEngineRestartUsesPBQuickRespawnCleanup()
    {
        Expect(RespawnStatePolicy::ShouldNormalizeEngineRestartToQuickRespawn(
            ExplicitRequestAction::QueueAndForwardNative, true),
            "managed explicit engine restart must enter the PB quick wrapper");
        Expect(!RespawnStatePolicy::ShouldNormalizeEngineRestartToQuickRespawn(
            ExplicitRequestAction::QueueAndForwardNative, false),
            "PB quick requests must remain exact native requests");
        Expect(!RespawnStatePolicy::ShouldNormalizeEngineRestartToQuickRespawn(
            ExplicitRequestAction::QueueAndSuppress, true),
            "legacy A wiring must not dispatch a normalized native request");
        Expect(!RespawnStatePolicy::ShouldNormalizeEngineRestartToQuickRespawn(
            ExplicitRequestAction::PassThrough, true),
            "permitted and unmanaged engine requests must remain untouched");
    }
}

int main()
{
    TestUnmanagedAndPermittedCallsPassThrough();
    TestOnlyAwaitingInputAcceptsExplicitIntent();
    TestABSelectsOnlyTheDispatchWiring();
    TestManagedEngineRestartUsesPBQuickRespawnCleanup();
    std::cout << "respawn state policy tests passed\n";
    return 0;
}
