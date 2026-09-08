#include "../Config/CommandLinePolicy.h"
#include "../Admission/StrictRosterAdmissionGate.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(bool condition, const char* message)
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
    const std::string client =
        R"("C:\Boundary\game.exe" -servername=Alpha -serverregion=asia-hk -debuglog)";
    Expect(!CommandLinePolicy::HasExactSwitch(client, "-server"),
        "server-name and region arguments must not imply -server");
    Expect(CommandLinePolicy::HasExactSwitch(client, "-debuglog"),
        "exact debug switch should be found");

    const std::string roomAuthority =
        R"("C:\Boundary\game.exe" -RoomAuthority -roomid=room_1-alpha)";
    Expect(CommandLinePolicy::HasExactSwitch(roomAuthority, "-roomauthority"),
        "ordinary room authority switch should be case-insensitive");
    Expect(!CommandLinePolicy::HasExactSwitch(
        R"(game.exe -RoomAuthorityName=test)", "-RoomAuthority"),
        "ordinary room authority requires an exact switch token");

    const std::string server =
        R"("C:\Boundary\game.exe" -server -servername="My Server" -LoadoutSpawnBridge=false)";
    Expect(CommandLinePolicy::HasExactSwitch(server, "-server"),
        "exact server switch should be found");
    Expect(!CommandLinePolicy::HasExactSwitch(server, "-LocalPveLoadout"),
        "local PVE loadout mode must remain opt-in");
    Expect(CommandLinePolicy::GetValue(server, "-servername=").value_or("") == "My Server",
        "quoted values should remain one token");
    Expect(!CommandLinePolicy::FeatureEnabled(server, "-LoadoutSpawnBridge"),
        "explicit false should disable a feature");
    Expect(CommandLinePolicy::FeatureEnabled(server, "-LoadoutBaselineBridge"),
        "missing feature flags should retain the default");
    Expect(CommandLinePolicy::FeatureEnabled(server, "-RespawnExplicitNative"),
        "native explicit respawn forwarding should default on");

    const std::string enabled =
        R"(game.exe -LoadoutSpawnBridge=1 -NativeArchiveOnlyExtra)";
    Expect(CommandLinePolicy::FeatureEnabled(enabled, "-LoadoutSpawnBridge", false),
        "explicit one should enable a feature");
    Expect(!CommandLinePolicy::HasExactSwitch(enabled, "-NativeArchiveOnly"),
        "prefix-compatible switches must not be accepted");
    Expect(!CommandLinePolicy::FeatureEnabled(
        "game.exe -RespawnExplicitNative=false",
        "-RespawnExplicitNative"),
        "A/B must be able to select the legacy replacement chain");

    const std::string localPve =
        R"(game.exe -server -pve -LocalPveLoadout)";
    Expect(CommandLinePolicy::HasExactSwitch(localPve, "-server") &&
        CommandLinePolicy::HasExactSwitch(localPve, "-pve") &&
        CommandLinePolicy::HasExactSwitch(localPve, "-LocalPveLoadout"),
        "local PVE loadout mode requires three independent exact switches");
    Expect(!CommandLinePolicy::HasExactSwitch(
        R"(game.exe -LocalPveLoadout=1)", "-LocalPveLoadout"),
        "a similarly prefixed value must not enable local PVE loadouts");
    Expect(StrictRosterAdmissionGate::IsExplicitOfflinePve(localPve),
        "only the complete local PVE bootstrap may bypass strict roster admission");
    Expect(!StrictRosterAdmissionGate::IsExplicitOfflinePve(
        R"(game.exe -server -pve)"),
        "bare PVE server mode must not bypass strict roster admission");
    Expect(!StrictRosterAdmissionGate::IsExplicitOfflinePve(
        R"(game.exe -server -LocalPveLoadout)"),
        "local PVE loadout without the PVE mode must not bypass strict roster admission");
    Expect(StrictRosterAdmissionGate::IsListenAuthorityBootstrap(
        false, true, false),
        "a client strict authority may route its frontend world to listen mode");
    Expect(StrictRosterAdmissionGate::IsListenAuthorityBootstrap(
        false, false, true),
        "the legacy room switch is classified as a client listen bootstrap before rejection");
    Expect(!StrictRosterAdmissionGate::IsListenAuthorityBootstrap(
        true, true, false),
        "an explicit strict server must never be routed to listen mode");
    Expect(!StrictRosterAdmissionGate::IsListenAuthorityBootstrap(
        true, false, true),
        "a server room switch must not create a listen-client path");
    Expect(StrictRosterAdmissionGate::IsDedicatedAuthorityBootstrap(true, true),
        "strict server bootstrap is dedicated");
    Expect(!StrictRosterAdmissionGate::IsDedicatedAuthorityBootstrap(false, true),
        "strict client bootstrap is not dedicated");
    Expect(StrictRosterAdmissionGate::EvaluatePreLogin(
        true, false, false) ==
        StrictRosterAdmissionGate::PreLoginDecision::OfflinePveBypass,
        "explicit local PVE must remain an isolated offline bypass");
    Expect(StrictRosterAdmissionGate::EvaluatePreLogin(
        false, false, false) ==
        StrictRosterAdmissionGate::PreLoginDecision::RejectAllocationUnavailable,
        "online PreLogin must reject before any allocation is installed");
    Expect(StrictRosterAdmissionGate::EvaluatePreLogin(
        false, true, false) ==
        StrictRosterAdmissionGate::PreLoginDecision::RejectNativeGrantUnverified,
        "online PreLogin must reject when NMT grant possession is unverified");
    Expect(StrictRosterAdmissionGate::EvaluatePreLogin(
        false, true, true) ==
        StrictRosterAdmissionGate::PreLoginDecision::AcceptForVerifiedNativeGrant,
        "the gate only admits a future verified native-grant path");
    Expect(StrictRosterAdmissionGate::MayStartOnlineAuthority(
        true, true, false, false),
        "explicit local PVE may start its isolated dedicated server");
    Expect(!StrictRosterAdmissionGate::MayStartOnlineAuthority(
        true, false, false, true),
        "bare online or retired RoomAuthority bootstrap must not reach the native listener");
    Expect(!StrictRosterAdmissionGate::MayStartOnlineAuthority(
        true, false, true, false),
        "strict authority must stop when pinned native hooks are not ready");
    Expect(StrictRosterAdmissionGate::MayStartOnlineAuthority(
        true, false, true, true),
        "strict authority may start only after pinned native hooks are ready");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, false, false),
        "installed hooks and a started path must not report strict online readiness");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, true, false),
        "native authority proof cannot substitute for the unverified client grant path");
    Expect(StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, true, true),
        "strict online readiness requires both native proofs");
    Expect(StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, true, false, false),
        "dedicated authority readiness must not require the client-only grant injector");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, true, false, true),
        "listen/P2P readiness must require the native client grant injector");
    Expect(StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, false, true, true, true, false),
        "a Member requires the proven client path without installing server-only hooks");
    Expect(StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, false, false, true, true, false),
        "a Member may reach native login while the authority proof remains pending");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, false, true, false, true, false),
        "a Member cannot report ready without its native injector");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, true, false, true, false, true),
        "an authority cannot substitute role selection for locked native admission proof");
    Expect(!StrictRosterAdmissionGate::CanReportStrictOnlineReady(
        true, false, false, true, false, false, false),
        "an online role cannot disable both required native paths");
    Expect(!StrictRosterAdmissionGate::CanReportPayloadReady(
        false, false, true),
        "an unverified executable must not report even isolated PVE as ready");
    Expect(StrictRosterAdmissionGate::CanReportPayloadReady(
        true, false, true),
        "the verified explicit local PVE launcher may report ready");

    const std::string multiMatch =
        R"(game.exe -DedicatedMultiMatch -multimatchconfig="C:\Project Rebound\serverconfig.json")";
    Expect(CommandLinePolicy::HasExactSwitch(multiMatch, "-DedicatedMultiMatch"),
        "dedicated multi-match must require its exact opt-in switch");
    Expect(!CommandLinePolicy::HasExactSwitch(
        R"(game.exe -DedicatedMultiMatchBackup)", "-DedicatedMultiMatch"),
        "a prefixed switch must not enable dedicated multi-match");
    Expect(CommandLinePolicy::GetValue(
        multiMatch, "-multimatchconfig=").value_or("") ==
        R"(C:\Project Rebound\serverconfig.json)",
        "the quoted multi-match config path must remain one token");

    std::cout << "command line policy tests passed\n";
    return 0;
}
