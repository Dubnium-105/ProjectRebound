#pragma once

namespace ListenResultPolicy
{
    // The native InitListen result is the ownership boundary for the
    // NetDriver. Binding the world is valid only after the native socket has
    // actually initialized, so a failed initialization cannot publish a
    // listening driver through the follow-up callback.
    template <typename Initialize, typename BindWorld>
    [[nodiscard]] bool InitializeAndBind(
        Initialize&& initialize,
        BindWorld&& bindWorld)
    {
        if (!initialize())
            return false;
        bindWorld();
        return true;
    }
}
