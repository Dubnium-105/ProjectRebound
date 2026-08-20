#include "../ClientLogic/DirectMatchUiCleanupPolicy.h"

#include <cstdlib>
#include <iostream>
#include <string_view>

namespace
{
    void Expect(bool condition, std::string_view message)
    {
        if (!condition)
        {
            std::cerr << "FAIL: " << message << '\n';
            std::exit(EXIT_FAILURE);
        }
    }
}

int main()
{
    using DirectMatchUiCleanupPolicy::IsDirectMatchFrontendWidget;

    Expect(IsDirectMatchFrontendWidget(
        "UMG_MainMenuBase_C /Engine/Transient.GameEngine_1.UMG_MainMenuBase_C_0"),
        "the main-menu layer must be hidden before direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_LoginGate_C /Engine/Transient.GameEngine_1.UMG_LoginGate_C_0"),
        "the platform-login gate must not survive direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_EnterGame_C /Engine/Transient.GameEngine_1.UMG_EnterGame_C_0"),
        "the press-to-start layer must not survive direct travel");
    Expect(IsDirectMatchFrontendWidget(
        "UMG_Login_C /Engine/Transient.GameEngine_1.UMG_Login_C_0"),
        "the platform-login layer must not survive direct travel");

    Expect(!IsDirectMatchFrontendWidget(
        "UMG_InGameOption_V2_C /Engine/Transient.GameEngine_1.UMG_InGameOption_V2_C_0"),
        "the in-game ESC menu must be preserved");
    Expect(!IsDirectMatchFrontendWidget(
        "ConfirmPage_C /Engine/Transient.GameEngine_1.ConfirmPage_C_0"),
        "prompt widgets must be preserved");
    Expect(!IsDirectMatchFrontendWidget("PBMainMenuManager_BP_C /Engine/Transient"),
        "the manager itself is not a cleanup target");
    Expect(!IsDirectMatchFrontendWidget(""),
        "an unknown widget must stop targeted cleanup");

    std::cout << "Direct-match UI cleanup policy tests passed\n";
    return EXIT_SUCCESS;
}
