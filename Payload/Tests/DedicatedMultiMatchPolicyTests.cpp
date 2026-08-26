#include "../ServerLogic/DedicatedMultiMatchPolicy.h"

#include <cstdlib>
#include <iostream>
#include <string>
#include <vector>

namespace
{
    void Expect(const bool condition, const char* message)
    {
        if (!condition)
        {
            std::cerr << "FAILED: " << message << std::endl;
            std::exit(1);
        }
    }

    void TestPlaylistValidation()
    {
        const auto valid = DedicatedMultiMatchPolicy::ValidatePlaylist(
            {"warehouse", "OSS", "DataCenter"}, true);
        Expect(valid.Valid, "known PVE-compatible aliases should validate");
        Expect(valid.Playlist == std::vector<std::string>({"Warehouse", "OSS", "DataCenter"}),
            "playlist should canonicalize aliases");

        Expect(!DedicatedMultiMatchPolicy::ValidatePlaylist(
            {"Warehouse", "WAREHOUSE"}, true).Valid,
            "duplicate aliases should be rejected");

        Expect(!DedicatedMultiMatchPolicy::ValidatePlaylist({"Dusty"}, true).Valid,
            "PVE-incompatible maps should be rejected");
        Expect(!DedicatedMultiMatchPolicy::ValidatePlaylist({"Warehouse?game=Bad"}, false).Valid,
            "URL options must not be accepted as map aliases");
        Expect(!DedicatedMultiMatchPolicy::ValidatePlaylist({}, false).Valid,
            "empty playlists should be rejected");

        Expect(DedicatedMultiMatchPolicy::TravelPackageForMap("warehouse") ==
            "/Game/Maps/EA/Domination/Warehouse/Warehouse",
            "travel must use the observed native package path, not a display alias");
        Expect(DedicatedMultiMatchPolicy::TravelPackageForMap("OSS") ==
            "/Game/Maps/EA/Purge/OSS/OSS",
            "Purge maps should resolve to their mounted package path");
        Expect(DedicatedMultiMatchPolicy::TravelPackageForMap("unknown").empty(),
            "unknown aliases must not become travel URLs");
    }

    void TestOrderedCandidatesAvoidImmediateRepeat()
    {
        const std::vector<std::string> playlist{"Warehouse", "OSS", "DataCenter"};
        const auto candidates = DedicatedMultiMatchPolicy::BuildCandidates(
            playlist, "Warehouse", 3);
        Expect(candidates == std::vector<std::string>({"OSS", "DataCenter"}),
            "multi-map candidates should follow playlist order without the current map");

        const auto wrapped = DedicatedMultiMatchPolicy::BuildCandidates(
            playlist, "DataCenter", 2);
        Expect(wrapped == std::vector<std::string>({"Warehouse", "OSS"}),
            "candidate selection should wrap deterministically");

        const auto restart = DedicatedMultiMatchPolicy::BuildCandidates(
            {"Warehouse"}, "Warehouse", 3);
        Expect(restart == std::vector<std::string>({"Warehouse"}),
            "a one-map playlist should select a native same-map seamless travel");
    }

    void TestWinnerAndVoteCommandAreDeterministic()
    {
        const std::vector<std::string> candidates{"OSS", "DataCenter", "Warehouse"};
        Expect(DedicatedMultiMatchPolicy::ResolveWinner(candidates, {0, 0, 0}) == 0,
            "zero votes should select the first candidate");
        Expect(DedicatedMultiMatchPolicy::ResolveWinner(candidates, {2, 2, 1}) == 0,
            "ties should select the first candidate in playlist order");
        Expect(DedicatedMultiMatchPolicy::ResolveWinner(candidates, {1, 3, 2}) == 1,
            "the highest count should win");

        Expect(DedicatedMultiMatchPolicy::ParseVoteCommand(" /VoTe 2 ", 3) == 1,
            "vote command should be case-insensitive and one-based");
        Expect(!DedicatedMultiMatchPolicy::ParseVoteCommand("/vote 0", 3),
            "vote zero should be rejected");
        Expect(!DedicatedMultiMatchPolicy::ParseVoteCommand("/vote 4", 3),
            "out-of-range votes should be rejected");
        Expect(!DedicatedMultiMatchPolicy::ParseVoteCommand("hello", 3),
            "normal chat should not be consumed");
    }

    void TestDestinationWorldMatching()
    {
        Expect(DedicatedMultiMatchPolicy::WorldNameMatchesMap(
            "Warehouse", "warehouse"),
            "an exact destination world should match case-insensitively");
        Expect(DedicatedMultiMatchPolicy::WorldNameMatchesMap(
            "UEDPIE_0_Warehouse", "Warehouse"),
            "a prefixed Unreal world name should match at an underscore boundary");
        Expect(!DedicatedMultiMatchPolicy::WorldNameMatchesMap(
            "Transition", "Warehouse"),
            "a seamless transition world must not be committed as the destination");
        Expect(!DedicatedMultiMatchPolicy::WorldNameMatchesMap(
            "NotWarehouse", "Warehouse"),
            "an arbitrary suffix without a boundary must not match");
    }

    void TestOwnedTravelUrlMarker()
    {
        Expect(DedicatedMultiMatchPolicy::IsOwnedTravelUrl(
            "/Game/Maps/EA/Purge/OSS/OSS?game=/Game/GameMode/PBGameMode.PBGameMode_C"
            "?ProjectReboundMultiMatch=1"),
            "a marked known-map playlist URL should belong to multi-match travel");
        Expect(!DedicatedMultiMatchPolicy::IsOwnedTravelUrl(
            "/Game/Maps/EA/Purge/OSS/OSS?game=/Game/GameMode/PBGameMode.PBGameMode_C"),
            "native unmarked travel must remain untouched");
        Expect(!DedicatedMultiMatchPolicy::IsOwnedTravelUrl(
            "/Game/Maps/Unknown/Unknown?ProjectReboundMultiMatch=1"),
            "a marker must not claim an unknown travel package");
    }

    void TestWaitingToEndTravelBoundary()
    {
        using DedicatedMultiMatchPolicy::ShouldBeginTravelAfterWaitingToEnd;

        Expect(!ShouldBeginTravelAfterWaitingToEnd(false, true, 1),
            "travel must require the authoritative WaitingToEnd callback");
        Expect(!ShouldBeginTravelAfterWaitingToEnd(true, false, 1),
            "travel must remain behind the playlist/vote lifecycle gate");
        Expect(!ShouldBeginTravelAfterWaitingToEnd(true, true, 0),
            "travel must preserve the first post-WaitingToEnd engine tick");
        Expect(ShouldBeginTravelAfterWaitingToEnd(true, true, 1),
            "travel may begin after one completed post-WaitingToEnd tick");
    }

    void TestPinnedInvalidTravelDelegateGuard()
    {
        using DedicatedMultiMatchPolicy::ShouldArmClientTravelDelegateGuard;
        using DedicatedMultiMatchPolicy::ShouldSuppressInvalidTravelDelegate;
        using DedicatedMultiMatchPolicy::
            ShouldUseUnavailableGameInstanceTravelFallback;
        using DedicatedMultiMatchPolicy::ShouldUseInvalidPlayerStateTravelFallback;

        Expect(ShouldArmClientTravelDelegateGuard(true, true),
            "a marked seamless ClientTravel must arm the client guard");
        Expect(!ShouldArmClientTravelDelegateGuard(true, false),
            "non-seamless marked travel must not arm the client guard");
        Expect(!ShouldArmClientTravelDelegateGuard(false, true),
            "unmarked native ClientTravel must not arm the client guard");

        Expect(ShouldSuppressInvalidTravelDelegate(0x2C0, true),
            "the pinned null-owner +0x2C0 RoundState delegate must be suppressed during owned travel");
        Expect(ShouldSuppressInvalidTravelDelegate(0x300, true),
            "the pinned null-owner +0x300 delegate must be suppressed during owned travel");
        Expect(ShouldSuppressInvalidTravelDelegate(0x310, true),
            "the pinned null-owner +0x310 delegate must be suppressed during owned travel");
        Expect(ShouldSuppressInvalidTravelDelegate(0x3B0, true),
            "the pinned null-owner +0x3B0 spawn delegate must be suppressed during owned travel");
        Expect(ShouldSuppressInvalidTravelDelegate(0x3C0, true),
            "the pinned null-owner +0x3C0 post-confirm delegate must be suppressed during owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x2C0, false),
            "the RoundState delegate address must remain native outside owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x300, false),
            "the pinned delegate address must remain native outside owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x310, false),
            "the second pinned delegate address must remain native outside owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x3B0, false),
            "the spawn delegate address must remain native outside owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x3C0, false),
            "the post-confirm delegate address must remain native outside owned travel");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x2C8, true),
            "the guard must match the RoundState delegate this, not its resulting read address");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x308, true),
            "the guard must not widen to neighboring low addresses");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x318, true),
            "the guard must match delegate this, not the resulting read address");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x320, true),
            "the guard must not widen between the proven members");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x3A0, true),
            "the guard must not widen below the proven spawn member");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x3B8, true),
            "the guard must match spawn delegate this, not its resulting read address");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x380, true),
            "the guard must not claim an adjacent unproven payload delegate");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x3D0, true),
            "the guard must not widen above the proven post-confirm member");
        Expect(!ShouldSuppressInvalidTravelDelegate(0x10000, true),
            "valid-looking delegate storage must remain native");

        Expect(ShouldUseInvalidPlayerStateTravelFallback(true, false),
            "owned seamless travel must tolerate a null or wrong-class PlayerState phase");
        Expect(!ShouldUseInvalidPlayerStateTravelFallback(false, false),
            "ordinary and P2P invalid PlayerState notifications must remain native");
        Expect(!ShouldUseInvalidPlayerStateTravelFallback(true, true),
            "a valid destination PlayerState must run the complete PB override");

        Expect(ShouldUseUnavailableGameInstanceTravelFallback(true, false),
            "owned seamless travel must tolerate the proven null PBGameInstance phase");
        Expect(!ShouldUseUnavailableGameInstanceTravelFallback(false, false),
            "ordinary/P2P PBGameInstance queries must remain native");
        Expect(!ShouldUseUnavailableGameInstanceTravelFallback(true, true),
            "an available destination PBGameInstance must run the native query");
    }

    void TestRetiredGameModeEndMatchGate()
    {
        using DedicatedMultiMatchPolicy::ShouldSuppressRetiredGameModeEndMatch;

        Expect(ShouldSuppressRetiredGameModeEndMatch(
            true, false, true, true, false, true),
            "the exact travel source must not run a duplicate EndMatch");
        Expect(ShouldSuppressRetiredGameModeEndMatch(
            true, false, false, false, true, false),
            "a committed retired GameMode must not freeze an empty result again");
        Expect(!ShouldSuppressRetiredGameModeEndMatch(
            true, false, false, false, true, true),
            "the current authority must retain its first native EndMatch");
        Expect(!ShouldSuppressRetiredGameModeEndMatch(
            false, false, true, true, true, false),
            "ordinary and P2P lifecycle must remain native");
        Expect(!ShouldSuppressRetiredGameModeEndMatch(
            true, true, true, true, true, false),
            "fallback must restore the native process lifecycle");
        Expect(!ShouldSuppressRetiredGameModeEndMatch(
            true, false, true, false, false, false),
            "an unrelated GameMode must never be claimed by the gate");

        using DedicatedMultiMatchPolicy::ShouldBypassNullResultMvp;
        Expect(ShouldBypassNullResultMvp(true, false, true, false),
            "the current owned result path must continue past a null MVP");
        Expect(!ShouldBypassNullResultMvp(true, false, true, true),
            "an available MVP must retain the complete native decoration path");
        Expect(!ShouldBypassNullResultMvp(true, false, false, false),
            "an unrelated GameMode must not claim the null-MVP continuation");
        Expect(!ShouldBypassNullResultMvp(false, false, true, false),
            "ordinary/P2P result handling must remain native");
        Expect(!ShouldBypassNullResultMvp(true, true, true, false),
            "fallback must restore native result handling");
    }

    void TestDestinationNetDriverRepairGate()
    {
        using DedicatedMultiMatchPolicy::ShouldRepairDestinationNetDriverBinding;

        Expect(ShouldRepairDestinationNetDriverBinding(
            true, true, true, true, true, true),
            "an owned destination with the proven detached driver should be repaired");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            false, true, true, true, true, true),
            "unowned/P2P travel must never repair a driver binding");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            true, false, true, true, true, true),
            "repair must wait until seamless travel was dispatched");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            true, true, false, true, true, true),
            "repair must require the pinned destination world");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            true, true, true, false, true, true),
            "repair must not replace a destination world's driver");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            true, true, true, true, false, true),
            "repair must not steal a driver from another world");
        Expect(!ShouldRepairDestinationNetDriverBinding(
            true, true, true, true, true, false),
            "repair must preserve the exact source connection set");
    }

    void TestSeamlessGameModePlayerCountRepairGate()
    {
        using DedicatedMultiMatchPolicy::
            ShouldRepairSeamlessGameModePlayerCounts;

        Expect(ShouldRepairSeamlessGameModePlayerCounts(true, 1, 0, 0),
            "owned rebound must repair an impossible zero native player total");
        Expect(!ShouldRepairSeamlessGameModePlayerCounts(false, 1, 0, 0),
            "ordinary/P2P GameMode counters must remain native");
        Expect(!ShouldRepairSeamlessGameModePlayerCounts(true, 0, 0, 0),
            "a destination without preserved humans must remain native");
        Expect(!ShouldRepairSeamlessGameModePlayerCounts(true, 1, 1, 0),
            "an already valid native player count must not be overwritten");
        Expect(!ShouldRepairSeamlessGameModePlayerCounts(true, 1, 0, 1),
            "a valid travelling-player count must not be overwritten");
    }

    void TestSeamlessReboundPlayerGate()
    {
        using DedicatedMultiMatchPolicy::ShouldPreserveSeamlessReboundPlayer;

        Expect(ShouldPreserveSeamlessReboundPlayer(true, true, true, true, false),
            "a live playable controller on the proven source connection should be preserved");
        Expect(!ShouldPreserveSeamlessReboundPlayer(true, true, true, false, true),
            "a rebound without a Pawn must re-enter destination role selection");
        Expect(!ShouldPreserveSeamlessReboundPlayer(false, true, true, true, true),
            "unowned/P2P travel must retain the native join lifecycle");
        Expect(!ShouldPreserveSeamlessReboundPlayer(true, false, true, true, true),
            "a new or replaced connection must use the normal join lifecycle");
        Expect(!ShouldPreserveSeamlessReboundPlayer(true, true, false, true, true),
            "a destroyed controller must not be registered in the destination generation");
        Expect(!ShouldPreserveSeamlessReboundPlayer(true, true, true, false, false),
            "a controller with neither Pawn nor authoritative role must fall back to role selection");
    }

    void TestSeamlessDuplicateRoleConfirmationGate()
    {
        using DedicatedMultiMatchPolicy::
            ShouldSuppressSeamlessDuplicateRoleConfirmation;

        Expect(!ShouldSuppressSeamlessDuplicateRoleConfirmation(
            true, true, false),
            "an exact source-role confirmation must rebuild destination native state before spawn");
        Expect(!ShouldSuppressSeamlessDuplicateRoleConfirmation(
            false, true, true),
            "ordinary/P2P role confirmations must remain native");
        Expect(!ShouldSuppressSeamlessDuplicateRoleConfirmation(
            true, false, true),
            "an empty role must not be claimed as a duplicate");
        Expect(!ShouldSuppressSeamlessDuplicateRoleConfirmation(
            true, true, false),
            "a post-death deliberate role change must remain native");
        Expect(ShouldSuppressSeamlessDuplicateRoleConfirmation(
            true, true, true),
            "a destination auto-role submission must not replace an already-playable Pawn");
    }

    void TestDedicatedHostClassification()
    {
        using DedicatedMultiMatchPolicy::IsDedicatedMatchHost;

        Expect(IsDedicatedMatchHost(1, false, true, false, false, false),
            "native dedicated mode should be accepted with authority");
        Expect(!IsDedicatedMatchHost(1, true, false, true, true, false),
            "a world without the matching authority game mode must be rejected");
        Expect(IsDedicatedMatchHost(3, true, true, true, true, false),
            "the pinned custom dedicated client-shaped world should be accepted");
        Expect(IsDedicatedMatchHost(0, true, true, true, true, false),
            "the pinned custom dedicated standalone-shaped world should be accepted");
        Expect(!IsDedicatedMatchHost(3, false, true, true, true, false),
            "a client-shaped world without the exact server switch must be rejected");
        Expect(!IsDedicatedMatchHost(3, true, true, true, true, true),
            "an outbound client NetDriver must be rejected");
        Expect(!IsDedicatedMatchHost(3, true, true, true, false, false),
            "a NetDriver owned by another world must be rejected");
        Expect(!IsDedicatedMatchHost(2, true, true, true, true, false),
            "listen/P2P hosts must remain on the native process-per-match path");
    }
}

int main()
{
    TestPlaylistValidation();
    TestOrderedCandidatesAvoidImmediateRepeat();
    TestWinnerAndVoteCommandAreDeterministic();
    TestDestinationWorldMatching();
    TestOwnedTravelUrlMarker();
    TestWaitingToEndTravelBoundary();
    TestPinnedInvalidTravelDelegateGuard();
    TestRetiredGameModeEndMatchGate();
    TestDestinationNetDriverRepairGate();
    TestSeamlessGameModePlayerCountRepairGate();
    TestSeamlessReboundPlayerGate();
    TestSeamlessDuplicateRoleConfirmationGate();
    TestDedicatedHostClassification();
    std::cout << "DedicatedMultiMatchPolicyTests passed" << std::endl;
    return 0;
}
