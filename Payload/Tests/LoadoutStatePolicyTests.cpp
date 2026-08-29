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
        Expect(LoadoutStatePolicy::ShouldRetryBaselineFetch(true, 1),
            "first retryable baseline failure should retry");
        Expect(LoadoutStatePolicy::ShouldRetryBaselineFetch(true, 3),
            "third retryable baseline failure should retry");
        Expect(!LoadoutStatePolicy::ShouldRetryBaselineFetch(true, 4),
            "fourth retryable baseline failure should fall back to native");
        Expect(!LoadoutStatePolicy::ShouldRetryBaselineFetch(false, 1),
            "permanent baseline failure should fall back immediately");

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

    void TestSeamlessRoleSpawnGateIsScopedAndBounded()
    {
        const auto start = Clock::time_point{};
        const auto deadline = start + 1s;
        Expect(LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            false, false, false, start, deadline),
            "ordinary spawns must remain un-gated");
        Expect(!LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, false, false, start + 500ms, deadline),
            "seamless rebound must wait for the client travel handshake");
        Expect(!LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, true, false, start + 500ms, deadline),
            "a settled baseline must not race an incomplete client travel");
        Expect(!LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, false, true, start + 500ms, deadline),
            "seamless rebound must wait for its fresh baseline");
        Expect(LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, true, true, start + 500ms, deadline),
            "settled baseline and client travel must release the rebound spawn");
        Expect(LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, false, true, deadline, deadline),
            "baseline failure may fall back only after client travel completes");
        Expect(!LoadoutStatePolicy::CanReleaseSeamlessRoleSpawn(
            true, false, false, deadline, deadline),
            "the baseline deadline must not bypass client travel completion");
    }

    void TestSeamlessFieldModSeedWaitsForNativeTravelCompletion()
    {
        Expect(!LoadoutStatePolicy::CanAttemptSeamlessFieldModRoleSeed(
            false, true, true, true),
            "ordinary and P2P spawns must not synthesize FieldMod roles");
        Expect(!LoadoutStatePolicy::CanAttemptSeamlessFieldModRoleSeed(
            true, true, false, true),
            "FieldMod roles must not be seeded before ServerNotifyLoadedWorld");
        Expect(!LoadoutStatePolicy::CanAttemptSeamlessFieldModRoleSeed(
            true, false, true, true),
            "an unsettled or terminal fetch cannot publish a baseline");
        Expect(!LoadoutStatePolicy::CanAttemptSeamlessFieldModRoleSeed(
            true, true, true, false),
            "an empty baseline must preserve the native fallback");
        Expect(LoadoutStatePolicy::CanAttemptSeamlessFieldModRoleSeed(
            true, true, true, true),
            "owned seamless travel may seed only after its native handshake");
    }

    void TestFreshSeamlessRoleConfirmationWaitsForFieldModSeed()
    {
        Expect(LoadoutStatePolicy::CanDispatchFreshSeamlessRoleConfirmation(
            false, false),
            "ordinary initial role confirmation must remain native");
        Expect(!LoadoutStatePolicy::CanDispatchFreshSeamlessRoleConfirmation(
            true, false),
            "fresh seamless role confirmation must wait for rebuilt containers");
        Expect(LoadoutStatePolicy::CanDispatchFreshSeamlessRoleConfirmation(
            true, true),
            "fresh seamless role confirmation may dispatch after native seeding");
    }

    void TestSeamlessRoleValidatorBypassIsExact()
    {
        Expect(LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            true, true, true, true, true),
            "the exact guarded seamless recovery may bypass the transient set");
        Expect(!LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            false, true, true, true, true),
            "ordinary RPC validation must remain native");
        Expect(!LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            true, false, true, true, true),
            "a completed or ordinary spawn gate cannot bypass validation");
        Expect(!LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            true, true, false, true, true),
            "a different controller cannot borrow the recovery permit");
        Expect(!LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            true, true, true, false, true),
            "a different role cannot borrow the recovery permit");
        Expect(!LoadoutStatePolicy::CanBypassSeamlessRoleValidator(
            true, true, true, true, false),
            "unverified native pre-order state cannot bypass validation");

        Expect(LoadoutStatePolicy::CanBypassFreshSeamlessRoleValidator(
            true, true, true),
            "fresh selection may bypass only for an already-seeded role");
        Expect(!LoadoutStatePolicy::CanBypassFreshSeamlessRoleValidator(
            false, true, true),
            "ordinary selection must retain the native validator");
        Expect(!LoadoutStatePolicy::CanBypassFreshSeamlessRoleValidator(
            true, false, true),
            "fresh selection cannot bypass before FieldMod seeding");
        Expect(!LoadoutStatePolicy::CanBypassFreshSeamlessRoleValidator(
            true, true, false),
            "a missing requested pre-order role cannot bypass validation");
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
    TestSeamlessRoleSpawnGateIsScopedAndBounded();
    TestSeamlessFieldModSeedWaitsForNativeTravelCompletion();
    TestFreshSeamlessRoleConfirmationWaitsForFieldModSeed();
    TestSeamlessRoleValidatorBypassIsExact();
    std::cout << "loadout state policy tests passed\n";
    return 0;
}
