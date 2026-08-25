#pragma once

#include <cstdint>

namespace ReplicationWorldGatePolicy
{
    inline bool HasClientLoadedCurrentWorld(
        std::uint16_t seamlessTravelCount,
        std::uint16_t lastCompletedSeamlessTravelCount)
    {
        // A zero count is the ordinary direct-connect world. During seamless
        // travel the server increments SeamlessTravelCount first and updates
        // LastCompletedSeamlessTravelCount only after the remote
        // ServerNotifyLoadedWorld RPC. Destination actor channels must not be
        // created in that interval or the client constructs their actors in
        // the retiring source/transitional world.
        return seamlessTravelCount == 0 ||
            seamlessTravelCount == lastCompletedSeamlessTravelCount;
    }

    inline bool IsCachedActorChannelUsable(
        const bool isStillOpenOnConnection,
        const bool actorStillMatches)
    {
        // The payload cache is only an accelerator. Native seamless travel is
        // free to close a channel and clear UActorChannel::Actor while the
        // pointer value remains in our cache. Both pieces of native ownership
        // must still agree before the channel enters ReplicateActor.
        return isStillOpenOnConnection && actorStillMatches;
    }
}
