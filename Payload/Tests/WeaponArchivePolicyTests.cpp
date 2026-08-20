#include "../Loadout/WeaponArchivePolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(bool actual, bool expected, const char* message)
    {
        if (actual != expected)
        {
            std::cerr << "FAILED: " << message << "\n";
            std::exit(1);
        }
    }
}

int main()
{
    using WeaponArchivePolicy::IsNativeOriginalPartAppearanceSentinel;

    Expect(IsNativeOriginalPartAppearanceSentinel("PartOri", ""), true,
        "native PartOri reset sentinel");
    Expect(IsNativeOriginalPartAppearanceSentinel("", "PTOriginal"), true,
        "native receiver painting reset sentinel");
    Expect(IsNativeOriginalPartAppearanceSentinel("", ""), false,
        "omitted appearance inherits the suite");
    Expect(IsNativeOriginalPartAppearanceSentinel("PartOri", "PTOriginal"), false,
        "complete original pair is dispatched normally");
    Expect(IsNativeOriginalPartAppearanceSentinel("PartOri", "OtherPainting"), false,
        "arbitrary painting is not a reset sentinel");
    Expect(IsNativeOriginalPartAppearanceSentinel("OtherSkin", ""), false,
        "arbitrary skin half-pair remains invalid");

    std::cout << "weapon archive policy tests passed\n";
    return 0;
}
