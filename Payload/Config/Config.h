// Config.h
#pragma once
#include <string>

struct ServerConfig
{
    std::wstring MapName;
    std::wstring FullModePath;
    unsigned int ExternalPort;
    unsigned int Port;
    bool IsPvE;
    int MinPlayersToStart;
    int MaxPlayers;
    std::string ServerName;
    std::string ServerRegion;
    std::string ServerUniqueId;
    std::string PublicHost;
    std::string GameServerAgentPath;
};

// Central server ip
extern std::string OnlineBackendAddress;

// One-time Dedicated Server registration credential. Runtime credentials and
// the node private key are owned by game-server-agent, not kept in the DLL.
extern std::string RegistrationToken;

// Room heartbeat credentials forwarded by the server launcher
extern std::string HostRoomId;
extern std::string HostToken;

// IP from the server browser
extern std::string MatchIP;

// Named pipe name for CommandFramework
extern std::string MatchPipeName;

extern ServerConfig Config;
extern bool amServer;

std::string GetCmdValue(const std::string &key);
void LoadConfig();
void LoadClientConfig();
