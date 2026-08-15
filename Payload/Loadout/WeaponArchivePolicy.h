#pragma once

#include <string_view>

namespace WeaponArchivePolicy
{
    inline bool IsNativeOriginalPartAppearanceSentinel(
        std::string_view skinId,
        std::string_view paintingId) noexcept
    {
        return (skinId == "PartOri" && paintingId.empty()) ||
            (skinId.empty() && paintingId == "PTOriginal");
    }
}
