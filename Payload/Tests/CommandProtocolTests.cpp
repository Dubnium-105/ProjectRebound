#include "../Communication/CommandProtocol.h"

#include <filesystem>
#include <fstream>
#include <iostream>
#include <stdexcept>
#include <string>
#include <utility>
#include <vector>

namespace
{
    int failures = 0;

    void Expect(const bool condition, const char* const description)
    {
        if (condition)
            return;
        ++failures;
        std::cerr << "FAILED: " << description << '\n';
    }

    void TestValidFrames()
    {
        const auto ping = CommandProtocol::ParseFrame("ping\t{}");
        Expect(ping.Succeeded(), "ping frame parses");
        Expect(ping.request && ping.request->command == "ping", "ping command preserved");

        const auto join = CommandProtocol::ParseFrame(
            "join\t{\"ip\":\"127.0.0.1:7777\",\"request_id\":\"req-1\"}");
        Expect(join.Succeeded(), "join frame parses");
        Expect(join.requestId == std::optional<std::string>("req-1"), "request id extracted");
        Expect(join.request && join.request->arguments["ip"] == "127.0.0.1:7777", "join IP preserved");

        const auto status = CommandProtocol::ParseFrame(
            "server_status\t{\"request_id\":\"status-1\"}");
        Expect(status.Succeeded(), "server_status frame parses");
        Expect(status.request && status.request->command == "server_status",
            "server_status command preserved");
    }

    void TestInvalidFrames()
    {
        Expect(!CommandProtocol::ParseFrame("").Succeeded(), "empty frame rejected");
        Expect(!CommandProtocol::ParseFrame("ping{}").Succeeded(), "missing delimiter rejected");
        Expect(!CommandProtocol::ParseFrame("ping\t").Succeeded(), "empty JSON rejected");
        Expect(!CommandProtocol::ParseFrame("ping\t[]").Succeeded(), "array JSON rejected");
        Expect(!CommandProtocol::ParseFrame("ping\tnull").Succeeded(), "null JSON rejected");
        Expect(!CommandProtocol::ParseFrame("ping\t{broken}").Succeeded(), "malformed JSON rejected");
        Expect(!CommandProtocol::ParseFrame("Ping\t{}").Succeeded(), "uppercase command rejected");
        Expect(!CommandProtocol::ParseFrame("ping\t{\"request_id\":42}").Succeeded(), "non-string request id rejected");

        std::string oversized = "debug\t{\"value\":\"";
        oversized.append(CommandProtocol::MaxFrameBytes, 'x');
        oversized.append("\"}");
        Expect(!CommandProtocol::ParseFrame(oversized).Succeeded(), "oversized frame rejected");
    }

    void TestFrameSizeBoundary()
    {
        const std::string prefix = "debug\t{\"value\":\"";
        const std::string suffix = "\"}";
        std::string maximumFrame = prefix;
        maximumFrame.append(
            CommandProtocol::MaxFrameBytes - 1U - prefix.size() - suffix.size(),
            'x');
        maximumFrame.append(suffix);
        Expect(maximumFrame.size() + 1U == CommandProtocol::MaxFrameBytes,
            "maximum request wire frame is exactly 64 KiB");
        Expect(CommandProtocol::ParseFrame(maximumFrame).Succeeded(),
            "maximum request wire frame is accepted");

        maximumFrame.insert(maximumFrame.size() - suffix.size(), 1, 'x');
        Expect(!CommandProtocol::ParseFrame(maximumFrame).Succeeded(),
            "request exceeding wire limit by one byte is rejected");

        constexpr std::size_t encodedFrameOverhead =
            sizeof("debug\t{\"value\":\"\"}\n") - 1U;
        nlohmann::json maximumPayload{
            {"value", std::string(
                CommandProtocol::MaxFrameBytes - encodedFrameOverhead,
                'x')}
        };
        Expect(CommandProtocol::EncodeFrame("debug", maximumPayload).size() ==
            CommandProtocol::MaxFrameBytes,
            "maximum response wire frame is accepted");

        maximumPayload["value"] = maximumPayload["value"].get<std::string>() + "x";
        bool threw = false;
        try
        {
            (void)CommandProtocol::EncodeFrame("debug", maximumPayload);
        }
        catch (const std::length_error&)
        {
            threw = true;
        }
        Expect(threw, "response exceeding wire limit by one byte is rejected");
    }

    void TestMatchTargets()
    {
        Expect(CommandProtocol::ValidateMatchTarget("127.0.0.1:7777"), "IPv4 target accepted");
        Expect(CommandProtocol::ValidateMatchTarget("game.example.test:443"), "hostname target accepted");
        Expect(CommandProtocol::ValidateMatchTarget("[2001:db8::1]:7777"), "bracketed IPv6 target accepted");
        const auto ipv6 = CommandProtocol::ParseMatchTarget("[2001:db8::1]:7777");
        Expect(ipv6 && ipv6->host == "2001:db8::1" && ipv6->port == 7777,
            "bracketed IPv6 target is decoded into host and port");
        Expect(CommandProtocol::FormatMatchTarget("2001:db8::1", 7777) ==
            "[2001:db8::1]:7777", "IPv6 endpoint is encoded with brackets");
        Expect(CommandProtocol::FormatMatchTarget("[2001:db8::1]", 7777) ==
            "[2001:db8::1]:7777", "already-bracketed IPv6 endpoint is normalized");
        Expect(!CommandProtocol::ValidateMatchTarget("[2001:db8:::1]:7777"),
            "malformed bracketed IPv6 target rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1"), "missing port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:0"), "zero port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:65536"), "oversized port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:7777;quit"), "console injection rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("2001:db8::1:7777"), "unbracketed IPv6 rejected");
    }

    void TestResponses()
    {
        const auto confirmedFrame = CommandProtocol::EncodeFrame(
            "confirm_client_match_connection_ack", nlohmann::json::object());
        Expect(CommandProtocol::ParseFrame(confirmedFrame.substr(0, confirmedFrame.size() - 1)).Succeeded(),
            "scoped client acknowledgement fits the shared command limit");
        const std::string maximumCommand(CommandProtocol::MaxCommandBytes, 'a');
        Expect(CommandProtocol::ParseFrame(maximumCommand + "\t{}").Succeeded(),
            "maximum bounded command name is accepted");
        Expect(!CommandProtocol::ParseFrame(maximumCommand + "a\t{}").Succeeded(),
            "command name exceeding the shared limit is rejected");
        bool rejectedOversizedCommand = false;
        try { (void)CommandProtocol::EncodeFrame(maximumCommand + "a", nlohmann::json::object()); }
        catch (const std::invalid_argument&) { rejectedOversizedCommand = true; }
        Expect(rejectedOversizedCommand, "response command exceeding the shared limit is rejected");
        const auto payload = CommandProtocol::WithRequestId(
            nlohmann::json{{"status", "accepted"}},
            std::optional<std::string>("req-2"));
        Expect(payload["request_id"] == "req-2", "response request id attached");

        const std::string frame = CommandProtocol::EncodeFrame("join_ack", payload);
        Expect(frame == "join_ack\t{\"request_id\":\"req-2\",\"status\":\"accepted\"}\n",
            "response frame encoded deterministically");

        const auto error = CommandProtocol::MakeError("invalid_request", "bad request");
        Expect(error["code"] == "invalid_request", "error code encoded");

        const std::string statusFrame = CommandProtocol::EncodeFrame(
            "server_status_ack",
            nlohmann::json{{"player_count", 2}, {"state", "RUNNING"}});
        Expect(statusFrame ==
            "server_status_ack\t{\"player_count\":2,\"state\":\"RUNNING\"}\n",
            "server status response is encoded deterministically");
    }

    void WriteWireFixtures(const std::string& path)
    {
        const std::string nonce = "0123456789abcdef0123456789abcdef";
        const std::string binaryHash =
            "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843";
        const nlohmann::json scope{
            {"attempt_id", "fixture_attempt"},
            {"authority_session_id", "fixture_authority"},
            {"world_instance_id", "fixture_world"},
            {"roster_revision", 7},
            {"route_generation", 3}
        };

        std::vector<std::pair<std::string, nlohmann::json>> responses;
        responses.emplace_back(
            "payload_status_ack",
            nlohmann::json{
                {"status", "blocked"},
                {"ready", false},
                {"code", "native_client_grant_injection_unverified"},
                {"protocol_version", "strict-roster-v2"},
                {"native_authority_path_ready", true},
                {"native_client_grant_injection_ready", false},
                {"strict_online_ready", false},
                {"offline_pve", false},
                {"world_instance_id", "fixture_world"},
                {"match", nlohmann::json{{"state", "world_ready"}, {"operation_sequence", 12}}},
                {"request_id", "fixture-status"}
            });
        responses.emplace_back(
            "join_ack",
            nlohmann::json{
                {"request_id", "fixture-join"},
                {"status", "accepted"},
                {"operation_sequence", 12}
            });
        responses.emplace_back(
            "install_match_allocation_ack",
            nlohmann::json{
                {"status", "accepted"},
                {"request_id", "fixture-install"},
                {"payload_version", "strict-roster-v2"},
                {"game_binary_sha256", binaryHash}
            });
        responses.emplace_back(
            "start_match_authority_ack",
            nlohmann::json{
                {"status", "ready"},
                {"request_id", "fixture-authority"},
                {"endpoint_host", "127.0.0.1"},
                {"endpoint_port", 7777},
                {"world_instance_id", "fixture_world"},
                {"native_connection_nonce", nonce},
                {"operation_sequence", 0}
            });
        nlohmann::json clientScope = scope;
        clientScope["player_id"] = "fixture_player";
        clientScope["grant_jti"] = "fixture_grant";
        clientScope["connection_generation"] = 2;
        responses.emplace_back(
            "confirm_client_match_connection_ack",
            nlohmann::json{
                {"accepted", true}, {"code", "accepted"},
                {"status", "travel_requested"},
                {"request_id", "fixture-client-confirm"},
                {"operation_sequence", 12},
                {"scope", clientScope},
                {"native_connection_nonce", nonce},
                {"scope_verified", false},
                {"local_pawn_ready", false}, {"native_net_ready", false},
                {"local_world_instance_id", "client_world_fixture"}
            });
        responses.emplace_back(
            "match_connection_events_ack",
            nlohmann::json{
                {"request_id", "fixture-events"},
                {"next_sequence", 4},
                {"events", nlohmann::json::array({
                    nlohmann::json{
                        {"sequence", 4},
                        {"attempt_id", "fixture_attempt"},
                        {"authority_session_id", "fixture_authority"},
                        {"roster_revision", 7},
                        {"world_instance_id", "fixture_world"},
                        {"route_generation", 3},
                        {"player_id", "fixture_player"},
                        {"grant_jti", "fixture-jti"},
                        {"connection_generation", 2},
                        {"native_connection_nonce", nonce},
                        {"state", "CONNECTED"}
                    }
                })}
            });
        responses.emplace_back(
            "confirm_match_admission_ack",
            nlohmann::json{
                {"accepted", true},
                {"code", "accepted"},
                {"status", "queued"},
                {"request_id", "fixture-reserve"},
                {"native_connection_nonce", nonce}
            });
        responses.emplace_back(
            "confirm_match_connection_ack",
            nlohmann::json{
                {"accepted", true},
                {"code", "accepted"},
                {"status", "accepted"},
                {"request_id", "fixture-confirm"},
                {"native_connection_nonce", nonce}
            });

        nlohmann::json pending = scope;
        pending["status"] = "cleanup_pending";
        pending["code"] = "cleanup_pending";
        pending["message"] = "native teardown is pending";
        pending["native_cleared"] = false;
        pending["world_teardown_required"] = true;
        pending["request_id"] = "fixture-clear-pending";
        responses.emplace_back("clear_match_allocation_pending", std::move(pending));

        nlohmann::json cleared = scope;
        cleared["status"] = "cleared";
        cleared["code"] = "cleared";
        cleared["native_cleared"] = true;
        cleared["world_teardown_required"] = false;
        cleared["request_id"] = "fixture-clear-final";
        responses.emplace_back("clear_match_allocation_ack", std::move(cleared));

        const std::filesystem::path outputPath(path);
        if (outputPath.has_parent_path())
            std::filesystem::create_directories(outputPath.parent_path());
        std::ofstream output(outputPath, std::ios::binary | std::ios::trunc);
        if (!output)
            throw std::runtime_error("unable to open wire fixture output: " + path);

        for (const auto& [command, payload] : responses)
            output << CommandProtocol::EncodeFrame(command, payload);
        output.flush();
        if (!output)
            throw std::runtime_error("unable to write wire fixture output: " + path);
        std::cout << "Wire fixtures written: " << path << " ("
                  << responses.size() << " frames)\n";
    }

    void WriteWireNegativeFixtures(const std::string& path)
    {
        const std::vector<std::pair<std::string, nlohmann::json>> responses{
            {
                "error",
                nlohmann::json{
                    {"code", "busy"},
                    {"message", "join is already in progress"},
                    {"request_id", "fixture-busy"}
                }
            },
            {
                "pong",
                nlohmann::json{{"request_id", "fixture-wrong-command"}}
            }
        };

        const std::filesystem::path outputPath(path);
        if (outputPath.has_parent_path())
            std::filesystem::create_directories(outputPath.parent_path());
        std::ofstream output(outputPath, std::ios::binary | std::ios::trunc);
        if (!output)
            throw std::runtime_error("unable to open negative wire fixture output: " + path);

        for (const auto& [command, payload] : responses)
            output << CommandProtocol::EncodeFrame(command, payload);
        output.flush();
        if (!output)
            throw std::runtime_error("unable to write negative wire fixture output: " + path);
        std::cout << "Negative wire fixtures written: " << path << " ("
                  << responses.size() << " frames)\n";
    }
}

int main(const int argc, char* const argv[])
{
    TestValidFrames();
    TestInvalidFrames();
    TestFrameSizeBoundary();
    TestMatchTargets();
    TestResponses();

    if (argc > 1)
        WriteWireFixtures(argv[1]);
    if (argc > 2)
        WriteWireNegativeFixtures(argv[2]);

    if (failures != 0)
    {
        std::cerr << failures << " test(s) failed\n";
        return 1;
    }

    std::cout << "CommandProtocol tests passed\n";
    return 0;
}
