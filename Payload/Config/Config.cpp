// Config.cpp
#include "Config.h"
#include "CommandLinePolicy.h"
#include <Windows.h>
#include <iostream>
#include "../Debug/Debug.h"

// Central server ip
std::string OnlineBackendAddress = "";

// Room heartbeat credentials forwarded by the server launcher
std::string HostRoomId = "";
std::string HostToken = "";

// IP from the server browser
std::string MatchIP = "";

// Named pipe name for CommandFramework
std::string MatchPipeName = "";

ServerConfig Config{};
bool amServer = false;
bool amListenServer = false;

// Set up the dll to get values from the wrapper
std::string GetCmdValue(const std::string &key)
{
    const auto value = CommandLinePolicy::GetValue(GetCommandLineA(), key);
    return value.value_or("");
}

void LoadConfig()
{
    std::string cmd = GetCommandLineA();

    // PvE flag
    Config.IsPvE = CommandLinePolicy::HasExactSwitch(cmd, "-pve");

    // Map
    std::string mapArg = GetCmdValue("-map=");
    if (!mapArg.empty())
    {
        Config.MapName = std::wstring(mapArg.begin(), mapArg.end());
    }
    else
    {
        // fallback to something safe
        Config.MapName = L"Warehouse";
    }

    // Mode
    std::string modeArg = GetCmdValue("-mode=");
    if (!modeArg.empty())
    {
        Config.FullModePath = std::wstring(modeArg.begin(), modeArg.end());
    }
    else
    {
        // fallback based on PvE
        Config.FullModePath = Config.IsPvE
                                  ? L"/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Hard.BP_PBGameMode_Rush_PVE_Hard_C"
                                  : L"/Game/Online/GameMode/PBGameMode_Rush_BP.PBGameMode_Rush_BP_C";
    }

    // Port
    std::string portArg = GetCmdValue("-port=");
    if (!portArg.empty())
    {
        Config.Port = std::stoi(portArg);
    }
    else
    {
        Config.Port = 7777;
    }

    // External port
    std::string externalArg = GetCmdValue("-external=");
    if (!externalArg.empty())
    {
        Config.ExternalPort = std::stoi(externalArg);
    }
    else
    {
        Config.ExternalPort = Config.Port; // default same as internal
    }

    Log("[SERVER] External port: " + std::to_string(Config.ExternalPort));

    // Name
    std::string serverNameArg = GetCmdValue("-servername=");
    if (!serverNameArg.empty())
    {
        Config.ServerName = serverNameArg;
        Log("[SERVER] Server name: " + serverNameArg);
    }
    else
    {
        Config.ServerName = "Dedicated Server";
    }
    // Region
    std::string serverRegionArg = GetCmdValue("-serverregion=");
    if (!serverRegionArg.empty())
    {
        Config.ServerRegion = serverRegionArg;
        Log("[SERVER] Server region: " + serverRegionArg);
    }
    else
    {
        Config.ServerRegion = "asia-hk";
    }

    Config.MaxPlayers = 10;
    std::string maxPlayersArg = GetCmdValue("-maxplayers=");
    if (!maxPlayersArg.empty())
    {
        Config.MaxPlayers = std::stoi(maxPlayersArg);
    }
    // Min players (still used in TickFlush)
    Config.MinPlayersToStart = Config.IsPvE ? 1 : 2;

    // Online check if contact central server
    std::string onlineArg = GetCmdValue("-online=");
    if (!onlineArg.empty())
    {
        OnlineBackendAddress = onlineArg;
        std::cout << "[SERVER] Online backend: " << OnlineBackendAddress << std::endl;
    }

    std::string roomIdArg = GetCmdValue("-roomid=");
    if (!roomIdArg.empty())
    {
        HostRoomId = roomIdArg;
        Log("[SERVER] Host room id: " + HostRoomId);
    }

    std::string hostTokenArg = GetCmdValue("-hosttoken=");
    if (!hostTokenArg.empty())
    {
        HostToken = hostTokenArg;
        Log("[SERVER] Host token received.");
    }

    std::string serverIdArg = GetCmdValue("-serverid=");
    if (!serverIdArg.empty())
    {
        Config.ServerUniqueId = serverIdArg;
        Log("[SERVER] Server ID: " + serverIdArg);
    }

    std::string publicHostArg = GetCmdValue("-publichost=");
    if (!publicHostArg.empty())
    {
        Config.PublicHost = publicHostArg;
        Log("[SERVER] Public host configured.");
    }

    MatchPipeName = GetCmdValue("-pipe=");
    if (!MatchPipeName.empty())
        Log("[SERVER] Toolbox command pipe configured.");
}

void LoadClientConfig()
{
    std::string matchArg = GetCmdValue("-match=");
    if (!matchArg.empty())
    {
        MatchIP = matchArg;
        ClientLog("[CLIENT] Auto-match target: " + MatchIP);
    }

    // Command pipe name for CommandFramework
    MatchPipeName = GetCmdValue("-pipe=");
    if (!MatchPipeName.empty())
    {
        ClientLog("[CLIENT] Command pipe name: " + MatchPipeName);
    }

    // NEW: debug log flag
    if (CommandLinePolicy::HasExactSwitch(GetCommandLineA(), "-debuglog"))
    {
        ClientDebugLogEnabled = true;
    }
}
