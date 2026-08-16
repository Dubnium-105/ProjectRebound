#include "../Loadout/LoadoutStatePolicy.h"

#include <chrono>
#include <cstdlib>
#include <iostream>
#include <optional>
#include <string>

namespace
{
    using namespace std::chrono_literals;
    using LoadoutStatePolicy::Clock;
    using LoadoutStatePolicy::PendingRoleConfirmation;

    void Expect(bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << '\n';
            std::exit(1);
        }
    }

    void TestSnapshotReadyBeforeConfirmation()
    {
        PendingRoleConfirmation pending;
        const auto decision = LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", true, false, Clock::time_point{});
        Expect(decision == LoadoutRoleConfirmDecision::Ready,
            "ready baseline should not defer confirmation");
        Expect(!pending.Active, "ready baseline should not create pending state");
    }

    void TestSnapshotArrivesWithinGrace()
    {
        PendingRoleConfirmation pending;
        const auto start = Clock::time_point{};
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", false, false, start) ==
            LoadoutRoleConfirmDecision::Deferred,
            "in-flight fetch should defer");
        const auto originalDeadline = pending.Deadline;
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", false, false, start + 300ms) ==
            LoadoutRoleConfirmDecision::Deferred,
            "duplicate while pending should stay deferred");
        Expect(pending.Deadline == originalDeadline,
            "duplicate confirmation must not extend grace");
        Expect(!LoadoutStatePolicy::PollRoleConfirmation(
            pending, false, false, start + 500ms),
            "unsettled fetch inside grace should not replay");
        const auto decision = LoadoutStatePolicy::PollRoleConfirmation(
            pending, true, false, start + 700ms);
        Expect(decision == LoadoutRoleConfirmDecision::Ready,
            "baseline arriving inside grace should replay ready");
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", true, false, start + 700ms) ==
            LoadoutRoleConfirmDecision::Ready,
            "re-entry should use the selected replay decision");
    }

    void TestTimeoutLateSnapshotAndSameRoleRespawn()
    {
        PendingRoleConfirmation pending;
        const auto start = Clock::time_point{};
        (void)LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", false, false, start);
        const auto timeout = LoadoutStatePolicy::PollRoleConfirmation(
            pending, false, false, start + 1000ms);
        Expect(timeout == LoadoutRoleConfirmDecision::Fallback,
            "confirmation should fall back at one-second deadline");
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", false, false, start + 1000ms) ==
            LoadoutRoleConfirmDecision::Fallback,
            "timeout replay must not defer recursively");

        LoadoutStatePolicy::CompleteRoleConfirmation(pending);
        Expect(!pending.Active && !pending.Replaying,
            "commit should finish the replay transaction");
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            pending, "PEACE", true, true, start + 2000ms) ==
            LoadoutRoleConfirmDecision::Ready,
            "a later same-role respawn must not be connection-lifetime deduplicated");
    }

    void TestPermanentFailureAndRoleSwitch()
    {
        PendingRoleConfirmation failed;
        Expect(LoadoutStatePolicy::BeginRoleConfirmation(
            failed, "PEACE", false, true, Clock::time_point{}) ==
            LoadoutRoleConfirmDecision::Fallback,
            "terminal fetch failure should fall back immediately");

        PendingRoleConfirmation switched;
        const auto start = Clock::time_point{};
        (void)LoadoutStatePolicy::BeginRoleConfirmation(
            switched, "PEACE", false, false, start);
        const auto firstDeadline = switched.Deadline;
        (void)LoadoutStatePolicy::BeginRoleConfirmation(
            switched, "WARDEN", false, false, start + 250ms);
        Expect(switched.RoleId == "WARDEN" && switched.Deadline > firstDeadline,
            "a genuine role switch should start its own bounded grace");
    }

    void TestPriorityRetryAndStaleConnectionPolicy()
    {
        using Source = LoadoutStatePolicy::EffectiveSource;
        Expect(LoadoutStatePolicy::ChooseEffectiveSource(true, true) ==
            Source::RuntimeOverride, "runtime override must outrank baseline");
        Expect(LoadoutStatePolicy::ChooseEffectiveSource(false, true) ==
            Source::MetaserverBaseline, "baseline must outrank native default");
        Expect(LoadoutStatePolicy::ChooseEffectiveSource(false, false) ==
            Source::NativeDefault, "native default must remain final fallback");

        Expect(LoadoutStatePolicy::RetryDelay(1) == 500ms, "retry 1");
        Expect(LoadoutStatePolicy::RetryDelay(2) == 2s, "retry 2");
        Expect(LoadoutStatePolicy::RetryDelay(3) == 10s, "retry 3");
        Expect(LoadoutStatePolicy::RetryDelay(4) == 30s, "steady retry");

        using Identity = LoadoutStatePolicy::ConnectionIdentity;
        const std::optional<Identity> active = Identity{"p_player", 2, 7};
        Expect(LoadoutStatePolicy::IsResponseCurrent(active, Identity{"p_player", 2, 7}),
            "matching response should be consumed");
        Expect(!LoadoutStatePolicy::IsResponseCurrent(active, Identity{"p_player", 1, 7}),
            "previous connection generation response must be dropped");
        Expect(!LoadoutStatePolicy::IsResponseCurrent(active, Identity{"p_player", 2, 6}),
            "previous world response must be dropped");
        Expect(!LoadoutStatePolicy::IsResponseCurrent(std::nullopt, Identity{"p_player", 2, 7}),
            "disconnected player response must be dropped");
    }

    void TestInventoryComparisonIgnoresContainerOrderOnly()
    {
        using Entry = LoadoutStatePolicy::InventoryEntry;
        const std::vector<Entry> expected = {
            {1, "PEACE_GSW-AR"},
            {2, "PEACE_GSW-DMR"},
            {3, "PEACE_ATK-HE"},
            {4, "None"},
            {5, "MELEE-KNIFE"},
            {6, "PEACE_FCM-BRAKE"},
        };
        const std::vector<Entry> reordered = {
            {6, "PEACE_FCM-BRAKE"},
            {3, "PEACE_ATK-HE"},
            {1, "PEACE_GSW-AR"},
            {5, "MELEE-KNIFE"},
            {2, "PEACE_GSW-DMR"},
            {4, "None"},
        };
        Expect(LoadoutStatePolicy::SameInventoryEntries(expected, reordered),
            "TMap iteration order must not change inventory equality");

        auto wrongItem = reordered;
        wrongItem[0].ItemId = "PEACE_FCM-GRAPPLE";
        Expect(!LoadoutStatePolicy::SameInventoryEntries(expected, wrongItem),
            "a different item in the same slot must remain a mismatch");

        auto duplicateSlot = reordered;
        duplicateSlot[0].Slot = 5;
        duplicateSlot[0].ItemId = "MELEE-KNIFE";
        Expect(!LoadoutStatePolicy::SameInventoryEntries(expected, duplicateSlot),
            "duplicate slot entries must not hide a missing slot");
    }
}

int main()
{
    TestSnapshotReadyBeforeConfirmation();
    TestSnapshotArrivesWithinGrace();
    TestTimeoutLateSnapshotAndSameRoleRespawn();
    TestPermanentFailureAndRoleSwitch();
    TestPriorityRetryAndStaleConnectionPolicy();
    TestInventoryComparisonIgnoresContainerOrderOnly();
    std::cout << "loadout state policy tests passed\n";
    return 0;
}
