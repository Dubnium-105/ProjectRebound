#pragma once

namespace ListenResultPolicy
{
    // Publishing the listening marker requires both the native InitListen
    // result and the structural facts consumed by the authority observer.
    // Keep this predicate pure so a partial NetDriver setup cannot become a
    // reported authority through a stale or speculative marker.
    inline constexpr bool ShouldPublishListening(
        const bool nativeListenSucceeded,
        const bool hasAuthorityGameMode,
        const bool hasNetDriver,
        const bool worldMatches,
        const bool serverConnectionAbsent) noexcept
    {
        return nativeListenSucceeded && hasAuthorityGameMode &&
            hasNetDriver && worldMatches && serverConnectionAbsent;
    }

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
