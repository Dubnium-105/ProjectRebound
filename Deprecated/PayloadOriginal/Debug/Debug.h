// Debug.h

#ifndef DEBUG_H_STKDF
#define DEBUG_H_STKDF
#pragma once
#include <string>
#include <fstream>
#include <mutex>

extern std::mutex LogMutex;
extern std::string LogFilePath;
extern bool ServerDebugLogEnabled;
extern bool ClientDebugLogEnabled;
extern std::ofstream clientLogFile;

std::string CurrentTimestamp();
void ServerLog(const std::string &msg);
void ServerDebugLog(const std::string &msg);
void ClientLog(const std::string &msg);
void ClientDebugLog(const std::string &msg);
void InitDebugConsole();
void EnableUnrealConsole();
void DebugLocateSubsystems();
void DebugDumpSubsystemsToFile();
void DebugDumpWeaponPartsToFile();
void HotkeyThread();
void ClientAutoDumpThread();

#endif //DEBUG_H_STKDF