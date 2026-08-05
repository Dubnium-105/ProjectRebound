#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <iostream>

#include "../SDKHelper/UnrealContainers.hpp"

namespace
{
    int failures = 0;

    void* TestReallocate(void* block, UC::uint64 newSize, UC::uint32 alignment)
    {
        (void)alignment;
        if (newSize == 0)
        {
            std::free(block);
            return nullptr;
        }
        return std::realloc(block, static_cast<std::size_t>(newSize));
    }

    void Expect(bool condition, const char* description)
    {
        if (condition) return;
        ++failures;
        std::cerr << "FAILED: " << description << '\n';
    }

    void TestCopyReusesCapacityWithoutTruncatingElements()
    {
        UC::TArray<std::uint64_t> destination;
        destination.Add(0x1111111111111111ULL);
        destination.Add(0x2222222222222222ULL);
        destination.Add(0x3333333333333333ULL);

        UC::TArray<std::uint64_t> source;
        source.Add(0xA1A2A3A4A5A6A7A8ULL);
        source.Add(0xB1B2B3B4B5B6B7B8ULL);

        destination = source; // Exercises MaxElements >= Other.NumElements.
        Expect(destination.Num() == 2, "copy assignment updates the element count");
        Expect(destination[0] == source[0], "copy assignment preserves every byte of element zero");
        Expect(destination[1] == source[1], "copy assignment preserves every byte of element one");
    }

    void TestCopyFromEmptyClearsLogicalContents()
    {
        UC::TArray<std::uint64_t> destination;
        destination.Add(42);
        UC::TArray<std::uint64_t> empty;

        destination = empty;
        Expect(destination.Num() == 0, "assigning an empty array clears the destination");
    }
}

int main()
{
    UC::FMemory::EngineRealloc = &TestReallocate;
    TestCopyReusesCapacityWithoutTruncatingElements();
    TestCopyFromEmptyClearsLogicalContents();
    if (failures == 0)
        std::cout << "All Unreal container tests passed.\n";
    return failures == 0 ? 0 : 1;
}
