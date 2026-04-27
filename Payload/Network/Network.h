// Network.h
#pragma once
#include <string>
#include "../Libs/json.hpp"

nlohmann::json BuildServerStatusPayload();
nlohmann::json BuildRoomHeartbeatPayload();
std::string StripHttpScheme(const std::string &backend);
bool PostJsonToBackend(const std::string &backend, const std::string &path, const nlohmann::json &payload);
void SendServerStatus(const std::string &backend);
bool SendRoomLifecycleStart(const std::string &backend);
void ReportRoomStartedIfNeeded();
void StartHeartbeatThread();