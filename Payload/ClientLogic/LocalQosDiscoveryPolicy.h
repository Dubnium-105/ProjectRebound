#pragma once

#include "../Config/CommandLinePolicy.h"

#include <charconv>
#include <cstdint>
#include <string>
#include <string_view>

namespace LocalQosDiscoveryPolicy
{
    enum class State
    {
        Disabled,
        Enabled,
        Invalid,
    };

    struct Decision
    {
        State state = State::Disabled;
        std::string discoveryUrl;
        std::string readyEvent;
        std::string error;
    };

    inline bool IsValidDiscoveryUrl(std::string_view value)
    {
        constexpr std::string_view prefix = "http://127.0.0.1:";
        constexpr std::string_view suffix = "/servers";
        if (!value.starts_with(prefix) || !value.ends_with(suffix))
            return false;
        const std::string_view portText = value.substr(
            prefix.size(), value.size() - prefix.size() - suffix.size());
        if (portText.empty() || portText.size() > 5)
            return false;
        uint32_t port = 0;
        const auto parsed = std::from_chars(
            portText.data(), portText.data() + portText.size(), port);
        return parsed.ec == std::errc{} && parsed.ptr == portText.data() + portText.size() &&
            port > 0 && port <= 65535;
    }

    inline bool IsValidReadyEvent(std::string_view value)
    {
        constexpr std::string_view prefix = R"(Local\ProjectRebound.Qos.)";
        if (!value.starts_with(prefix) || value.size() != prefix.size() + 32)
            return false;
        for (const char valueChar : value.substr(prefix.size()))
        {
            const bool decimal = valueChar >= '0' && valueChar <= '9';
            const bool lowerHex = valueChar >= 'a' && valueChar <= 'f';
            if (!decimal && !lowerHex)
                return false;
        }
        return true;
    }

    inline Decision Evaluate(std::string_view commandLine)
    {
        const auto url = CommandLinePolicy::GetValue(
            commandLine, "-LocalPveQosDiscoveryUrl=");
        const auto event = CommandLinePolicy::GetValue(
            commandLine, "-LocalPveQosReadyEvent=");
        if (!url && !event)
            return {};
        if (!url || !event)
        {
            return {State::Invalid, {}, {},
                "local PvE QoS switches must be supplied together"};
        }
        if (!IsValidDiscoveryUrl(*url))
            return {State::Invalid, {}, {}, "local PvE QoS URL is invalid"};
        if (!IsValidReadyEvent(*event))
            return {State::Invalid, {}, {}, "local PvE QoS readiness event is invalid"};
        return {State::Enabled, *url, *event, {}};
    }
}
