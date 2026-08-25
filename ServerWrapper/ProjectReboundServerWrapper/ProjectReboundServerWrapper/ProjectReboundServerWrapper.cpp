// ======================================================
//  INCLUDES AND GLOBALS
// ======================================================

#define NOMINMAX

#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>

#include <atomic>
#include <array>
#include <cctype>
#include <chrono>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <mutex>
#include <random>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <unordered_map>
#include <vector>
#include <functional>
#include "json.hpp"
using json = nlohmann::json;

#pragma comment(lib, "ws2_32.lib")

enum class ServerState
{
    Stopped,
    Starting,
    Running,
    Stopping,
    Restarting
};

HANDLE g_ServerProcess = NULL;
DWORD g_ServerPid = 0;

//Config constants
const std::string DEFAULT_BACKEND = "https://api.project-rebound.space";
std::string CurrentMap = "Warehouse";
std::string CurrentMode = "pve";
std::string LastMap = "";
std::string CurrentDifficulty = "normal";
std::string OnlineBackend = "";
std::string ServerName = "DefaultServer";
std::string ServerRegion = "CN";
std::string HostRoomId = "";
std::string HostToken = "";
std::string ServerUniqueId = "";
std::string PublicHost = "";
std::string ToolboxPipeName = "";
std::string GameExePath = ".\\ProjectBoundarySteam-Win64-Shipping.exe";
int g_ServerPort = 7777;
int g_ExternalPort = g_ServerPort;
int g_MaxPlayers = 10;
bool OfflineMode = false;
bool UseDX11 = false;

// Dedicated multi-match is deliberately opt-in.  The payload owns the
// in-process travel state; the wrapper only validates and passes this stable
// configuration to the child process.
struct MultiMatchConfig
{
    bool enabled = false;
    std::vector<std::string> playlist;
    int travelTimeoutSeconds = 45;
    bool voteEnabled = true;
    int voteDurationSeconds = 15;
    int voteCandidateCount = 3;
};

MultiMatchConfig g_MultiMatch;
std::string g_ServerConfigPath = "serverconfig.json";

//Lifecycle Management
std::mutex g_ServerMutex;
std::mutex g_LogMutex;
std::atomic<bool> ServerRunning = false;
std::atomic<bool> HeartbeatSeen = false;
std::atomic<bool> g_WrapperShuttingDown = false;
std::atomic<ServerState> g_ServerState{ ServerState::Stopped };
std::atomic<uint64_t> g_ServerGeneration{ 0 };
std::atomic<int> g_ConsecutiveFailures{ 0 };
std::atomic<uint64_t> g_LastHeartbeatTickMs{ 0 };

std::chrono::steady_clock::time_point g_LastFailureTime;
std::chrono::steady_clock::time_point g_ServerLaunchTime;
const int MAX_FAILURES = 3;
const auto FAILURE_RESET_WINDOW = std::chrono::minutes(1);

void LauncherLog(const std::string& msg);
void LaunchServer();
void RestartServer();
void KillServer();
std::string GetCmdValue(const std::string& key);
void LoadCommandLineConfig();

bool LaunchServerLocked();
bool StopServerLocked();
void RequestRestart(bool rotateMap, const std::string& reason);
void PipeReader(HANDLE pipe, uint64_t generation);
void StartWatchdog(HANDLE processHandle, uint64_t generation);
void StartExitWatcher(HANDLE processHandle, uint64_t generation);

void SaveConfigFile();
bool LoadConfigFile();
std::filesystem::path GetServerConfigPath();
bool ValidateMultiMatchConfig(MultiMatchConfig& config, std::string& error);
std::string PickNextPlaylistMap();
void ObserveMultiMatchStatusLine(const std::string& line, uint64_t generation);

// ======================================================
//  LOGGING SYSTEM
// ======================================================

std::ofstream logFile;

void LauncherLog(const std::string& msg)
{
    std::string line = "[Launcher] " + msg;
    std::lock_guard<std::mutex> lock(g_LogMutex);

    logFile << line << std::endl;
    logFile.flush();
    std::cout << line << std::endl;
}

// ======================================================
//  UTILITY FUNCTIONS
// ======================================================
//Init the steady clock in milliseconds for heartbeat checking
uint64_t SteadyNowMs()
{
    return static_cast<uint64_t>(
        std::chrono::duration_cast<std::chrono::milliseconds>(
            std::chrono::steady_clock::now().time_since_epoch()).count());
}
//Sets the LastHeartbeatTick to current time
void ResetHeartbeatClock()
{
    g_LastHeartbeatTickMs.store(SteadyNowMs());
}

//Compare lastheartbeat and now, check if the difference is larger than the given std::chrono::seconds
bool HasHeartbeatTimedOut(std::chrono::seconds timeout)
{
    const uint64_t last = g_LastHeartbeatTickMs.load();
    const uint64_t now = SteadyNowMs();
    return now > last + static_cast<uint64_t>(timeout.count()) * 1000ULL; //Operate the time in 64bit ULL
}

//Function to Dupe importante watchdog handles into the current process so they aer protected
HANDLE DuplicateProcessHandle(HANDLE source)
{
    HANDLE duplicated = NULL;
    if (!DuplicateHandle(
        GetCurrentProcess(),
        source,
        GetCurrentProcess(),
        &duplicated,
        0,
        FALSE,
        DUPLICATE_SAME_ACCESS))
    {
        LauncherLog("DuplicateHandle failed. GetLastError=" + std::to_string(GetLastError()));
        return NULL;
    }

    return duplicated;
}

//Gets the current timestamp in the format of YYYYMMDD_HHMMSS for log file naming
std::string CurrentTimestamp()
{
    auto now = std::chrono::system_clock::now();
    std::time_t t = std::chrono::system_clock::to_time_t(now);

    std::tm tm{};
    localtime_s(&tm, &t);

    std::ostringstream oss;
    oss << std::put_time(&tm, "%Y%m%d_%H%M%S");
    return oss.str();
}

std::string WideToUtf8(const std::wstring& value)
{
    if (value.empty())
        return {};

    const int required = WideCharToMultiByte(
        CP_UTF8,
        0,
        value.data(),
        static_cast<int>(value.size()),
        nullptr,
        0,
        nullptr,
        nullptr);

    if (required <= 0)
        return {};

    std::string result(static_cast<std::size_t>(required), '\0');
    WideCharToMultiByte(
        CP_UTF8,
        0,
        value.data(),
        static_cast<int>(value.size()),
        result.data(),
        required,
        nullptr,
        nullptr);

    return result;
}

// Converts ServerState enum to string for logging purposes
const char* ServerStateToString(ServerState state)
{
    switch (state)
    {
    case ServerState::Running:
        return "Running";
    case ServerState::Starting:
        return "Starting";
    case ServerState::Stopping:
        return "Stopping";
    case ServerState::Restarting:
        return "Restarting";
    case ServerState::Stopped:
    default:
        return "Stopped";
    }
}


// ======================================================
//  CONSOLE COMMAND SYSTEM
// ======================================================

// Struct the command structure with name and it's function handler
struct Command
{
    std::string name;
    std::string help;
    std::function<void(const std::string& args)> handler;
};

//Init the command lookup table
std::vector<Command> g_Commands;
std::unordered_map<std::string, size_t> g_CommandIndex;

//Register commands and build the command lookup table
void RegisterCommand(const std::string& name,
    const std::string& help,
    std::function<void(const std::string&)> handler)
{
    const size_t index = g_Commands.size();
    g_Commands.push_back({ name, help, std::move(handler) });
    g_CommandIndex.emplace(g_Commands.back().name, index);
}

//Function used to normalize the mapnames so the map lookup and map commands became non-case-sensitive
std::string NormalizeKey(std::string_view value)
{
    std::string normalized;
    normalized.reserve(value.size());

    for (unsigned char ch : value)
        normalized.push_back(static_cast<char>(std::tolower(ch)));

    return normalized;
}

// ======================================================
//  MAP LISTS AND MAP LOGIC
// ======================================================

// Define the map information structure
struct MapInfo {
    std::string_view name;
    bool pveBug;
};

// List of maps with their PVE bug status
const std::array<MapInfo, 10> MapList{ {
    { "OSS",         false },
    { "MiniFarm",    false },
    { "Warehouse",   false },
    { "Dusty",       true  },
    { "DataCenter",  false },
    { "CircularX",   false },
    { "Museum_art",  true  },
    { "RelayStation",true  },
    { "Oriolus",     true  },
    { "GangesRiver", true  }
} };

//Creates the actuall index hash map of the maplist, and making all keys normalized for non-case-sensitive lookup
std::unordered_map<std::string, size_t> BuildMapLookup()
{
    std::unordered_map<std::string, size_t> lookup; //create hashmap
    lookup.reserve(MapList.size()); //Pre‑allocates memory of the list size
    //lookthrough and normalize
    for (size_t index = 0; index < MapList.size(); ++index)
        lookup.emplace(NormalizeKey(MapList[index].name), index);

    return lookup;
}
const std::unordered_map<std::string, size_t> g_MapLookup = BuildMapLookup(); //define then call

const MapInfo* FindMapInfo(std::string_view name)
{
    const auto it = g_MapLookup.find(NormalizeKey(name));
    if (it == g_MapLookup.end())
        return nullptr;

    return &MapList[it->second];
}

bool IsMapAllowedForCurrentMode(std::string_view name)
{
    const MapInfo* map = FindMapInfo(name);
    if (!map)
        return false;

    return !(_stricmp(CurrentMode.c_str(), "pve") == 0 && map->pveBug);
}

std::filesystem::path GetServerConfigPath()
{
    std::error_code error;
    const std::filesystem::path configuredPath(g_ServerConfigPath);
    const std::filesystem::path absolutePath =
        std::filesystem::absolute(configuredPath, error);

    if (error)
        return configuredPath;

    return absolutePath.lexically_normal();
}

bool ValidateMultiMatchConfig(MultiMatchConfig& config, std::string& error)
{
    if (!config.enabled)
        return true;

    if (config.playlist.empty())
    {
        error = "playlist must contain at least one map";
        return false;
    }

    if (config.travelTimeoutSeconds < 10 || config.travelTimeoutSeconds > 180)
    {
        error = "travelTimeoutSeconds must be between 10 and 180";
        return false;
    }

    if (config.voteDurationSeconds < 0 || config.voteDurationSeconds > 60)
    {
        error = "vote.durationSeconds must be between 0 and 60";
        return false;
    }

    if (config.voteCandidateCount < 1 || config.voteCandidateCount > 3)
    {
        error = "vote.candidateCount must be between 1 and 3";
        return false;
    }

    std::vector<std::string> canonicalPlaylist;
    canonicalPlaylist.reserve(config.playlist.size());

    std::unordered_map<std::string, bool> seen;
    seen.reserve(config.playlist.size());

    for (const std::string& requestedMap : config.playlist)
    {
        const MapInfo* map = FindMapInfo(requestedMap);
        if (!map)
        {
            error = "playlist contains unknown map '" + requestedMap + "'";
            return false;
        }

        if (!IsMapAllowedForCurrentMode(requestedMap))
        {
            error = "playlist map '" + std::string(map->name) +
                "' is not allowed for mode '" + CurrentMode + "'";
            return false;
        }

        const std::string normalized = NormalizeKey(map->name);
        if (seen.find(normalized) != seen.end())
        {
            error = "playlist contains duplicate map '" + std::string(map->name) + "'";
            return false;
        }

        seen.emplace(normalized, true);
        canonicalPlaylist.emplace_back(map->name);
    }

    // Store canonical game map names so later travel arguments are stable
    // regardless of the spelling used in serverconfig.json.
    config.playlist = std::move(canonicalPlaylist);
    return true;
}

std::string PickNextPlaylistMap()
{
    if (!g_MultiMatch.enabled || g_MultiMatch.playlist.empty())
        return CurrentMap;

    for (size_t index = 0; index < g_MultiMatch.playlist.size(); ++index)
    {
        if (_stricmp(g_MultiMatch.playlist[index].c_str(), CurrentMap.c_str()) == 0)
            return g_MultiMatch.playlist[(index + 1) % g_MultiMatch.playlist.size()];
    }

    return g_MultiMatch.playlist.front();
}

void ObserveMultiMatchStatusLine(const std::string& line, const uint64_t generation)
{
    constexpr std::string_view marker = "[MULTIMATCH_STATUS] ";
    if (line.rfind(marker, 0) != 0)
        return;

    const std::string payload = line.substr(marker.size());
    const json status = json::parse(payload, nullptr, false);
    if (status.is_discarded() || !status.is_object() ||
        !status.value("enabled", false) ||
        !status.contains("activeMap") || !status["activeMap"].is_string())
    {
        return;
    }

    const std::string reportedMap = status["activeMap"].get<std::string>();
    const MapInfo* const map = FindMapInfo(reportedMap);
    if (!map)
        return;

    std::lock_guard<std::mutex> lock(g_ServerMutex);
    if (generation != g_ServerGeneration.load() || !g_MultiMatch.enabled)
        return;

    if (_stricmp(CurrentMap.c_str(), map->name.data()) != 0)
    {
        CurrentMap = std::string(map->name);
        LauncherLog("Observed in-process playlist advance to: " + CurrentMap);
    }
}

//Picks a random map from the map list, but avoid picking the last played map to prevent back-to-back same map rotation. If all maps are ineligible, returns the last map used
std::string PickRandomMapAvoidingLast()
{
    static std::mt19937 rng(std::random_device{}());
    size_t eligibleCount = 0;
    std::string selected = LastMap;

    for (const auto& m : MapList)
    {
        if (m.name == LastMap) continue;
        if (CurrentMode == "pve" && m.pveBug) continue;

        ++eligibleCount;
        if (std::uniform_int_distribution<size_t>(1, eligibleCount)(rng) == 1)
            selected.assign(m.name);
    }

    if (eligibleCount == 0)
        return LastMap;

    return selected;
}
// For the listmap command
void PrintMapList()
{
    LauncherLog("=== Available Maps ===");

    for (const auto& m : MapList)
    {
        if (m.pveBug)
            std::cout << m.name << "  [FORBIDDEN: PVE BUG]" << std::endl;
        else
            std::cout << m.name << std::endl;
    }

    LauncherLog("======================");
}


// ======================================================
//  CONFIGURATION COMMANDS
// ======================================================

void SetMap(const std::string& name)
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);

    const auto it = g_MapLookup.find(NormalizeKey(name));
    if (it == g_MapLookup.end())
    {
        LauncherLog("Unknown map: " + name);
        return;
    }

    const MapInfo& map = MapList[it->second];
    if (map.pveBug && CurrentMode == "pve")
    {
        LauncherLog("Map '" + name + "' is forbidden due to PVE bug.");
        return;
    }

    CurrentMap.assign(map.name);
    LauncherLog("Map set to: " + CurrentMap);
}

void SetMode(const std::string& mode)
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);

    if (_stricmp(mode.c_str(), "pvp") == 0)
    {
        CurrentMode = "pvp";
        LauncherLog("Mode set to PvP.");
    }
    else if (_stricmp(mode.c_str(), "pve") == 0)
    {
        CurrentMode = "pve";
        LauncherLog("Mode set to PvE.");
    }
    else
    {
        LauncherLog("Invalid mode. Use: pvp or pve");
    }
}

void SetDifficulty(const std::string& diff)
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);

    if (_stricmp(diff.c_str(), "easy") == 0)
    {
        CurrentDifficulty = "easy";
        LauncherLog("Difficulty set to EASY.");
    }
    else if (_stricmp(diff.c_str(), "normal") == 0)
    {
        CurrentDifficulty = "normal";
        LauncherLog("Difficulty set to NORMAL.");
    }
    else if (_stricmp(diff.c_str(), "hard") == 0)
    {
        CurrentDifficulty = "hard";
        LauncherLog("Difficulty set to HARD.");
    }
    else
    {
        LauncherLog("Invalid difficulty. Use: easy, normal, hard");
    }
}

void InitCommands()
{
    g_Commands.reserve(16);
    g_CommandIndex.reserve(16);

    RegisterCommand("maplist", "Show all maps", [](const std::string& args) {
        PrintMapList();
        });

    RegisterCommand("setmap", "setmap <name>", [](const std::string& args) {
        SetMap(args);
        });

    RegisterCommand("setmode", "setmode <pvp|pve>", [](const std::string& args) {
        SetMode(args);
        });

    RegisterCommand("difficulty", "difficulty <easy|normal|hard>", [](const std::string& args) {
        SetDifficulty(args);
        });

    RegisterCommand("killserver", "Kill the running server", [](const std::string& args) {
        KillServer();
        });

    RegisterCommand("restart", "Restart the server", [](const std::string& args) {
        RestartServer();
        });

    RegisterCommand("online", "online [backend]", [](const std::string& args) {
        std::lock_guard<std::mutex> lock(g_ServerMutex);
        if (args.empty())
            OnlineBackend = DEFAULT_BACKEND;
        else
            OnlineBackend = args;
        OfflineMode = false;
        LauncherLog("Online mode enabled. Backend = " + OnlineBackend);
        });

    RegisterCommand("offline", "Disable backend", [](const std::string& args) {
        std::lock_guard<std::mutex> lock(g_ServerMutex);
        OfflineMode = true;
        LauncherLog("Offline mode enabled.");
        });

    RegisterCommand("servername", "servername <name>", [](const std::string& args) {
        std::lock_guard<std::mutex> lock(g_ServerMutex);
        ServerName = args;
        LauncherLog("Server name set to: " + ServerName);
        });

    RegisterCommand("serverregion", "serverregion <region>", [](const std::string& args) {
        std::lock_guard<std::mutex> lock(g_ServerMutex);
        ServerRegion = args;
        LauncherLog("Server region set to: " + ServerRegion);
        });

    RegisterCommand("setport", "setport <1-65535>", [](const std::string& args) {
        try {
            int p = std::stoi(args);
            if (p < 1 || p > 65535)
                LauncherLog("Invalid port.");
            else {
                std::lock_guard<std::mutex> lock(g_ServerMutex);
                g_ServerPort = p;
                LauncherLog("Server port set to: " + std::to_string(p));
            }
        }
        catch (...) {
            LauncherLog("Invalid port format.");
        }
        });

    RegisterCommand("setexternal", "setexternal <1-65535>", [](const std::string& args) {
        try {
            int p = std::stoi(args);
            if (p < 1 || p > 65535)
                LauncherLog("Invalid external port.");
            else {
                std::lock_guard<std::mutex> lock(g_ServerMutex);
                g_ExternalPort = p;
                LauncherLog("External port set to: " + std::to_string(p));
            }
        }
        catch (...) {
            LauncherLog("Invalid external port format.");
        }
        });

    RegisterCommand("saveconfig", "Save current settings to serverconfig.json", [](const std::string& args) {
        SaveConfigFile();
        LauncherLog("Configuration saved.");
        });

    RegisterCommand("reloadconfig", "Reload settings from serverconfig.json", [](const std::string& args) {
        if (LoadConfigFile())
            LauncherLog("Configuration reloaded.");
        else
            LauncherLog("Failed to reload configuration.");
        });

    RegisterCommand("status", "Show current server status", [](const std::string& args) {
        const ServerState state = g_ServerState.load();
        LauncherLog("=== Server Status ===");
        LauncherLog("Map: " + CurrentMap);
        LauncherLog("Mode: " + CurrentMode);
        LauncherLog("Difficulty: " + CurrentDifficulty);
        LauncherLog("Server Name: " + ServerName);
        LauncherLog("Region: " + ServerRegion);
        LauncherLog("Port: " + std::to_string(g_ServerPort));
        LauncherLog("External Port: " + std::to_string(g_ExternalPort));
        LauncherLog("Backend: " + (OfflineMode ? "Offline" : OnlineBackend));
        LauncherLog("State: " + std::string(ServerStateToString(state)));
        LauncherLog("Multi-match: " + std::string(g_MultiMatch.enabled ? "Enabled" : "Disabled"));
        if (g_MultiMatch.enabled)
        {
            std::string playlist;
            for (size_t index = 0; index < g_MultiMatch.playlist.size(); ++index)
            {
                if (index != 0)
                    playlist += " -> ";
                playlist += g_MultiMatch.playlist[index];
            }

            LauncherLog("Multi-match playlist: " + playlist);
            LauncherLog("Travel timeout: " + std::to_string(g_MultiMatch.travelTimeoutSeconds) + "s");
            LauncherLog("Vote: " + std::string(g_MultiMatch.voteEnabled ? "Enabled" : "Disabled") +
                ", duration=" + std::to_string(g_MultiMatch.voteDurationSeconds) + "s" +
                ", candidates=" + std::to_string(g_MultiMatch.voteCandidateCount));
        }
        });

    RegisterCommand("help", "Show all commands", [](const std::string& args) {
        LauncherLog("Available commands:");
        for (auto& c : g_Commands)
            std::cout << "  " << c.name << " - " << c.help << std::endl;
        });
}
void InputThread()
{
    while (true)
    {
        std::string line;
        std::getline(std::cin, line);

        if (line.empty())
            continue;

        std::string cmd, args;
        size_t space = line.find(' ');
        if (space == std::string::npos)
        {
            cmd = line;
            args = "";
        }
        else
        {
            cmd = line.substr(0, space);
            args = line.substr(space + 1);
        }

        const auto commandIt = g_CommandIndex.find(cmd);
        if (commandIt != g_CommandIndex.end())
        {
            g_Commands[commandIt->second].handler(args);
            continue;
        }

        LauncherLog("Unknown command. Type 'help' for list.");
    }
}

// ======================================================
//  BUILD STARTUP COMMNADLINE
// ======================================================

std::string GetCmdValue(const std::string& key)
{
    std::string cmd = GetCommandLineA();
    size_t pos = cmd.find(key);
    if (pos == std::string::npos)
        return "";

    pos += key.length();
    size_t end = cmd.find(" ", pos);
    if (end == std::string::npos)
        end = cmd.length();

    return cmd.substr(pos, end - pos);
}

void LoadCommandLineConfig()
{
    std::string portArg = GetCmdValue("-port=");
    if (!portArg.empty())
        g_ServerPort = std::stoi(portArg);

    g_ExternalPort = g_ServerPort;

    std::string mapArg = GetCmdValue("-map=");
    if (!mapArg.empty())
        SetMap(mapArg);

    std::string modeArg = GetCmdValue("-mode=");
    if (!modeArg.empty())
    {
        if (modeArg.find("PVE") != std::string::npos || modeArg.find("pve") != std::string::npos)
            SetMode("pve");
        else if (modeArg.find("PVP") != std::string::npos || modeArg.find("pvp") != std::string::npos)
            SetMode("pvp");
        else
            SetMode(modeArg);
    }

    std::string difficultyArg = GetCmdValue("-difficulty=");
    if (!difficultyArg.empty())
        SetDifficulty(difficultyArg);

    std::string serverNameArg = GetCmdValue("-servername=");
    if (!serverNameArg.empty())
        ServerName = serverNameArg;

    std::string serverRegionArg = GetCmdValue("-serverregion=");
    if (!serverRegionArg.empty())
        ServerRegion = serverRegionArg;

    std::string onlineArg = GetCmdValue("-online=");
    if (!onlineArg.empty())
    {
        OnlineBackend = onlineArg;
        OfflineMode = false;
    }

    std::string roomIdArg = GetCmdValue("-roomid=");
    if (!roomIdArg.empty())
        HostRoomId = roomIdArg;

    std::string hostTokenArg = GetCmdValue("-hosttoken=");
    if (!hostTokenArg.empty())
        HostToken = hostTokenArg;

    std::string serverIdArg = GetCmdValue("-serverid=");
    if (!serverIdArg.empty())
        ServerUniqueId = serverIdArg;

    std::string publicHostArg = GetCmdValue("-publichost=");
    if (!publicHostArg.empty())
        PublicHost = publicHostArg;

    std::string maxPlayersArg = GetCmdValue("-maxplayers=");
    if (!maxPlayersArg.empty())
        g_MaxPlayers = std::stoi(maxPlayersArg);

    std::string toolboxPipeArg = GetCmdValue("-pipe=");
    if (!toolboxPipeArg.empty())
        ToolboxPipeName = toolboxPipeArg;

    std::string gameExeArg = GetCmdValue("-gameexe=");
    if (!gameExeArg.empty())
        GameExePath = gameExeArg;
}

// ======================================================
//  CONFIG FILE LOADING AND SAVING
// ======================================================

bool LoadConfigFile()
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);

    const std::filesystem::path path = GetServerConfigPath();

    if (!std::filesystem::exists(path))
        return false;

    std::ifstream f(path);
    if (!f.is_open())
        return false;

    json j;
    try {
        f >> j;
    }
    catch (...) {
        LauncherLog("Config file exists but is invalid JSON.");
        return false;
    }

    if (j.contains("map") && j["map"].is_string())
        CurrentMap = j["map"];

    if (j.contains("mode") && j["mode"].is_string())
        CurrentMode = j["mode"];

    if (j.contains("difficulty") && j["difficulty"].is_string())
        CurrentDifficulty = j["difficulty"];

    if (j.contains("serverName") && j["serverName"].is_string())
        ServerName = j["serverName"];

    if (j.contains("serverRegion") && j["serverRegion"].is_string())
        ServerRegion = j["serverRegion"];

    if (j.contains("port") && j["port"].is_number_integer())
        g_ServerPort = j["port"];

    if (j.contains("externalPort") && j["externalPort"].is_number_integer())
        g_ExternalPort = j["externalPort"];

    if (j.contains("backend") && j["backend"].is_string())
        OnlineBackend = j["backend"];

    if (j.contains("serverId") && j["serverId"].is_string())
        ServerUniqueId = j["serverId"];

    if (j.contains("publicHost") && j["publicHost"].is_string())
        PublicHost = j["publicHost"];

    if (j.contains("maxPlayers") && j["maxPlayers"].is_number_integer())
        g_MaxPlayers = j["maxPlayers"];

    if (j.contains("offline") && j["offline"].is_boolean())
        OfflineMode = j["offline"];

    if (j.contains("dx11") && j["dx11"].is_boolean())
        UseDX11 = j["dx11"];

    MultiMatchConfig loadedMultiMatch;
    std::string multiMatchShapeError;
    if (j.contains("multiMatch"))
    {
        const json& multiMatch = j["multiMatch"];
        if (!multiMatch.is_object())
        {
            multiMatchShapeError = "multiMatch must be an object";
        }
        else
        {
            if (multiMatch.contains("enabled"))
            {
                if (multiMatch["enabled"].is_boolean())
                    loadedMultiMatch.enabled = multiMatch["enabled"];
                else
                    multiMatchShapeError = "multiMatch.enabled must be a boolean";
            }

            if (multiMatch.contains("playlist"))
            {
                if (!multiMatch["playlist"].is_array())
                {
                    multiMatchShapeError = "multiMatch.playlist must be an array";
                }
                else
                {
                    for (const json& item : multiMatch["playlist"])
                    {
                        if (!item.is_string())
                        {
                            loadedMultiMatch.playlist.clear();
                            multiMatchShapeError =
                                "multiMatch.playlist contains a non-string entry";
                            break;
                        }
                        loadedMultiMatch.playlist.emplace_back(item.get<std::string>());
                    }
                }
            }

            if (multiMatch.contains("travelTimeoutSeconds"))
            {
                if (multiMatch["travelTimeoutSeconds"].is_number_integer())
                    loadedMultiMatch.travelTimeoutSeconds = multiMatch["travelTimeoutSeconds"];
                else
                    multiMatchShapeError =
                        "multiMatch.travelTimeoutSeconds must be an integer";
            }

            if (multiMatch.contains("vote"))
            {
                if (!multiMatch["vote"].is_object())
                {
                    multiMatchShapeError = "multiMatch.vote must be an object";
                }
                else
                {
                    const json& vote = multiMatch["vote"];
                    if (vote.contains("enabled"))
                    {
                        if (vote["enabled"].is_boolean())
                            loadedMultiMatch.voteEnabled = vote["enabled"];
                        else
                            multiMatchShapeError =
                                "multiMatch.vote.enabled must be a boolean";
                    }
                    if (vote.contains("durationSeconds"))
                    {
                        if (vote["durationSeconds"].is_number_integer())
                            loadedMultiMatch.voteDurationSeconds = vote["durationSeconds"];
                        else
                            multiMatchShapeError =
                                "multiMatch.vote.durationSeconds must be an integer";
                    }
                    if (vote.contains("candidateCount"))
                    {
                        if (vote["candidateCount"].is_number_integer())
                            loadedMultiMatch.voteCandidateCount = vote["candidateCount"];
                        else
                            multiMatchShapeError =
                                "multiMatch.vote.candidateCount must be an integer";
                    }
                }
            }
        }
    }

    std::string multiMatchError;
    if (!multiMatchShapeError.empty())
    {
        LauncherLog("Invalid multi-match configuration: " + multiMatchShapeError +
            "; multi-match remains disabled.");
        loadedMultiMatch = MultiMatchConfig{};
    }
    else if (!ValidateMultiMatchConfig(loadedMultiMatch, multiMatchError))
    {
        LauncherLog("Invalid multi-match configuration: " + multiMatchError +
            "; multi-match remains disabled.");
        loadedMultiMatch = MultiMatchConfig{};
    }

    if (loadedMultiMatch.enabled)
    {
        bool currentMapInPlaylist = false;
        for (const std::string& map : loadedMultiMatch.playlist)
        {
            if (_stricmp(map.c_str(), CurrentMap.c_str()) == 0)
            {
                currentMapInPlaylist = true;
                break;
            }
        }

        if (!currentMapInPlaylist)
        {
            LauncherLog("Configured map '" + CurrentMap +
                "' is not in the multi-match playlist; starting with '" +
                loadedMultiMatch.playlist.front() + "'.");
            CurrentMap = loadedMultiMatch.playlist.front();
        }
    }

    g_MultiMatch = std::move(loadedMultiMatch);

    LauncherLog("Loaded configuration from " + path.string());
    LauncherLog("Multi-match is " + std::string(g_MultiMatch.enabled ? "enabled" : "disabled") +
        " for the next child launch.");
    return true;
}

void SaveConfigFile()
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);

    json j;
    j["map"] = CurrentMap;
    j["mode"] = CurrentMode;
    j["difficulty"] = CurrentDifficulty;
    j["serverName"] = ServerName;
    j["serverRegion"] = ServerRegion;
    j["port"] = g_ServerPort;
    j["externalPort"] = g_ExternalPort;
    j["backend"] = OnlineBackend;
    j["serverId"] = ServerUniqueId;
    j["publicHost"] = PublicHost;
    j["maxPlayers"] = g_MaxPlayers;
    j["offline"] = OfflineMode;
    j["dx11"] = UseDX11;

    j["multiMatch"] = {
        {"enabled", g_MultiMatch.enabled},
        {"playlist", g_MultiMatch.playlist},
        {"travelTimeoutSeconds", g_MultiMatch.travelTimeoutSeconds},
        {"vote", {
            {"enabled", g_MultiMatch.voteEnabled},
            {"durationSeconds", g_MultiMatch.voteDurationSeconds},
            {"candidateCount", g_MultiMatch.voteCandidateCount}
        }}
    };

    const std::filesystem::path path = GetServerConfigPath();
    std::ofstream f(path);
    if (!f.is_open())
    {
        LauncherLog("Failed to save configuration to " + path.string());
        return;
    }
    f << j.dump(4);

    LauncherLog("Saved configuration to " + path.string());
}
// ======================================================
//  SERVER LIFECYCLE
// ======================================================

bool StopServerLocked()
{
    if (!g_ServerProcess)
    {
        g_ServerPid = 0;
        ServerRunning.store(false);
        g_ServerState.store(ServerState::Stopped);
        return true;
    }

    LauncherLog("Stopping server...");
    g_ServerState.store(ServerState::Stopping);
    ServerRunning.store(false);

    g_ServerGeneration.fetch_add(1);

    HANDLE process = g_ServerProcess;
    DWORD exitCode = 0;

    if (GetExitCodeProcess(process, &exitCode) && exitCode == STILL_ACTIVE)
    {
        if (!TerminateProcess(process, 0))
        {
            LauncherLog("TerminateProcess failed. GetLastError=" + std::to_string(GetLastError()));
            return false;
        }
    }

    const DWORD waitResult = WaitForSingleObject(process, 5000);
    if (waitResult != WAIT_OBJECT_0)
    {
        LauncherLog("ERROR: timed out waiting for server process to exit.");
        return false;
    }

    CloseHandle(process);
    g_ServerProcess = NULL;
    g_ServerPid = 0;
    g_ServerState.store(ServerState::Stopped);
    return true;
}

void KillServer()
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);
    StopServerLocked();
}

void RestartServer()
{
    RequestRestart(false, "manual restart");
}

void RequestRestart(bool rotateMap, const std::string& reason)
{
    if (g_WrapperShuttingDown.load())
        return;

    std::lock_guard<std::mutex> lock(g_ServerMutex);

    const ServerState state = g_ServerState.load();
    if (state == ServerState::Starting ||
        state == ServerState::Stopping ||
        state == ServerState::Restarting)
    {
        LauncherLog("Restart ignored: server lifecycle transition already in progress.");
        return;
    }

    auto now = std::chrono::steady_clock::now();
    if (now - g_LastFailureTime < FAILURE_RESET_WINDOW)
        ++g_ConsecutiveFailures;
    else
        g_ConsecutiveFailures = 1;
    g_LastFailureTime = now;

    if (g_ConsecutiveFailures.load() >= MAX_FAILURES)
    {
        LauncherLog("CRITICAL: Server failed to restart 3 times in 1 minute. Stopping auto-restart.");
        MessageBoxA(NULL,
            "Server failed to restart repeatedly.\n"
            "Possible reasons:\n"
            "- The configured UDP port is occupied by another program.\n"
            "- Map file missing or corrupt.\n"
            "- Antivirus blocking the executable.\n\n"
            "Please check the logs and restart the launcher manually.",
            "Project Boundary Server Wrapper",
            MB_OK | MB_ICONERROR);
        g_ServerState.store(ServerState::Stopped);
        ServerRunning.store(false);
        g_ConsecutiveFailures = 0;
        return;
    }

    g_ServerState.store(ServerState::Restarting);
    LauncherLog("Restarting server (" + reason + ")...");

    if (rotateMap)
    {
        LastMap = CurrentMap;
        if (g_MultiMatch.enabled)
        {
            CurrentMap = PickNextPlaylistMap();
            LauncherLog("Recovering multi-match from the next playlist map: " + CurrentMap);
        }
        else
        {
            CurrentMap = PickRandomMapAvoidingLast();
            LauncherLog("Auto-rotating map to: " + CurrentMap);
        }
    }

    if (!StopServerLocked())
        return;

    if (!LaunchServerLocked())
        LauncherLog("ERROR: restart failed.");
}


// ======================================================
//  PROCESS I/O AND WATCHDOGS
// ======================================================

void PipeReader(HANDLE pipe, uint64_t generation)
{
    char buffer[4096];
    DWORD bytesRead = 0;
    std::string pendingLines;

    while (true)
    {
        if (!ReadFile(pipe, buffer, sizeof(buffer) - 1, &bytesRead, NULL) || bytesRead == 0)
            break;

        buffer[bytesRead] = '\0';
        std::string msg(buffer, bytesRead);

        pendingLines.append(msg);
        size_t newline = std::string::npos;
        while ((newline = pendingLines.find('\n')) != std::string::npos)
        {
            std::string line = pendingLines.substr(0, newline);
            if (!line.empty() && line.back() == '\r')
                line.pop_back();
            ObserveMultiMatchStatusLine(line, generation);
            pendingLines.erase(0, newline + 1);
        }
        if (pendingLines.size() > 64 * 1024)
            pendingLines.clear();

        // Detect heartbeat
        if (msg.find("[HEARTBEAT]") != std::string::npos && generation == g_ServerGeneration.load())
        {
            ResetHeartbeatClock();
            HeartbeatSeen = true;
            g_ConsecutiveFailures = 0;
            LauncherLog("Heartbeat received");
        }

        std::lock_guard<std::mutex> lock(g_LogMutex);
        logFile << msg;
        logFile.flush();
        std::cout << msg;
    }

    CloseHandle(pipe);
    LauncherLog("PipeReader thread ended.");
}

void HideGameWindow(DWORD pid)
{
    HWND hwnd = NULL;

    while ((hwnd = FindWindowExW(NULL, hwnd, NULL, NULL)) != NULL)
    {
        DWORD windowPID = 0;
        GetWindowThreadProcessId(hwnd, &windowPID);

        if (windowPID == pid)
        {
            ShowWindow(hwnd, SW_HIDE);
        }
    }
}

BOOL WINAPI ConsoleHandler(DWORD ctrlType)
{
    (void)ctrlType;
    g_WrapperShuttingDown.store(true);

    std::lock_guard<std::mutex> lock(g_ServerMutex);
    StopServerLocked();

    return FALSE;
}

void StartWatchdog(HANDLE processHandle, uint64_t generation)
{
    std::thread([processHandle, generation]() {
        const auto startupTimeout = std::chrono::seconds(120);
        const auto heartbeatTimeout = std::chrono::seconds(30);

        while (true)
        {
            if (g_WrapperShuttingDown.load())
                break;

            if (generation != g_ServerGeneration.load())
                break;

            if (g_ServerState.load() != ServerState::Running)
                break;

            DWORD code = 0;
            if (!GetExitCodeProcess(processHandle, &code) || code != STILL_ACTIVE)
                break;

            if (HasHeartbeatTimedOut(HeartbeatSeen ? heartbeatTimeout : startupTimeout))
            {
                LauncherLog(HeartbeatSeen
                    ? "Heartbeat timeout — server frozen."
                    : "Startup timeout — server did not finish initialization.");

                RequestRestart(true, HeartbeatSeen ? "heartbeat timeout" : "startup timeout");
                break;
            }

            Sleep(1000);
        }

        CloseHandle(processHandle);
        }).detach();
}

void StartExitWatcher(HANDLE processHandle, uint64_t generation)
{
    std::thread([processHandle, generation]() {
        WaitForSingleObject(processHandle, INFINITE);
        DWORD exitCode = 0;
        const bool haveExitCode = GetExitCodeProcess(processHandle, &exitCode) != FALSE;
        CloseHandle(processHandle);

        if (g_WrapperShuttingDown.load())
            return;

        if (generation != g_ServerGeneration.load())
            return;

        if (g_ServerState.load() != ServerState::Running)
            return;

        LauncherLog("Server exited unexpectedly." +
            std::string(haveExitCode ? " exitCode=" + std::to_string(exitCode) : " exitCode=<unavailable>"));
        RequestRestart(true, "process exit");
        }).detach();
}

// ======================================================
//  PORT CHECKING
// ======================================================

bool IsPortAvailable(int port, bool useTCP = false)
{
    WSADATA wsa;
    if (WSAStartup(MAKEWORD(2, 2), &wsa) != 0)
        return false;

    int sockType = useTCP ? SOCK_STREAM : SOCK_DGRAM;
    int protocol = useTCP ? IPPROTO_TCP : IPPROTO_UDP;
    SOCKET sock = socket(AF_INET, sockType, protocol);
    if (sock == INVALID_SOCKET) {
        WSACleanup();
        return false;
    }

    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(port);

    int result = bind(sock, (sockaddr*)&addr, sizeof(addr));
    closesocket(sock);
    WSACleanup();

    return result != SOCKET_ERROR;
}


// ======================================================
//  SERVER LAUNCHING
// ======================================================

std::wstring QuoteWindowsArgument(const std::filesystem::path& value)
{
    // The configuration path is passed as one explicit, quoted argument. A
    // Windows path cannot contain a double quote, so no additional command
    // line escaping is needed here.
    return L"\"" + value.wstring() + L"\"";
}

bool LaunchServerLocked()
{
    if (g_WrapperShuttingDown.load())
        return false;

    const ServerState state = g_ServerState.load();
    if (state == ServerState::Starting || state == ServerState::Running)
    {
        LauncherLog("Launch ignored: server is already starting or running.");
        return false;
    }

    int serverPort = g_ServerPort;
    if (!IsPortAvailable(serverPort)) {
        LauncherLog("ERROR: UDP Port " + std::to_string(serverPort) + " is already in use!");
        g_ServerState.store(ServerState::Stopped);
        ServerRunning.store(false);
        return false;
    }

    g_ServerState.store(ServerState::Starting);
    ServerRunning.store(false);
    LauncherLog("Launching server process...");
    HeartbeatSeen = false;
    ResetHeartbeatClock();  // updates g_LastHeartbeatTickMs
    g_ServerLaunchTime = std::chrono::steady_clock::now();

    SECURITY_ATTRIBUTES sa{ sizeof(SECURITY_ATTRIBUTES), NULL, TRUE };
    HANDLE readPipe = NULL;
    HANDLE writePipe = NULL;

    if (!CreatePipe(&readPipe, &writePipe, &sa, 0))
    {
        LauncherLog("CreatePipe failed. GetLastError=" + std::to_string(GetLastError()));
        g_ServerState.store(ServerState::Stopped);
        return false;
    }

    if (!SetHandleInformation(readPipe, HANDLE_FLAG_INHERIT, 0))
    {
        LauncherLog("SetHandleInformation failed. GetLastError=" + std::to_string(GetLastError()));
        CloseHandle(readPipe);
        CloseHandle(writePipe);
        g_ServerState.store(ServerState::Stopped);
        return false;
    }

    STARTUPINFOW si{};
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES | STARTF_USESHOWWINDOW;
    si.hStdOutput = writePipe;
    si.hStdError = writePipe;
    si.wShowWindow = SW_HIDE;

    PROCESS_INFORMATION pi{};

    std::string modePath;

    if (CurrentMode == "pve")
    {
        if (CurrentDifficulty == "easy")
            modePath = "/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Easy.BP_PBGameMode_Rush_PVE_Easy_C";
        else if (CurrentDifficulty == "hard")
            modePath = "/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Hard.BP_PBGameMode_Rush_PVE_Hard_C";
        else
            modePath = "/Game/Online/GameMode/BP_PBGameMode_Rush_PVE_Normal.BP_PBGameMode_Rush_PVE_Normal_C";
    }
    else
    {
        modePath = "/Game/Online/GameMode/PBGameMode_Rush_BP.PBGameMode_Rush_BP_C";
    }

    // Build command line
    std::wstring wGameExe(GameExePath.begin(), GameExePath.end());

    std::wstring cmd =
        L"\"" + wGameExe + L"\" "
        L"-log -server -nullrhi -nosplash -NoWindow "
        L"-map=" + std::wstring(CurrentMap.begin(), CurrentMap.end()) + L" "
        L"-mode=" + std::wstring(modePath.begin(), modePath.end()) + L" "
        L"-port=" + std::to_wstring(serverPort) + L" "
        L"-external=" + std::to_wstring(g_ExternalPort) + L" "
        + (CurrentMode == "pve" ? L"-pve " : L"");

    if (UseDX11)
        cmd += L"-dx11 ";

    std::wstring wName(ServerName.begin(), ServerName.end());
    cmd += L"-servername=" + wName + L" ";

    std::wstring wRegion(ServerRegion.begin(), ServerRegion.end());
    cmd += L"-serverregion=" + wRegion + L" ";

    if (!OfflineMode && !OnlineBackend.empty())
    {
        std::wstring wOnline(OnlineBackend.begin(), OnlineBackend.end());
        cmd += L"-online=" + wOnline + L" ";
    }

    if (!HostRoomId.empty())
    {
        std::wstring wRoomId(HostRoomId.begin(), HostRoomId.end());
        cmd += L"-roomid=" + wRoomId + L" ";
    }

    if (!HostToken.empty())
    {
        std::wstring wHostToken(HostToken.begin(), HostToken.end());
        cmd += L"-hosttoken=" + wHostToken + L" ";
    }

    if (!ServerUniqueId.empty())
        cmd += L"-serverid=" + std::wstring(ServerUniqueId.begin(), ServerUniqueId.end()) + L" ";
    if (!PublicHost.empty())
        cmd += L"-publichost=" + std::wstring(PublicHost.begin(), PublicHost.end()) + L" ";
    cmd += L"-maxplayers=" + std::to_wstring(g_MaxPlayers) + L" ";
    if (!ToolboxPipeName.empty())
        cmd += L"-pipe=" + std::wstring(ToolboxPipeName.begin(), ToolboxPipeName.end()) + L" ";

    if (g_MultiMatch.enabled)
    {
        const std::filesystem::path configPath = GetServerConfigPath();
        cmd += L"-DedicatedMultiMatch ";
        cmd += L"-multimatchconfig=" + QuoteWindowsArgument(configPath) + L" ";
        LauncherLog("Dedicated multi-match enabled; child config path=" + configPath.string());
    }

    if (!CreateProcessW(
        NULL,
        cmd.data(),
        NULL,
        NULL,
        TRUE,
        CREATE_NO_WINDOW | DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP,
        NULL,
        NULL,
        &si,
        &pi))
    {
        LauncherLog("Failed to launch server! GetLastError=" + std::to_string(GetLastError()));
        LauncherLog("Command line omitted because it contains registration credentials.");
        CloseHandle(readPipe);
        CloseHandle(writePipe);
        g_ServerState.store(ServerState::Stopped);
        return false;
    }

    CloseHandle(writePipe);
    CloseHandle(pi.hThread);

    g_ServerProcess = pi.hProcess;
    g_ServerPid = pi.dwProcessId;

    const uint64_t generation = g_ServerGeneration.fetch_add(1) + 1;
    ResetHeartbeatClock();

    ServerRunning.store(true);
    g_ServerState.store(ServerState::Running);

    std::thread(PipeReader, readPipe, generation).detach();

    HANDLE watchdogHandle = DuplicateProcessHandle(pi.hProcess);
    if (watchdogHandle)
        StartWatchdog(watchdogHandle, generation);

    HANDLE exitWatcherHandle = DuplicateProcessHandle(pi.hProcess);
    if (exitWatcherHandle)
        StartExitWatcher(exitWatcherHandle, generation);

    LauncherLog("Server launched. PID = " + std::to_string(pi.dwProcessId));

    HideGameWindow(pi.dwProcessId);
    LauncherLog("Server window hidden.");
    return true;
}

void LaunchServer()
{
    std::lock_guard<std::mutex> lock(g_ServerMutex);
    LaunchServerLocked();
}


// ======================================================
//  MAIN ENTRY POINT
// ======================================================

int main()
{
    ResetHeartbeatClock();
    LoadCommandLineConfig();
    ResetHeartbeatClock();
    SetConsoleCtrlHandler(ConsoleHandler, TRUE);

    std::filesystem::create_directory("logs");

    std::string logPath = "logs/log-" + CurrentTimestamp() + ".txt";
    logFile.open(logPath, std::ios::app);

    LauncherLog("Logging to: " + logPath);
    LauncherLog("Wrapper started.");
    LoadCommandLineConfig();
    LauncherLog("Configured UDP port: " + std::to_string(g_ServerPort));

    if (!LoadConfigFile())
    {
        LauncherLog("No config found. Creating default serverconfig.json...");
        SaveConfigFile();
    }

    InitCommands();
    std::thread(InputThread).detach();

    LaunchServer();

    while (true)
    {
        Sleep(1000);
    }
}
