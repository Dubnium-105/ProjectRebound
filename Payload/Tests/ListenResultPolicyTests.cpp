#include "../Replication/ListenResultPolicy.h"

#include <cstdlib>
#include <iostream>
#include <vector>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAIL: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    std::vector<int> failedCalls;
    const bool failed = ListenResultPolicy::InitializeAndBind(
        [&failedCalls]() {
            failedCalls.push_back(1);
            return false;
        },
        [&failedCalls]() { failedCalls.push_back(2); });
    Expect(!failed, "a failed native initialization must propagate false");
    Expect(failedCalls == std::vector<int>{1},
        "world binding must not run after native initialization fails");

    std::vector<int> successfulCalls;
    const bool succeeded = ListenResultPolicy::InitializeAndBind(
        [&successfulCalls]() {
            successfulCalls.push_back(1);
            return true;
        },
        [&successfulCalls]() { successfulCalls.push_back(2); });
    Expect(succeeded, "a successful native initialization must propagate true");
    Expect(successfulCalls == std::vector<int>{1, 2},
        "world binding must follow successful native initialization");

    Expect(!ListenResultPolicy::ShouldPublishListening(
        false, true, true, true, true),
        "a failed native listen must never publish the listening marker");
    Expect(!ListenResultPolicy::ShouldPublishListening(
        true, false, true, true, true),
        "authority game mode is required before publishing the listening marker");
    Expect(!ListenResultPolicy::ShouldPublishListening(
        true, true, false, true, true),
        "a NetDriver is required before publishing the listening marker");
    Expect(!ListenResultPolicy::ShouldPublishListening(
        true, true, true, false, true),
        "the observed world must match before publishing the listening marker");
    Expect(!ListenResultPolicy::ShouldPublishListening(
        true, true, true, true, false),
        "a server connection must be absent before publishing the listening marker");
    Expect(ListenResultPolicy::ShouldPublishListening(
        true, true, true, true, true),
        "a successful native listen with all authority facts may publish the marker");

    std::cout << "listen result policy tests passed\n";
    return 0;
}
