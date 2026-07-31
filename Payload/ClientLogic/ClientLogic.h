#pragma once

#include <atomic>
#include <string>

extern std::atomic<bool> LoginCompleted;

// Thread-safe producer API. The actual Unreal calls are performed by
// PumpPendingClientCommands from the ProcessEvent game thread.
[[nodiscard]] bool QueueConnectToMatch(const std::string& target);
void ConnectToMatch();
void AutoConnectToMatchFromCmdline();
void PumpPendingClientCommands();
