#pragma once

// Small, UObject-free command-line helpers.  The injected process receives a
// Windows command line, but loadout/server routing must be based on complete
// argv-style tokens instead of substring matches (for example, -servername
// must never imply -server).

#include <algorithm>
#include <cctype>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace CommandLinePolicy
{
    inline std::vector<std::string> Tokenize(std::string_view commandLine)
    {
        std::vector<std::string> result;
        std::size_t cursor = 0;
        while (cursor < commandLine.size())
        {
            while (cursor < commandLine.size() &&
                std::isspace(static_cast<unsigned char>(commandLine[cursor])) != 0)
            {
                ++cursor;
            }
            if (cursor == commandLine.size()) break;

            std::string token;
            bool quoted = false;
            while (cursor < commandLine.size())
            {
                const char ch = commandLine[cursor++];
                if (ch == '"')
                {
                    quoted = !quoted;
                    continue;
                }
                if (!quoted && std::isspace(static_cast<unsigned char>(ch)) != 0)
                    break;
                token.push_back(ch);
            }
            if (!token.empty()) result.push_back(std::move(token));
        }
        return result;
    }

    inline bool EqualsAsciiInsensitive(std::string_view left, std::string_view right)
    {
        return left.size() == right.size() &&
            std::equal(left.begin(), left.end(), right.begin(), [](char a, char b) {
                return std::tolower(static_cast<unsigned char>(a)) ==
                    std::tolower(static_cast<unsigned char>(b));
            });
    }

    inline bool HasExactSwitch(std::string_view commandLine, std::string_view key)
    {
        const auto tokens = Tokenize(commandLine);
        return std::any_of(tokens.begin(), tokens.end(), [&](const std::string& token) {
            return EqualsAsciiInsensitive(token, key);
        });
    }

    inline std::optional<std::string> GetValue(
        std::string_view commandLine,
        std::string_view keyWithEquals)
    {
        const auto tokens = Tokenize(commandLine);
        for (const std::string& token : tokens)
        {
            if (token.size() < keyWithEquals.size()) continue;
            if (!EqualsAsciiInsensitive(
                std::string_view(token).substr(0, keyWithEquals.size()), keyWithEquals))
            {
                continue;
            }
            return token.substr(keyWithEquals.size());
        }
        return std::nullopt;
    }

    inline bool FeatureEnabled(
        std::string_view commandLine,
        std::string_view key,
        bool defaultValue = true)
    {
        const std::string valuePrefix = std::string(key) + "=";
        const auto value = GetValue(commandLine, valuePrefix);
        if (!value) return defaultValue;
        if (EqualsAsciiInsensitive(*value, "0") ||
            EqualsAsciiInsensitive(*value, "false"))
        {
            return false;
        }
        if (EqualsAsciiInsensitive(*value, "1") ||
            EqualsAsciiInsensitive(*value, "true"))
        {
            return true;
        }
        return defaultValue;
    }
}
