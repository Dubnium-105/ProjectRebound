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
    using ArchiveCompletionPolicy::NormalizeEquipmentCompletion;
    using ArchiveCompletionPolicy::NormalizePersistedCompletion;
    using ArchiveCompletionPolicy::NormalizeWeaponCustomizationCompletion;

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
    ExpectEqual(NormalizePersistedCompletion(9002), 9002,
        "equipment-only persisted code must remain unchanged on generic archive paths");
    ExpectEqual(NormalizePersistedCompletion(503), 503,
        "arbitrary transport error must remain unchanged");
    ExpectEqual(NormalizePersistedCompletion(-1), -1,
        "negative internal error must remain unchanged");

    ExpectEqual(NormalizeEquipmentCompletion(404), 0,
        "equipment archive should accept the stale pending sentinel");
    ExpectEqual(NormalizeEquipmentCompletion(9002), 0,
        "equipment archive should accept its observed persisted completion");
    ExpectEqual(NormalizeEquipmentCompletion(0), 0,
        "equipment archive native success must remain success");
    ExpectEqual(NormalizeEquipmentCompletion(200), 200,
        "equipment archive completion 200 must remain unchanged");
    ExpectEqual(NormalizeEquipmentCompletion(9001), 9001,
        "adjacent game error 9001 must remain unchanged");
    ExpectEqual(NormalizeEquipmentCompletion(9003), 9003,
        "adjacent game error 9003 must remain unchanged");
    ExpectEqual(NormalizeEquipmentCompletion(-1), -1,
        "equipment archive negative internal error must remain unchanged");

    ExpectEqual(NormalizeWeaponCustomizationCompletion(404), 0,
        "weapon customization should accept the stale pending sentinel");
    ExpectEqual(NormalizeWeaponCustomizationCompletion(0), 0,
        "weapon customization native success must remain success");
    ExpectEqual(NormalizeWeaponCustomizationCompletion(9002), 9002,
        "weapon customization must not inherit the equipment-only 9002 rule");
    ExpectEqual(NormalizeWeaponCustomizationCompletion(9001), 9001,
        "weapon customization game errors must remain unchanged");
    ExpectEqual(NormalizeWeaponCustomizationCompletion(503), 503,
        "weapon customization transport errors must remain unchanged");
    ExpectEqual(NormalizeWeaponCustomizationCompletion(-1), -1,
        "weapon customization internal errors must remain unchanged");

    std::cout << "archive completion policy tests passed\n";
    return 0;
}
