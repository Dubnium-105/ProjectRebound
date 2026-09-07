#pragma once

#include "../Config/CommandLinePolicy.h"

#include <string_view>

namespace StrictRosterAdmissionGate
{
    // The only intentionally isolated no-roster path is the pinned local PVE
    // launcher.  A bare -pve server, a listen authority, and every online
    // dedicated bootstrap remain subject to strict roster admission.
    inline bool IsExplicitOfflinePve(std::string_view commandLine) noexcept
    {
        return CommandLinePolicy::HasExactSwitch(commandLine, "-server") &&
            CommandLinePolicy::HasExactSwitch(commandLine, "-pve") &&
            CommandLinePolicy::HasExactSwitch(commandLine, "-LocalPveLoadout");
    }

    // A strict client/listen authority is the only bootstrap that may route
    // an initially standalone client world to listen mode.  An explicit
    // -server authority must remain dedicated even though the pinned client
    // executable may report a provisional standalone world during startup.
    inline bool IsListenAuthorityBootstrap(
        const bool serverBootstrap,
        const bool strictAuthorityBootstrap,
        const bool roomAuthorityBootstrap) noexcept
    {
        return !serverBootstrap &&
            (strictAuthorityBootstrap || roomAuthorityBootstrap);
    }

    inline bool IsDedicatedAuthorityBootstrap(
        const bool serverBootstrap,
        const bool strictAuthorityBootstrap) noexcept
    {
        return serverBootstrap && strictAuthorityBootstrap;
    }

    enum class PreLoginDecision
    {
        OfflinePveBypass,
        RejectAllocationUnavailable,
        RejectNativeGrantUnverified,
        AcceptForVerifiedNativeGrant,
    };

    inline PreLoginDecision EvaluatePreLogin(
        const bool offlinePve,
        const bool admissionActive,
        const bool nativeClientGrantInjectionVerified) noexcept
    {
        if (offlinePve)
            return PreLoginDecision::OfflinePveBypass;
        if (!admissionActive)
            return PreLoginDecision::RejectAllocationUnavailable;
        if (!nativeClientGrantInjectionVerified)
            return PreLoginDecision::RejectNativeGrantUnverified;
        return PreLoginDecision::AcceptForVerifiedNativeGrant;
    }

    inline bool MayStartOnlineAuthority(
        const bool runServer,
        const bool offlinePve,
        const bool strictAuthorityBootstrap,
        const bool nativeHooksReady) noexcept
    {
        if (!runServer || offlinePve)
            return true;
        return strictAuthorityBootstrap && nativeHooksReady;
    }

    // Hooks being installed only proves that the fixed native callsites can
    // be guarded.  It does not prove that a signed allocation has produced a
    // native authority admission receipt, and it certainly does not prove
    // that the client can put a join grant into NMT_Login.  Keep this pure so
    // status/reporting code cannot accidentally turn either capability into a
    // readiness flag by observing a started server or an active policy.
    inline bool CanReportStrictOnlineReady(
        const bool executableVerified,
        const bool offlinePve,
        const bool nativeAuthorityPathReady,
        const bool nativeAuthorityAdmissionVerified,
        const bool nativeClientGrantInjectionVerified) noexcept
    {
        return executableVerified && !offlinePve &&
            nativeAuthorityPathReady && nativeAuthorityAdmissionVerified &&
            nativeClientGrantInjectionVerified;
    }

    inline bool CanReportPayloadReady(
        const bool executableVerified,
        const bool strictOnlineReady,
        const bool offlinePve) noexcept
    {
        return executableVerified && (strictOnlineReady || offlinePve);
    }
}
