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

    std::cout << "listen result policy tests passed\n";
    return 0;
}
