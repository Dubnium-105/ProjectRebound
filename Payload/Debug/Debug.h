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
void HotkeyThreadWithDebugTool();  // 扩展热键：F5=dump, F6=state, F7=reapply, F9=range
void DebugLocateSubsystems();
void DebugDumpSubsystemsToFile();
void DebugDumpWeaponPartsToFile();
