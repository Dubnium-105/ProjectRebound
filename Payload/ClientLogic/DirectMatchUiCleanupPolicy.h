#pragma once

#include <cstddef>
#include <string_view>

namespace DirectMatchUiCleanupPolicy
{
    inline constexpr std::size_t MaxFrontendWidgets = 8;

    inline bool IsDirectMatchFrontendWidget(std::string_view fullName)
    {
        // These are the three persistent frontend layers observed on the
        // fixed Boundary build before a successful main-menu login. Do not
        // broaden this list to prompts or in-game menus: they live in sibling
        // CommonUI stacks owned by the same LocalPlayer subsystem.
        return fullName.find("UMG_MainMenuBase_C") != std::string_view::npos ||
            fullName.find("UMG_LoginGate_C") != std::string_view::npos ||
            fullName.find("UMG_Login_C") != std::string_view::npos ||
            fullName.find("UMG_EnterGame_C") != std::string_view::npos;
    }
}
