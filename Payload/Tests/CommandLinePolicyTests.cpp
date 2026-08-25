#include "../Config/CommandLinePolicy.h"

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
