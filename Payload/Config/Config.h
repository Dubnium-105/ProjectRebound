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
};

// Central server ip
extern std::string OnlineBackendAddress;

// Room heartbeat credentials forwarded by the server launcher
extern std::string HostRoomId;
extern std::string HostToken;

// IP from the server browser
extern std::string MatchIP;

// Named pipe name for CommandFramework
extern std::string MatchPipeName;

extern ServerConfig Config;
extern bool amServer;
extern bool amListenServer;

std::string GetCmdValue(const std::string &key);
void LoadConfig();
void LoadClientConfig();
