#include "../Replication/ReplicationWorldGatePolicy.h"

#include <cstdlib>
#include <iostream>

namespace
{
    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }
}

int main()
{
    using ReplicationWorldGatePolicy::HasClientLoadedCurrentWorld;
    using ReplicationWorldGatePolicy::IsCachedActorChannelUsable;

    Expect(HasClientLoadedCurrentWorld(0, 0),
        "direct-connect replication must remain available");
    Expect(!HasClientLoadedCurrentWorld(1, 0),
        "generation two actors must wait for ServerNotifyLoadedWorld");
    Expect(HasClientLoadedCurrentWorld(1, 1),
        "generation two actors may replicate after client world load");
    Expect(!HasClientLoadedCurrentWorld(3, 2),
        "every later seamless generation must re-arm the gate");
    Expect(HasClientLoadedCurrentWorld(3, 3),
        "later generations release only at their matching acknowledgement");

    Expect(IsCachedActorChannelUsable(true, true),
        "an open channel whose actor still matches may replicate");
    Expect(!IsCachedActorChannelUsable(false, true),
        "a channel removed from OpenChannels must be evicted");
    Expect(!IsCachedActorChannelUsable(true, false),
        "a recycled or cleared actor channel must be evicted");
    Expect(!IsCachedActorChannelUsable(false, false),
        "a fully retired channel must never reach ReplicateActor");

    std::cout << "replication world gate policy tests passed\n";
    return 0;
}
