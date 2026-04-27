// Debug.h
#pragma once
#include <string>
#include <fstream>

extern bool ClientDebugLogEnabled;
extern std::ofstream clientLogFile;

std::string CurrentTimestamp();
void Log(const std::string& msg);
void ClientLog(const std::string& msg);
void InitDebugConsole();
void EnableUnrealConsole();
void HotkeyThread();
void DebugLocateSubsystems();
void DebugDumpSubsystemsToFile();
void DebugDumpWeaponPartsToFile();
