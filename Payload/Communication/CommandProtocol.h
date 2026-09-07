#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>

#include "../Libs/json.hpp"

namespace CommandProtocol
{
    inline constexpr char Delimiter = '\t';
    inline constexpr char Newline = '\n';
    inline constexpr std::size_t MaxFrameBytes = 64U * 1024U;
    // Includes scoped acknowledgement names such as
    // confirm_client_match_connection_ack.
    inline constexpr std::size_t MaxCommandBytes = 64U;
    inline constexpr std::size_t MaxRequestIdBytes = 128U;
    inline constexpr std::size_t MaxMatchTargetBytes = 512U;
    inline constexpr std::size_t MaxTokenBytes = 4096U;

    struct Request
    {
        std::string command;
        nlohmann::json arguments = nlohmann::json::object();
        std::optional<std::string> requestId;
    };

    struct ParseResult
    {
        std::optional<Request> request;
        std::string errorCode;
        std::string errorMessage;
        std::optional<std::string> requestId;

        [[nodiscard]] bool Succeeded() const noexcept
        {
            return request.has_value();
        }
    };

    struct MatchEndpoint
    {
        std::string host;
        std::uint16_t port = 0;
    };

    [[nodiscard]] ParseResult ParseFrame(std::string_view frame);

    [[nodiscard]] bool ValidateMatchTarget(
        std::string_view target,
        std::string* failureReason = nullptr);

    // Parse and format the one endpoint representation shared by the Payload
    // command channel and the transport controller. IPv6 is always emitted
    // as [address]:port; a bare address or zero port is never accepted.
    [[nodiscard]] std::optional<MatchEndpoint> ParseMatchTarget(
        std::string_view target,
        std::string* failureReason = nullptr);

    [[nodiscard]] std::string FormatMatchTarget(
        std::string_view host,
        std::uint16_t port);

    [[nodiscard]] nlohmann::json WithRequestId(
        nlohmann::json payload,
        const std::optional<std::string>& requestId);

    [[nodiscard]] nlohmann::json MakeError(
        std::string_view code,
        std::string_view message,
        const std::optional<std::string>& requestId = std::nullopt);

    [[nodiscard]] std::string EncodeFrame(
        std::string_view command,
        const nlohmann::json& payload);
}
