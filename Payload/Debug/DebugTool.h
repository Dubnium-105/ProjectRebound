#pragma once

#include <string>
#include "../Libs/json.hpp"

class DebugTool
{
public:
    void ExecuteHotkey(int vkCode);
    void ExecuteChat(const std::string& payload);
    nlohmann::json ExecuteJson(const nlohmann::json& args);
};
