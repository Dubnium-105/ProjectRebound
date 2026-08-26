#include "../ClientLogic/LocalQosDiscoveryPolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    using namespace LocalQosDiscoveryPolicy;
    Expect(Evaluate("game.exe").state == State::Disabled,
        "ordinary launches must leave the QoS initializer untouched");

    const auto enabled = Evaluate(
        R"(game.exe -LocalPveQosDiscoveryUrl=http://127.0.0.1:32123/servers -LocalPveQosReadyEvent=Local\ProjectRebound.Qos.0123456789abcdef0123456789abcdef)");
    Expect(enabled.state == State::Enabled,
        "the Toolbox loopback contract should be accepted");
    Expect(enabled.discoveryUrl == "http://127.0.0.1:32123/servers",
        "the validated URL must be preserved exactly");

    Expect(Evaluate(
        R"(game.exe -LocalPveQosDiscoveryUrl=http://127.0.0.1:32123/servers)").state ==
        State::Invalid,
        "partial opt-in must fail closed");
    Expect(Evaluate(
        R"(game.exe -LocalPveQosDiscoveryUrl=http://192.168.1.4:32123/servers -LocalPveQosReadyEvent=Local\ProjectRebound.Qos.0123456789abcdef0123456789abcdef)").state ==
        State::Invalid,
        "non-loopback discovery must be rejected");
    Expect(Evaluate(
        R"(game.exe -LocalPveQosDiscoveryUrl=http://127.0.0.1:0/servers -LocalPveQosReadyEvent=Local\ProjectRebound.Qos.0123456789abcdef0123456789abcdef)").state ==
        State::Invalid,
        "port zero must be rejected");
    Expect(Evaluate(
        R"(game.exe -LocalPveQosDiscoveryUrl=http://127.0.0.1:32123/servers -LocalPveQosReadyEvent=Global\ProjectRebound.Qos.0123456789abcdef0123456789abcdef)").state ==
        State::Invalid,
        "the readiness event namespace must be fixed");

    std::cout << "local QoS discovery policy tests passed\n";
    return 0;
}
