#include "../Communication/CommandProtocol.h"

#include <iostream>
#include <string>

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

    void TestMatchTargets()
    {
        Expect(CommandProtocol::ValidateMatchTarget("127.0.0.1:7777"), "IPv4 target accepted");
        Expect(CommandProtocol::ValidateMatchTarget("game.example.test:443"), "hostname target accepted");
        Expect(CommandProtocol::ValidateMatchTarget("[2001:db8::1]:7777"), "bracketed IPv6 target accepted");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1"), "missing port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:0"), "zero port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:65536"), "oversized port rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("127.0.0.1:7777;quit"), "console injection rejected");
        Expect(!CommandProtocol::ValidateMatchTarget("2001:db8::1:7777"), "unbracketed IPv6 rejected");
    }

    void TestResponses()
    {
        const auto payload = CommandProtocol::WithRequestId(
            nlohmann::json{{"status", "accepted"}},
            std::optional<std::string>("req-2"));
        Expect(payload["request_id"] == "req-2", "response request id attached");

        const std::string frame = CommandProtocol::EncodeFrame("join_ack", payload);
        Expect(frame == "join_ack\t{\"request_id\":\"req-2\",\"status\":\"accepted\"}\n",
            "response frame encoded deterministically");

        const auto error = CommandProtocol::MakeError("invalid_request", "bad request");
        Expect(error["code"] == "invalid_request", "error code encoded");
    }
}

int main()
{
    TestValidFrames();
    TestInvalidFrames();
    TestMatchTargets();
    TestResponses();

    if (failures != 0)
    {
        std::cerr << failures << " test(s) failed\n";
        return 1;
    }

    std::cout << "CommandProtocol tests passed\n";
    return 0;
}
