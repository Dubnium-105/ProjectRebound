// ======================================================
//  PROJECT BOUNDARY SERVER WRAPPER – PUBLIC INTERFACE
// ======================================================

#pragma once
#include <string>
#include <atomic>
#include <mutex>

// --- External log callback type ---
using LogCallback = void(*)(const std::string& msg);
using ConfigChangeCallback = void(*)();
void SetConfigChangeCallback(ConfigChangeCallback callback);

// --- Core lifecycle ---
void InitWrapperCore();                     // load config, open log file, init command system
void StartConsoleInput();                   // launch stdin reader thread (CLI only)
void RunWrapperLoop();                      // start server & wait for shutdown signal
void WrapperMain();                         // combined CLI entry: InitCore + Input + RunLoop

//Server control
void LaunchServer();
void RestartServer();
void KillServer();

//Command execution (shared by CLI & GUI)
void ExecuteConsoleCommand(const std::string& line);

//External log hook
void SetExternalLogCallback(LogCallback callback);

//Global shutdown flag (set by UI or signal)
extern std::atomic<bool> g_WrapperShuttingDown;

//ServerConfigs
extern std::mutex g_ServerMutex;
extern std::string CurrentMap;
extern std::string CurrentMode;
extern std::string CurrentDifficulty;
extern std::string ServerName;
extern std::string ServerRegion;
extern int g_ServerPort;
extern int g_ExternalPort;
extern std::string OnlineBackend;
extern bool OfflineMode;
extern bool UseDX11;

bool LoadConfigFile();
void SaveConfigFile();
void LauncherLog(const std::string &msg);