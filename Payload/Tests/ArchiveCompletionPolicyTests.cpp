#include "../Hooks/ArchiveCompletionPolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void ExpectEqual(int actual, int expected, const char* message)
    {
        if (actual != expected)
        {
            std::cerr << "FAILED: " << message << " (actual=" << actual
                      << ", expected=" << expected << ")\n";
            std::exit(1);
        }
    }
}

int main()
{
    using ArchiveCompletionPolicy::NormalizePersistedCompletion;

    ExpectEqual(NormalizePersistedCompletion(404), 0,
        "stale native pending sentinel should become success");
    ExpectEqual(NormalizePersistedCompletion(0), 0,
        "native success must remain success");
    ExpectEqual(NormalizePersistedCompletion(1), 1,
        "ordinary failure must remain unchanged");
    ExpectEqual(NormalizePersistedCompletion(200), 200,
        "native completion 200 must remain unchanged");
    ExpectEqual(NormalizePersistedCompletion(9001), 9001,
        "known game error must remain unchanged");
    ExpectEqual(NormalizePersistedCompletion(503), 503,
        "arbitrary transport error must remain unchanged");
    ExpectEqual(NormalizePersistedCompletion(-1), -1,
        "negative internal error must remain unchanged");

    std::cout << "archive completion policy tests passed\n";
    return 0;
}
