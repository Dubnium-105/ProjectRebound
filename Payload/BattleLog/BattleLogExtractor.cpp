#include "BattleLogExtractor.h"

#include <Windows.h>
#include <bcrypt.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <chrono>
#include <cctype>
#include <cstdint>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <map>
#include <sstream>
#include <string>
#include <unordered_set>
#include <vector>

#include "../SDK.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Libs/json.hpp"
#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../Utility/Utility.h"

#pragma comment(lib, "bcrypt.lib")

namespace BattleLog
{
namespace
{
    using nlohmann::json;
    using namespace SDK;

    thread_local int gCaptureDepth = 0;
    UWorld* gObservedWorld = nullptr;
    std::unordered_set<std::string> gCapturedStages;
    std::atomic_uint64_t gFileSequence = 0;

    struct P2PContext final
    {
        bool Enabled = false;
        std::string MatchId;
        std::string CapabilityId;
        std::string ServerNonce;
        std::string ClientVersion = "payload-v3";
        std::string AuthorityKind = "CLIENT_OBSERVER";
    };

    P2PContext gP2PContext;
    bool gP2PTimelineActive = false;
    bool gP2PTimelineFinalized = false;
    bool gP2PTimelineTruncated = false;
    bool gP2PStartSnapshotWritten = false;
    uint64_t gP2PTimelineSequence = 0;
    std::string gP2PTimelineSessionId;
    std::string gP2PTimelineDigest;
    json gP2PTimelineEvents = json::array();
    std::chrono::steady_clock::time_point gP2PTimelineStartedAt;

    constexpr size_t kMaximumP2PTimelineEvents = 4096;
    constexpr int32 kMaximumSnapshotStringCodeUnits = 1 << 20;

    struct MatchClassification final
    {
        std::string Type = "unknown";
        std::string Source = "unavailable";
        std::string Confidence = "unknown";
        std::vector<std::string> Evidence;
    };

    struct ParticipantAggregate final
    {
        int32 Count = 0;
        int32 Kills = 0;
        int32 Deaths = 0;
        int32 Assists = 0;
        double Score = 0.0;
    };

    struct TeamAggregate final
    {
        ParticipantAggregate All;
        ParticipantAggregate Humans;
        ParticipantAggregate AI;
    };

    class CaptureScope final
    {
    public:
        CaptureScope() { ++gCaptureDepth; }
        ~CaptureScope() { --gCaptureDepth; }
    };

    bool EndsWith(const std::string& value, const std::string& suffix)
    {
        return value.size() >= suffix.size()
            && value.compare(value.size() - suffix.size(), suffix.size(), suffix) == 0;
    }

    bool IsStartTrigger(const std::string& functionName)
    {
        return EndsWith(functionName, ".K2_MatchHasStarted")
            || EndsWith(functionName, ".K2_StartMatchIntro")
            || EndsWith(functionName, ".K2_StartRoleSelection")
            || EndsWith(functionName, ".K2_StartCountdownToStart");
    }

    bool IsCaptureTrigger(const std::string& functionName)
    {
        return EndsWith(functionName, ".ClientMatchHasEnded")
            || EndsWith(functionName, ".K2_StartMatchEnding")
            || EndsWith(functionName, ".K2_MatchHasEnded")
            || EndsWith(functionName, ".K2_StartShowingMatchResult")
            || EndsWith(functionName, ".ClientStartShowingMatchResult")
            || EndsWith(functionName, ".SaveMatchResultInfo")
            || EndsWith(functionName, ".GetLastPostMatchSettlementData");
    }

    bool IsFinalP2PTrigger(const std::string& functionName)
    {
        return EndsWith(functionName, ".K2_StartShowingMatchResult")
            || EndsWith(functionName, ".ClientStartShowingMatchResult");
    }

    bool IsRoundStartTrigger(const std::string& functionName)
    {
        return EndsWith(functionName, ".K2_RoundHasStarted");
    }

    bool IsRoundEndTrigger(const std::string& functionName)
    {
        return EndsWith(functionName, ".K2_RoundHasEnded")
            || EndsWith(functionName, ".ClientRoundHasEnded");
    }

    std::string EnvironmentValue(const char* name, const size_t maximumLength)
    {
        const DWORD required = GetEnvironmentVariableA(name, nullptr, 0);
        if (required <= 1 || required > maximumLength + 1)
            return {};
		std::string value(static_cast<size_t>(required), '\0');
		const DWORD written = GetEnvironmentVariableA(name, value.data(), required);
		if (written != required - 1)
            return {};
		value.resize(written);
        return value;
    }

    bool IsOpaqueContextValue(const std::string& value)
    {
        if (value.empty())
            return false;
        return std::all_of(
            value.begin(), value.end(),
            [](const unsigned char character)
            {
                return std::isalnum(character) || character == '_' || character == '-';
            });
    }

    std::string Hex(const uint8_t* bytes, const size_t length)
    {
        static constexpr char digits[] = "0123456789abcdef";
        std::string result(length * 2, '\0');
        for (size_t index = 0; index < length; ++index)
        {
            result[index * 2] = digits[(bytes[index] >> 4) & 0x0f];
            result[index * 2 + 1] = digits[bytes[index] & 0x0f];
        }
        return result;
    }

    std::string Sha256Hex(const std::string& value)
    {
        BCRYPT_ALG_HANDLE algorithm = nullptr;
        BCRYPT_HASH_HANDLE hash = nullptr;
        DWORD objectLength = 0;
        DWORD copied = 0;
        std::vector<uint8_t> object;
        std::array<uint8_t, 32> digest{};

        if (BCryptOpenAlgorithmProvider(&algorithm, BCRYPT_SHA256_ALGORITHM, nullptr, 0) < 0)
            return {};
        const auto closeAlgorithm = [&]() { BCryptCloseAlgorithmProvider(algorithm, 0); };
        if (BCryptGetProperty(
                algorithm, BCRYPT_OBJECT_LENGTH,
                reinterpret_cast<PUCHAR>(&objectLength), sizeof(objectLength), &copied, 0) < 0)
        {
            closeAlgorithm();
            return {};
        }
        object.resize(objectLength);
        if (BCryptCreateHash(
                algorithm, &hash, object.data(), objectLength, nullptr, 0, 0) < 0)
        {
            closeAlgorithm();
            return {};
        }
        const NTSTATUS hashStatus = BCryptHashData(
            hash,
            reinterpret_cast<PUCHAR>(const_cast<char*>(value.data())),
            static_cast<ULONG>(value.size()), 0);
        const NTSTATUS finishStatus = hashStatus < 0
            ? hashStatus
            : BCryptFinishHash(hash, digest.data(), static_cast<ULONG>(digest.size()), 0);
        BCryptDestroyHash(hash);
        closeAlgorithm();
        return finishStatus < 0 ? std::string() : Hex(digest.data(), digest.size());
    }

    std::string RandomTimelineSessionId()
    {
        std::array<uint8_t, 16> random{};
        if (BCryptGenRandom(
                nullptr, random.data(), static_cast<ULONG>(random.size()),
                BCRYPT_USE_SYSTEM_PREFERRED_RNG) < 0)
        {
            return {};
        }
        return "p2tl_" + Hex(random.data(), random.size());
    }

    P2PContext LoadP2PContext()
    {
        P2PContext context;
        context.MatchId = EnvironmentValue("PROJECT_REBOUND_P2P_MATCH_ID", 64);
        context.CapabilityId = EnvironmentValue("PROJECT_REBOUND_P2P_CAPABILITY_ID", 64);
        context.ServerNonce = EnvironmentValue("PROJECT_REBOUND_P2P_SERVER_NONCE", 128);
        const std::string clientVersion =
            EnvironmentValue("PROJECT_REBOUND_CLIENT_VERSION", 64);
        if (!clientVersion.empty())
            context.ClientVersion = clientVersion;
        const std::string authority =
            EnvironmentValue("PROJECT_REBOUND_P2P_AUTHORITY_KIND", 32);
        if (authority == "LISTEN_HOST_OBSERVER")
            context.AuthorityKind = authority;
        context.Enabled = IsOpaqueContextValue(context.MatchId)
            && IsOpaqueContextValue(context.CapabilityId)
            && IsOpaqueContextValue(context.ServerNonce);
        return context;
    }

    bool IsP2PProcessSide(const ProcessSide side)
    {
        if (!gP2PContext.Enabled)
            return false;
        return gP2PContext.AuthorityKind == "LISTEN_HOST_OBSERVER"
            ? side == ProcessSide::Server
            : side == ProcessSide::Client;
    }

    void ResetP2PTimeline()
    {
        gP2PTimelineActive = false;
        gP2PTimelineFinalized = false;
        gP2PTimelineTruncated = false;
        gP2PStartSnapshotWritten = false;
        gP2PTimelineSequence = 0;
        gP2PTimelineSessionId.clear();
        gP2PTimelineDigest.clear();
        gP2PTimelineEvents = json::array();
    }

    bool BeginP2PTimeline()
    {
        ResetP2PTimeline();
        gP2PTimelineSessionId = RandomTimelineSessionId();
        if (gP2PTimelineSessionId.empty())
            return false;
        gP2PTimelineDigest = Sha256Hex(
            gP2PContext.MatchId + "|" + gP2PContext.CapabilityId + "|"
            + gP2PContext.ServerNonce + "|" + gP2PTimelineSessionId);
        if (gP2PTimelineDigest.empty())
            return false;
        gP2PTimelineStartedAt = std::chrono::steady_clock::now();
        gP2PTimelineActive = true;
        return true;
    }

    bool AppendP2PTimelineEvent(const std::string& type, const json& payload)
    {
        if (!gP2PTimelineActive || gP2PTimelineFinalized)
            return false;
		const size_t eventCount = gP2PTimelineEvents.size();
		if (eventCount >= kMaximumP2PTimelineEvents
			|| (type != "MATCH_ENDED" && eventCount >= kMaximumP2PTimelineEvents - 1))
        {
            gP2PTimelineTruncated = true;
            return false;
        }
        const uint64_t sequence = ++gP2PTimelineSequence;
        const uint64_t elapsed = static_cast<uint64_t>(
            std::chrono::duration_cast<std::chrono::milliseconds>(
                std::chrono::steady_clock::now() - gP2PTimelineStartedAt).count());
        const std::string payloadDigest = Sha256Hex(payload.dump());
        const std::string eventDigest = Sha256Hex(
            gP2PTimelineDigest + "|" + std::to_string(sequence) + "|" + type + "|"
            + std::to_string(elapsed) + "|" + payloadDigest);
        if (payloadDigest.empty() || eventDigest.empty())
            return false;
        gP2PTimelineEvents.push_back({
            {"seq", sequence},
            {"type", type},
            {"local_monotonic_ms", elapsed},
            {"previous_event_hash", gP2PTimelineDigest},
            {"event_hash", eventDigest},
            {"payload", payload},
        });
        gP2PTimelineDigest = eventDigest;
        if (type == "MATCH_ENDED")
            gP2PTimelineFinalized = true;
        return true;
    }

    json P2PTimeline()
    {
		const uint64_t firstSequence = gP2PTimelineEvents.empty()
			? 0
			: gP2PTimelineEvents.front()["seq"].get<uint64_t>();
		const uint64_t lastSequence = gP2PTimelineEvents.empty()
			? 0
			: gP2PTimelineEvents.back()["seq"].get<uint64_t>();
        return {
			{"first_seq", firstSequence},
			{"last_seq", lastSequence},
            {"events_digest", gP2PTimelineDigest},
            {"timeline_truncated", gP2PTimelineTruncated},
            {"events", gP2PTimelineEvents},
        };
    }

    std::string WideToUtf8(const std::wstring& value)
    {
        if (value.empty())
            return {};

        const int required = WideCharToMultiByte(
            CP_UTF8,
            WC_ERR_INVALID_CHARS,
            value.data(),
            static_cast<int>(value.size()),
            nullptr,
            0,
            nullptr,
            nullptr);
        if (required <= 0)
        {
            const int fallbackRequired = WideCharToMultiByte(
                CP_UTF8,
                0,
                value.data(),
                static_cast<int>(value.size()),
                nullptr,
                0,
                nullptr,
                nullptr);
            if (fallbackRequired <= 0)
                return {};

            std::string fallback(static_cast<size_t>(fallbackRequired), '\0');
            WideCharToMultiByte(
                CP_UTF8,
                0,
                value.data(),
                static_cast<int>(value.size()),
                fallback.data(),
                fallbackRequired,
                nullptr,
                nullptr);
            return fallback;
        }

        std::string result(static_cast<size_t>(required), '\0');
        WideCharToMultiByte(
            CP_UTF8,
            WC_ERR_INVALID_CHARS,
            value.data(),
            static_cast<int>(value.size()),
            result.data(),
            required,
            nullptr,
            nullptr);
        return result;
    }

    bool IsReadableMemoryRange(const void* address, const size_t length)
    {
        if (!address || length == 0)
            return false;

        const auto begin = reinterpret_cast<uintptr_t>(address);
        const auto end = begin + length;
        if (end < begin)
            return false;

        auto cursor = begin;
        while (cursor < end)
        {
            MEMORY_BASIC_INFORMATION memory{};
            if (VirtualQuery(reinterpret_cast<const void*>(cursor), &memory, sizeof(memory))
                != sizeof(memory))
            {
                return false;
            }

            const DWORD protection = memory.Protect & 0xff;
            const bool readable = protection == PAGE_READONLY
                || protection == PAGE_READWRITE
                || protection == PAGE_WRITECOPY
                || protection == PAGE_EXECUTE_READ
                || protection == PAGE_EXECUTE_READWRITE
                || protection == PAGE_EXECUTE_WRITECOPY;
            if (memory.State != MEM_COMMIT
                || (memory.Protect & PAGE_GUARD) != 0
                || !readable)
            {
                return false;
            }

            const auto regionBegin = reinterpret_cast<uintptr_t>(memory.BaseAddress);
            const auto regionEnd = regionBegin + memory.RegionSize;
            if (regionEnd <= cursor)
                return false;
            cursor = (std::min)(end, regionEnd);
        }
        return true;
    }

    std::string ToString(const FString& value)
    {
        const int32 length = value.Num();
        if (length <= 0
            || length > value.Max()
            || length > kMaximumSnapshotStringCodeUnits)
        {
            return {};
        }

        const wchar_t* data = value.CStr();
        const size_t byteLength = static_cast<size_t>(length) * sizeof(wchar_t);
        if (!IsReadableMemoryRange(data, byteLength))
            return {};

        size_t textLength = static_cast<size_t>(length);
        if (data[textLength - 1] == L'\0')
            --textLength;
        return WideToUtf8(std::wstring(data, textLength));
    }

    std::string ToString(const FText& value)
    {
        if (!IsReadableMemoryRange(value.TextData, sizeof(FTextImpl::FTextData)))
            return {};
        return ToString(value.GetStringRef());
    }

    std::string ToString(const FName& value)
    {
        return value.ToString();
    }

    bool ContainsCaseInsensitive(std::string value, const std::string& needle)
    {
        std::transform(
            value.begin(),
            value.end(),
            value.begin(),
            [](const unsigned char character)
            {
                return static_cast<char>(std::tolower(character));
            });
        return value.find(needle) != std::string::npos;
    }

    std::string PointerString(const void* pointer)
    {
        std::ostringstream output;
        output << "0x" << std::hex << std::uppercase
               << reinterpret_cast<std::uintptr_t>(pointer);
        return output.str();
    }

    json ObjectRef(const UObject* object)
    {
        if (!object)
            return nullptr;

        return {
            {"address", PointerString(object)},
            {"full_name", object->GetFullName()},
        };
    }

    json PlayerRef(const APBPlayerState* player)
    {
        if (!player)
            return nullptr;

        return {
            {"address", PointerString(player)},
            {"full_name", player->GetFullName()},
            {"player_id", player->PlayerId},
            {"player_name_raw", ToString(player->PlayerNamePrivate)},
            {"user_id_raw", ToString(player->GetUserIdstr())},
            {"platform_id_raw", ToString(player->GetPlatformIDStr())},
        };
    }

    json IntArray(const TArray<int32>& values)
    {
        json result = json::array();
        for (const int32 value : values)
            result.push_back(value);
        return result;
    }

    json NameArray(const TArray<FName>& values)
    {
        json result = json::array();
        for (const FName& value : values)
            result.push_back(ToString(value));
        return result;
    }

    const char* GameModeName(const EPBGameMode value)
    {
        switch (value)
        {
        case EPBGameMode::Base: return "Base";
        case EPBGameMode::TeamDeathMatch: return "TeamDeathMatch";
        case EPBGameMode::FreeForAll: return "FreeForAll";
        case EPBGameMode::RushMatch: return "RushMatch";
        case EPBGameMode::CaptureTheVehicle: return "CaptureTheVehicle";
        case EPBGameMode::KOHMatch: return "KOHMatch";
        case EPBGameMode::Elimination: return "Elimination";
        case EPBGameMode::Domination: return "Domination";
        case EPBGameMode::Skirmish: return "Skirmish";
        case EPBGameMode::CSTM: return "CSTM";
        case EPBGameMode::Menu: return "Menu";
        case EPBGameMode::TrainingLevel: return "TrainingLevel";
        case EPBGameMode::MaxGameModeNum: return "MaxGameModeNum";
        default: return "Unknown";
        }
    }

    const char* GameResultName(const EPBGameResult value)
    {
        switch (value)
        {
        case EPBGameResult::Won: return "Won";
        case EPBGameResult::Lost: return "Lost";
        case EPBGameResult::Draw: return "Draw";
        case EPBGameResult::Suspend: return "Suspend";
        case EPBGameResult::None: return "None";
        default: return "Unknown";
        }
    }

    const char* TeamName(const EPBTeam value)
    {
        switch (value)
        {
        case EPBTeam::SolarSysDefences: return "SolarSysDefences";
        case EPBTeam::StarExiles: return "StarExiles";
        case EPBTeam::TeamNumber: return "TeamNumber";
        case EPBTeam::Invalid: return "Invalid";
        default: return "Unknown";
        }
    }

    const char* RoleName(const EPBRole value)
    {
        switch (value)
        {
        case EPBRole::Assualt: return "Assualt";
        case EPBRole::Recon: return "Recon";
        case EPBRole::Medic: return "Medic";
        case EPBRole::Assassin: return "Assassin";
        case EPBRole::Tank: return "Tank";
        case EPBRole::Sniper: return "Sniper";
        default: return "Unknown";
        }
    }

    template <typename Enum>
    json EnumValue(const Enum value, const char* name)
    {
        return {
            {"value", static_cast<unsigned int>(value)},
            {"name", name},
        };
    }

    json SerializePlayerMatchResultInfo(const FPlayerMatchResultInfo& value)
    {
        return {
            {"player_name", ToString(value.PlayerName)},
            {"kill", value.Kill},
            {"death", value.Death},
            {"assist", value.Assist},
            {"score", value.Score},
            {"team_id", value.TeamID},
            {"is_spectator", value.IsSpectator},
        };
    }

    json SerializePersonalMatchResult(const FMatchResultPersonalInfo& value)
    {
        json unlockItems = json::array();
        for (const auto& pair : value.UnlockItem)
        {
            unlockItems.push_back({
                {"item", ToString(pair.Key())},
                {"count", pair.Value()},
            });
        }

        json achievements = json::array();
        for (const FPBAchievementStatInfo& achievement : value.CombatAchievementStat)
        {
            achievements.push_back({
                {"name", ToString(achievement.Name)},
                {"image", ObjectRef(achievement.ImageInfo)},
                {"count", achievement.Count},
            });
        }

        return {
            {"is_spectator", value.bIsSpectator},
            {"exp_multi", value.ExpMulti},
            {"unlock_items", std::move(unlockItems)},
            {"headshot_count", value.HeadShotCount},
            {"accuracy", value.Accuracy},
            {"max_kill_distance", value.MaxKillDistance},
            {"avg_kill_distance", value.AvgKillDistance},
            {"spm", value.SPM},
            {"max_kill_streak_count", value.MaxKillStreakCount},
            {"score", value.Score},
            {"kill", value.Kill},
            {"death", value.Death},
            {"assist", value.Assist},
            {"kda", value.KDA},
            {"team_id", value.TeamID},
            {"player_array_self_index", value.PlayerArraySelfIndex},
            {"combat_achievements", std::move(achievements)},
        };
    }

    json SerializeMatchResultInfo(const FMatchResultInfo& value)
    {
        json players = json::array();
        for (const FPlayerMatchResultInfo& player : value.PlayersInfo)
            players.push_back(SerializePlayerMatchResultInfo(player));

        return {
            {"players", std::move(players)},
            {"map_name", ToString(value.MapName)},
            {"game_mode", EnumValue(value.GameMode, GameModeName(value.GameMode))},
            {"match_time", value.MatchTime},
            {"team_scores", IntArray(value.TeamScore)},
            {"personal", SerializePersonalMatchResult(value.PersonalMatchResultInfo)},
        };
    }

    json SerializeRoundResult(const FPBRoundResult& value)
    {
        return {
            {"winner_team_id", value.RoundWinnerTeamID},
            {"team_scores", IntArray(value.TeamRoundScores)},
            {"is_final_round", value.bFinalRound},
        };
    }

    json SerializeMatchResult(const FPBMatchResult& value)
    {
        json rounds = json::array();
        for (const FPBRoundResult& round : value.RoundResults)
            rounds.push_back(SerializeRoundResult(round));

        return {
            {"winner_team_id", value.MatchWinnerTeamID},
            {"team_scores", IntArray(value.TeamMatchScores)},
            {"mvp", PlayerRef(value.MVPPBPlayerState)},
            {"rounds", std::move(rounds)},
        };
    }

    json SerializePostMatchPlayer(const FPBPostMatchPlayerStatisticData& value)
    {
        return {
            {"user_id", ToString(value.UserID)},
            {"nickname", ToString(value.NickName)},
            {"player_score", value.PlayerScore},
            {"kill", value.Kill},
            {"assist", value.Assist},
            {"death", value.Death},
            {"kd", value.KD},
            {"is_mvp", value.bIsMvp},
        };
    }

    json SerializePostMatchSettlement(const FPBLastPostMatchSettlementInfo& value)
    {
        json teams = json::array();
        for (const auto& teamPair : value.ParticipatingTeams)
        {
            const FPBPostMatchTeamStatisticData& team = teamPair.Value();
            json members = json::array();
            for (const auto& memberPair : team.TeamMembers)
            {
                members.push_back({
                    {"map_key", ToString(memberPair.Key())},
                    {"data", SerializePostMatchPlayer(memberPair.Value())},
                });
            }

            teams.push_back({
                {"map_key", teamPair.Key()},
                {"team_id", team.TeamID},
                {"score", team.Score},
                {"result", team.Result},
                {"members", std::move(members)},
            });
        }

        json medals = json::array();
        for (const auto& medalPair : value.MedalStatistic)
        {
            medals.push_back({
                {"medal", ToString(medalPair.Key())},
                {"count", medalPair.Value()},
            });
        }

        return {
            {"mode", ToString(value.Mode)},
            {"participating_teams", std::move(teams)},
            {"medals", std::move(medals)},
        };
    }

    json SerializeNameInt64Map(const TMap<FName, int64>& values)
    {
        json result = json::array();
        for (const auto& pair : values)
        {
            result.push_back({
                {"key", ToString(pair.Key())},
                {"value", pair.Value()},
            });
        }
        return result;
    }

    json SerializeNameInt32Map(const TMap<FName, int32>& values)
    {
        json result = json::array();
        for (const auto& pair : values)
        {
            result.push_back({
                {"key", ToString(pair.Key())},
                {"value", pair.Value()},
            });
        }
        return result;
    }

    json SerializeInGameData(const FPBInGameData& value)
    {
        json hitHistory = json::array();
        for (const FPBBeHitInfo& hit : value.BeHitHistory)
        {
            int64 dateTimeTicks = 0;
            static_assert(sizeof(dateTimeTicks) == sizeof(hit.BeHitDateTime));
            std::memcpy(&dateTimeTicks, &hit.BeHitDateTime, sizeof(dateTimeTicks));

            UClass* damageType = hit.DamageEvent.DamageTypeClass;
            hitHistory.push_back({
                {"instigator", PlayerRef(hit.InstigatorPlayer)},
                {"damage_causer", ObjectRef(hit.DamageCauser)},
                {"damage", hit.Damage},
                {"damage_type", ObjectRef(damageType)},
                {"be_hit_datetime_ticks_raw", dateTimeTicks},
            });
        }

        json damageCausers = json::array();
        for (const AActor* damageCauser : value.DamageCausers)
            damageCausers.push_back(ObjectRef(damageCauser));

        return {
            {"kill_in_one_magazine", value.KillInOneMagazine},
            {"last_break_air_tightness_player", PlayerRef(value.LastBreakAirTightnessPlayer)},
            {"be_hit_history", std::move(hitHistory)},
            {"capturing_zone", ObjectRef(value.CapturingCapturableZone)},
            {"emp_hit_count", value.EMPHitCount},
            {"damage_causers", std::move(damageCausers)},
            {"fix_space_suit_count", value.FixSpaceSuitCount},
            {"is_in_capture_zone", value.bIsInCaptureZone},
        };
    }

    json SerializePlayerState(APBPlayerState* player, APBGameMode* gameMode)
    {
        const EPBGameResult matchResult = player->GetMatchResult();
        const EPBGameResult lastRoundResult = player->GetLastRoundResult();
        const EPBTeam team = player->GetTeamEnum();
        const FGenericTeamId genericTeam = player->GetGenericTeamId();
        const FPBInGameData inGameData = player->GetInGameData();

        json platformUniqueId = nullptr;
        const std::string platformUniqueIdRaw = ToString(player->PlatformUniqueIDJsonString);
        if (!platformUniqueIdRaw.empty())
        {
            platformUniqueId = json::parse(platformUniqueIdRaw, nullptr, false);
            if (platformUniqueId.is_discarded())
                platformUniqueId = nullptr;
        }

        json characterLevels = json::array();
        const int32 characterCount = player->CharacterLevels.CharacterIds.Num();
        for (int32 index = 0; index < characterCount; ++index)
        {
            characterLevels.push_back({
                {"character_id", ToString(player->CharacterLevels.CharacterIds[index])},
                {"level", player->CharacterLevels.CharacterLevels.IsValidIndex(index)
                    ? json(player->CharacterLevels.CharacterLevels[index])
                    : json(nullptr)},
            });
        }

        json result = {
            {"object", ObjectRef(player)},
            {"identity", {
                {"player_id", player->PlayerId},
                {"player_name", ToString(player->GetPlayerName())},
                {"player_name_raw", ToString(player->PlayerNamePrivate)},
                {"short_player_name", ToString(player->GetShortPlayerName())},
                {"player_name_before_filter", ToString(player->PlayerNameBeforeFilter)},
                {"user_id", ToString(player->GetUserIdstr())},
                {"default_id", ToString(player->GetDefaultIDStr())},
                {"platform_id", ToString(player->GetPlatformIDStr())},
                {"platform_unique_id_json_raw", platformUniqueIdRaw},
                {"platform_unique_id_json", std::move(platformUniqueId)},
                {"assigned_voice_account_name", ToString(player->AssignedVoiceAccountName)},
            }},
            {"assignment", {
                {"selected_character_id", ToString(player->SelectedCharacterID)},
                {"possessed_character_id", ToString(player->PossessedCharacterId)},
                {"usage_character_id", ToString(player->UsageCharacterID)},
                {"role", EnumValue(player->PBRole, RoleName(player->PBRole))},
                {"level", player->Level},
                {"player_index", player->PlayerIndex},
                {"playing_game_time", player->PlayingGameTime},
                {"team_id", player->GetTeamId()},
                {"generic_team_id", genericTeam.TeamID},
                {"camp_id", player->GetCampID()},
                {"team_num", player->GetTeamNum()},
                {"team", EnumValue(team, TeamName(team))},
                {"has_selected_role", player->HasSelectedRole()},
                {"has_ever_played_as_character", player->bHasEverPlayAsCharacterInMatch},
                {"is_visitor", player->IsVisitor},
                {"character_levels", std::move(characterLevels)},
            }},
            {"raw_fields", {
                {"score", player->Score},
                {"num_kills", player->NumKills},
                {"num_deaths", player->NumDeaths},
                {"num_assists", player->NumAssists},
                {"num_bullets_fired", player->NumBulletsFired},
                {"num_rockets_fired", player->NumRocketsFired},
                {"ping_compressed", player->ping},
                {"start_time", player->StartTime},
                {"is_spectator", static_cast<bool>(player->bIsSpectator)},
                {"only_spectator", static_cast<bool>(player->bOnlySpectator)},
                {"is_bot", static_cast<bool>(player->bIsABot)},
                {"is_inactive", static_cast<bool>(player->bIsInactive)},
            }},
            {"computed_stats", {
                {"kill", player->GetKills()},
                {"death", player->GetDeaths()},
                {"assist", player->GetAssist()},
                {"player_score", player->GetPlayerScore()},
                {"team_score", player->GetTeamScoreOfThePlayer()},
                {"kd_ratio", player->GetKDRatio()},
                {"spm", player->GetSPM()},
                {"headshot_count", player->GetHeadShotCount()},
                {"avg_kill_distance", player->GetAvgKillDistance()},
                {"max_kill_distance", player->GetMaxKillDistance()},
                {"killing_streak_count", player->GetKillingStreakCount()},
                {"max_kill_streak_count", player->GetMaxKillStreakCount()},
                {"num_bullets_fired", player->GetNumBulletsFired()},
                {"num_rockets_fired", player->GetNumRocketsFired()},
                {"ping_ms", player->GetPingInMilliseconds()},
            }},
            {"outcome", {
                {"match_result", EnumValue(matchResult, GameResultName(matchResult))},
                {"last_round_result", EnumValue(lastRoundResult, GameResultName(lastRoundResult))},
                {"is_match_mvp", player->IsMatchMVP()},
                {"is_match_winner", player->IsMatchWinner()},
                {"is_round_winner", player->IsRoundWinner()},
                {"is_quitter", player->IsQuitter()},
            }},
            {"role_score_map", SerializeNameInt64Map(player->RoleScoreMap)},
            {"match_character_score_map", SerializeNameInt32Map(player->MatchCharacterScoreMap)},
            {"in_game_data", SerializeInGameData(inGameData)},
        };

        if (gameMode)
            result["server_match_result_info"] = SerializeMatchResultInfo(gameMode->GetMatchResultInfo(player));
        else
            result["server_match_result_info"] = nullptr;

        return result;
    }

    std::vector<APBPlayerState*> CollectPlayers(APBGameState* gameState)
    {
        std::vector<APBPlayerState*> players;
        std::unordered_set<APBPlayerState*> seen;
        if (!gameState)
            return players;

        for (APBPlayerState* player : gameState->PBPlayerArray)
        {
            if (player && seen.insert(player).second)
                players.push_back(player);
        }

        for (APlayerState* basePlayer : gameState->PlayerArray)
        {
            if (!basePlayer || !basePlayer->IsA(APBPlayerState::StaticClass()))
                continue;

            APBPlayerState* player = static_cast<APBPlayerState*>(basePlayer);
            if (seen.insert(player).second)
                players.push_back(player);
        }

        return players;
    }

    MatchClassification ClassifyMatch(
        const ProcessSide side,
        APBGameState* gameState,
        APBGameMode* gameMode)
    {
        MatchClassification result;

        const std::string modeAlias = gameState
            ? ToString(gameState->ModeInfo.ModeAliasName)
            : std::string();
        const std::string gameStateObject = gameState
            ? gameState->GetFullName()
            : std::string();
        const std::string gameModeObject = gameMode
            ? gameMode->GetFullName()
            : std::string();
        const std::string configuredModePath = WideToUtf8(Config.FullModePath);

        if (!modeAlias.empty())
            result.Evidence.push_back("mode_alias=" + modeAlias);
        if (!gameStateObject.empty())
            result.Evidence.push_back("game_state=" + gameStateObject);
        if (!gameModeObject.empty())
            result.Evidence.push_back("game_mode=" + gameModeObject);
        if (side == ProcessSide::Server && !configuredModePath.empty())
            result.Evidence.push_back("configured_mode_path=" + configuredModePath);

        const std::vector<std::string> modeEvidence = {
            modeAlias,
            gameStateObject,
            gameModeObject,
            configuredModePath,
        };
        const bool metadataSaysPvE = std::any_of(
            modeEvidence.begin(),
            modeEvidence.end(),
            [](const std::string& evidence)
            {
                return ContainsCaseInsensitive(evidence, "pve");
            });

        // LoadConfig() runs before a dedicated server starts listening, so this
        // flag is authoritative when set. Explicit runtime PVE metadata may
        // override a missing -pve flag because launchers can provide only a
        // PVE mode path.
        if (side == ProcessSide::Server)
        {
            result.Evidence.push_back(
                std::string("Config.IsPvE=") + (Config.IsPvE ? "true" : "false"));
            if (Config.IsPvE)
            {
                result.Type = "pve";
                result.Source = "server_config";
                result.Confidence = "authoritative";
            }
            else if (metadataSaysPvE)
            {
                result.Type = "pve";
                result.Source = "runtime_mode_metadata_override";
                result.Confidence = "high";
                result.Evidence.push_back(
                    "runtime PVE metadata overrides missing -pve launch flag");
            }
            else
            {
                result.Type = "pvp";
                result.Source = "server_config";
                result.Confidence = "authoritative";
            }
            return result;
        }

        if (metadataSaysPvE)
        {
            result.Type = "pve";
            result.Source = "sdk_mode_metadata";
            result.Confidence = "high";
            return result;
        }

        for (const std::string& evidence : modeEvidence)
        {
            if (ContainsCaseInsensitive(evidence, "pvp"))
            {
                result.Type = "pvp";
                result.Source = "sdk_mode_metadata";
                result.Confidence = "high";
                return result;
            }
        }

        // Project Boundary's client mode metadata names PvE variants explicitly
        // (for example Rush_PVE_Normal). A populated non-PvE alias is therefore
        // useful PvP evidence, but is marked inferred rather than authoritative.
        if (!modeAlias.empty())
        {
            result.Type = "pvp";
            result.Source = "sdk_mode_metadata_non_pve";
            result.Confidence = "inferred";
        }

        return result;
    }

    void AddParticipant(
        ParticipantAggregate& aggregate,
        const APBPlayerState* player)
    {
        ++aggregate.Count;
        aggregate.Kills += player->NumKills;
        aggregate.Deaths += player->NumDeaths;
        aggregate.Assists += player->NumAssists;
        aggregate.Score += player->Score;
    }

    json SerializeParticipantAggregate(const ParticipantAggregate& value)
    {
        return {
            {"count", value.Count},
            {"kills", value.Kills},
            {"deaths", value.Deaths},
            {"assists", value.Assists},
            {"score", value.Score},
        };
    }

    json SerializeParticipantSummary(
        const std::vector<APBPlayerState*>& players)
    {
        ParticipantAggregate all;
        ParticipantAggregate humans;
        ParticipantAggregate ai;
        std::map<int32, TeamAggregate> teams;

        for (const APBPlayerState* player : players)
        {
            if (!player)
                continue;

            const bool isAI = static_cast<bool>(player->bIsABot);
            const int32 teamId = player->GetTeamId();
            AddParticipant(all, player);
            AddParticipant(isAI ? ai : humans, player);
            AddParticipant(teams[teamId].All, player);
            AddParticipant(
                isAI ? teams[teamId].AI : teams[teamId].Humans,
                player);
        }

        json teamValues = json::array();
        for (const auto& [teamId, team] : teams)
        {
            teamValues.push_back({
                {"team_id", teamId},
                {"all", SerializeParticipantAggregate(team.All)},
                {"humans", SerializeParticipantAggregate(team.Humans)},
                {"ai", SerializeParticipantAggregate(team.AI)},
            });
        }

        return {
            {"all", SerializeParticipantAggregate(all)},
            {"humans", SerializeParticipantAggregate(humans)},
            {"ai", SerializeParticipantAggregate(ai)},
            {"teams", std::move(teamValues)},
        };
    }

    json SerializeGameState(APBGameState* gameState)
    {
        if (!gameState)
            return nullptr;

        json teamTotalKills = json::array();
        for (const auto& pair : gameState->TeamTotalKills)
        {
            teamTotalKills.push_back({
                {"team_id", pair.Key()},
                {"kills", pair.Value()},
            });
        }

        const FPBMatchResult matchResult = gameState->GetMatchResult();
        const FPBRoundResult lastRoundResult = gameState->GetLastRoundResult();

        return {
            {"object", ObjectRef(gameState)},
            {"assigned_match_id", ToString(gameState->AssignedMatchID)},
            {"match_sub_state", ToString(gameState->MatchSubState)},
            {"round_state", ToString(gameState->RoundState)},
            {"current_round_count", gameState->CurrentRoundCount},
            {"max_round_count", gameState->MaxRoundCount},
            {"current_game_mode", EnumValue(
                gameState->CurrentGameModeEnum,
                GameModeName(gameState->CurrentGameModeEnum))},
            {"map_alias_name", ToString(gameState->CurrentPlayingMapAliasName)},
            {"map_display_name", ToString(gameState->CurrentPlayingMapNameText)},
            {"mode", {
                {"alias_name", ToString(gameState->ModeInfo.ModeAliasName)},
                {"full_name", ToString(gameState->ModeInfo.ModeFullyName)},
                {"description", ToString(gameState->ModeInfo.Description)},
                {"estimated_play_time", gameState->ModeInfo.EstimatedPlayTime},
            }},
            {"total_game_time", gameState->TotalGameTime},
            {"elapsed_time", gameState->GetElapsedTime()},
            {"remaining_time", gameState->RemainingTime},
            {"replicated_remaining_time", gameState->ReplicatedRemainingTime},
            {"timer_paused", gameState->bTimerPaused},
            {"match_goal_score", gameState->MatchGoalScore},
            {"round_goal_scores", IntArray(gameState->RoundGoalScore)},
            {"team_match_scores", IntArray(gameState->TeamMatchScores)},
            {"team_round_scores", IntArray(gameState->TeamRoundScores)},
            {"team_zero_kills", gameState->TeamZeroKills},
            {"team_one_kills", gameState->TeamOneKills},
            {"team_total_kills", std::move(teamTotalKills)},
            {"host_player_num", gameState->HostPlayerNum},
            {"max_player_num", gameState->MaxPlayerNum},
            {"match_result", SerializeMatchResult(matchResult)},
            {"last_round_result", SerializeRoundResult(lastRoundResult)},
        };
    }

    std::string Iso8601UtcNow()
    {
        const auto now = std::chrono::system_clock::now();
        const std::time_t raw = std::chrono::system_clock::to_time_t(now);
        std::tm utc{};
        gmtime_s(&utc, &raw);

        const auto milliseconds = std::chrono::duration_cast<std::chrono::milliseconds>(
            now.time_since_epoch()) % 1000;

        std::ostringstream output;
        output << std::put_time(&utc, "%Y-%m-%dT%H:%M:%S")
               << '.' << std::setw(3) << std::setfill('0') << milliseconds.count()
               << 'Z';
        return output.str();
    }

    std::string SafeFilenamePart(std::string value)
    {
        for (char& character : value)
        {
            const unsigned char byte = static_cast<unsigned char>(character);
            if (!std::isalnum(byte) && character != '-' && character != '_')
                character = '_';
        }

        while (!value.empty() && value.back() == '_')
            value.pop_back();
        if (value.empty())
            value = "unknown";
        if (value.size() > 80)
            value.resize(80);
        return value;
    }

    std::filesystem::path OutputDirectory()
    {
        wchar_t localAppData[MAX_PATH]{};
        const DWORD length = GetEnvironmentVariableW(
            L"LOCALAPPDATA",
            localAppData,
            static_cast<DWORD>(std::size(localAppData)));
        if (length > 0 && length < std::size(localAppData))
        {
            return std::filesystem::path(localAppData)
                / L"ProjectRebound"
                / L"battlelog-dumps";
        }

        return std::filesystem::current_path() / "battlelog-dumps";
    }

    std::string TriggerLeaf(const std::string& functionName)
    {
        const size_t separator = functionName.find_last_of('.');
        return separator == std::string::npos
            ? functionName
            : functionName.substr(separator + 1);
    }

    json SdkLayout()
    {
        return {
            {"FPlayerMatchResultInfo", {
                {"size", sizeof(FPlayerMatchResultInfo)},
                {"fields", {"PlayerName", "Kill", "Death", "Assist", "Score", "TeamID", "IsSpectator"}},
            }},
            {"FMatchResultPersonalInfo", {
                {"size", sizeof(FMatchResultPersonalInfo)},
                {"fields", {
                    "bIsSpectator", "ExpMulti", "UnlockItem", "HeadShotCount", "Accuracy",
                    "MaxKillDistance", "AvgKillDistance", "SPM", "MaxKillStreakCount",
                    "Score", "Kill", "Death", "Assist", "KDA", "TeamID",
                    "PlayerArraySelfIndex", "CombatAchievementStat",
                }},
            }},
            {"FMatchResultInfo", {
                {"size", sizeof(FMatchResultInfo)},
                {"fields", {"PlayersInfo", "MapName", "GameMode", "MatchTime", "TeamScore", "PersonalMatchResultInfo"}},
            }},
            {"FPBMatchResult", {
                {"size", sizeof(FPBMatchResult)},
                {"fields", {"MatchWinnerTeamID", "TeamMatchScores", "MVPPBPlayerState", "RoundResults"}},
            }},
            {"FPBLastPostMatchSettlementInfo", {
                {"size", sizeof(FPBLastPostMatchSettlementInfo)},
                {"fields", {"ParticipatingTeams", "MedalStatistic", "Mode"}},
            }},
            {"APBPlayerState", {
                {"size", sizeof(APBPlayerState)},
                {"note", "raw replicated fields plus SDK getter results are emitted per player"},
            }},
        };
    }

    void Capture(
        const ProcessSide side,
        UObject* object,
        const std::string& functionName,
        void* parms)
    {
        CaptureScope captureScope;

        UWorld* world = UWorld::GetWorld();
        APBGameState* gameState = nullptr;
        APBGameMode* gameMode = nullptr;
        UPBGameInstance* gameInstance = nullptr;

        if (world)
        {
            if (world->GameState && world->GameState->IsA(APBGameState::StaticClass()))
                gameState = static_cast<APBGameState*>(world->GameState);
            if (world->AuthorityGameMode && world->AuthorityGameMode->IsA(APBGameMode::StaticClass()))
                gameMode = static_cast<APBGameMode*>(world->AuthorityGameMode);
            if (world->OwningGameInstance
                && world->OwningGameInstance->IsA(UPBGameInstance::StaticClass()))
            {
                gameInstance = static_cast<UPBGameInstance*>(world->OwningGameInstance);
            }
        }

        std::vector<std::string> warnings;
        if (!world)
            warnings.emplace_back("UWorld::GetWorld() returned null");
        if (!gameState)
            warnings.emplace_back("APBGameState was not available at this trigger");
        if (side == ProcessSide::Server && !gameMode)
            warnings.emplace_back("APBGameMode was not available; personalized server result is null");

        const std::string matchId = gameState
            ? ToString(gameState->AssignedMatchID)
            : std::string();
        const MatchClassification classification =
            ClassifyMatch(side, gameState, gameMode);
        const std::vector<APBPlayerState*> players = CollectPlayers(gameState);
        const json participantSummary = SerializeParticipantSummary(players);
        const json finalResult = gameState
            ? SerializeMatchResult(gameState->GetMatchResult())
            : json(nullptr);
		const bool p2pSnapshot = IsP2PProcessSide(side) && gP2PTimelineActive;
		const bool finalP2PSnapshot = p2pSnapshot && IsFinalP2PTrigger(functionName);
		if (p2pSnapshot && !gP2PTimelineFinalized)
		{
			AppendP2PTimelineEvent("STAT_CHECKPOINT", {
				{"trigger", TriggerLeaf(functionName)},
				{"participants", participantSummary},
			});
			if (finalP2PSnapshot)
			{
				AppendP2PTimelineEvent("MATCH_ENDED", {
					{"trigger", TriggerLeaf(functionName)},
					{"result", finalResult},
				});
			}
		}
        json pveRecord = nullptr;
        json pvpRecord = nullptr;
        const json classifiedRecord = {
            {"result", finalResult},
            {"participants", participantSummary},
        };
        if (classification.Type == "pve")
            pveRecord = classifiedRecord;
        else if (classification.Type == "pvp")
            pvpRecord = classifiedRecord;

        json root = {
            {"schema", "project-rebound.battlelog.raw"},
            {"schema_version", 2},
            {"captured_at_utc", Iso8601UtcNow()},
            {"source", side == ProcessSide::Server ? "server" : "client"},
            {"trigger", functionName},
            {"match_id", matchId},
            {"match_classification", {
                {"type", classification.Type},
                {"is_pve", classification.Type == "pve"},
                {"is_pvp", classification.Type == "pvp"},
                {"source", classification.Source},
                {"confidence", classification.Confidence},
                {"evidence", classification.Evidence},
            }},
            {"participant_summary", participantSummary},
            {"pve_record", std::move(pveRecord)},
            {"pvp_record", std::move(pvpRecord)},
            {"trigger_object", ObjectRef(object)},
            {"world", ObjectRef(world)},
            {"sdk_layout", SdkLayout()},
            {"game_state", SerializeGameState(gameState)},
            {"players", json::array()},
            {"rpc_match_result", nullptr},
            {"save_match_result_info_parameter", nullptr},
            {"game_instance_local_match_result_info", nullptr},
            {"career_post_match_settlement", nullptr},
            {"warnings", warnings},
        };

		if (p2pSnapshot)
		{
			root["schema"] = "project-rebound.p2p-battlelog.raw";
			root["schema_version"] = 3;
			root["p2p_match_id"] = gP2PContext.MatchId;
			root["capability_id"] = gP2PContext.CapabilityId;
			root["server_nonce"] = gP2PContext.ServerNonce;
			root["authority_kind"] = gP2PContext.AuthorityKind;
			root["client_version"] = gP2PContext.ClientVersion;
			root["timeline_session_id"] = gP2PTimelineSessionId;
			root["report_completeness"] = finalP2PSnapshot ? "FINAL" : "PARTIAL";
			root["report_revision"] = 1;
			root["timeline"] = P2PTimeline();
		}

        for (APBPlayerState* player : players)
            root["players"].push_back(SerializePlayerState(player, gameMode));

        if (parms && EndsWith(functionName, ".ClientMatchHasEnded"))
        {
            const auto* matchParms =
                static_cast<const Params::PBPlayerController_ClientMatchHasEnded*>(parms);
            root["rpc_match_result"] = SerializeMatchResult(matchParms->MatchResult);
        }

        if (parms && EndsWith(functionName, ".SaveMatchResultInfo"))
        {
            const auto* saveParms =
                static_cast<const Params::PBGameInstance_SaveMatchResultInfo*>(parms);
            root["save_match_result_info_parameter"] =
                SerializeMatchResultInfo(saveParms->InInfo);
        }

        if (gameInstance)
        {
            root["game_instance_local_match_result_info"] =
                SerializeMatchResultInfo(gameInstance->LocalMatchResultInfo);
        }

        if (parms && EndsWith(functionName, ".GetLastPostMatchSettlementData"))
        {
            const auto* settlementParms =
                static_cast<const Params::PBCareerManager_GetLastPostMatchSettlementData*>(parms);
            root["career_post_match_settlement"] =
                SerializePostMatchSettlement(settlementParms->ReturnValue);
        }
        else if (side == ProcessSide::Client)
        {
            UObject* careerObject = GetLastOfType(UPBCareerManager::StaticClass(), false);
            if (careerObject)
            {
                auto* careerManager = static_cast<UPBCareerManager*>(careerObject);
                root["career_post_match_settlement"] =
                    SerializePostMatchSettlement(
                        careerManager->GetLastPostMatchSettlementData());
            }
            else
            {
                root["warnings"].push_back(
                    "UPBCareerManager was not available at this trigger");
            }
        }

        std::error_code directoryError;
        const std::filesystem::path directory =
            OutputDirectory() / classification.Type;
        std::filesystem::create_directories(directory, directoryError);
        if (directoryError)
        {
            Log("[BATTLELOG] Cannot create dump directory: "
                + directory.string() + " (" + directoryError.message() + ")");
            return;
        }

        const std::string sourceName =
            side == ProcessSide::Server ? "server" : "client";
        const std::string stageName = SafeFilenamePart(TriggerLeaf(functionName));
        const std::string matchName =
            SafeFilenamePart(matchId.empty() ? "match-id-unavailable" : matchId);
        const uint64_t sequence = ++gFileSequence;

        std::filesystem::path outputPath =
            directory
            / (CurrentTimestamp()
                + "_" + std::to_string(sequence)
                + "_" + classification.Type
                + "_" + sourceName
                + "_" + matchName
                + "_" + stageName
                + ".json");
		if (p2pSnapshot)
			outputPath += ".ready";
		const std::filesystem::path writePath = p2pSnapshot
			? std::filesystem::path(outputPath.string() + ".tmp")
			: outputPath;

		std::ofstream output(writePath, std::ios::binary | std::ios::trunc);
        if (!output.is_open())
        {
			Log("[BATTLELOG] Cannot open dump file: " + writePath.string());
            return;
        }

        output << root.dump(2);
        output.close();
		if (p2pSnapshot)
		{
			std::error_code renameError;
			std::filesystem::rename(writePath, outputPath, renameError);
			if (renameError)
			{
				std::error_code cleanupError;
				std::filesystem::remove(writePath, cleanupError);
				Log("[BATTLELOG] Cannot seal P2P snapshot: "
					+ outputPath.string() + " (" + renameError.message() + ")");
				return;
			}
		}
        Log("[BATTLELOG] Raw post-match snapshot: " + outputPath.string());
    }
}

void ResetForMatchGeneration(SDK::UWorld* world)
{
    gObservedWorld = world;
    gCapturedStages.clear();
    gP2PContext = LoadP2PContext();
    ResetP2PTimeline();
    Log("[BATTLELOG] Reset match-scoped capture state for a new generation.");
}

void OnProcessEventPost(
    const ProcessSide side,
    SDK::UObject* object,
    const std::string& functionName,
    void* parms)
{
    if (gCaptureDepth > 0 || functionName.empty())
        return;

    SDK::UWorld* currentWorld = SDK::UWorld::GetWorld();
    if (currentWorld != gObservedWorld)
    {
        gObservedWorld = currentWorld;
        gCapturedStages.clear();
		gP2PContext = LoadP2PContext();
		ResetP2PTimeline();
    }

    if (IsStartTrigger(functionName))
    {
        gCapturedStages.clear();
		if (IsP2PProcessSide(side) && !gP2PTimelineActive && BeginP2PTimeline())
		{
			AppendP2PTimelineEvent("MATCH_STARTED", {
				{"trigger", TriggerLeaf(functionName)},
			});
			if (!gP2PStartSnapshotWritten)
			{
				gP2PStartSnapshotWritten = true;
				try
				{
					Capture(side, object, functionName, parms);
				}
				catch (const std::exception& exception)
				{
					Log("[BATTLELOG] Initial P2P snapshot failed: "
						+ std::string(exception.what()));
				}
				catch (...)
				{
					Log("[BATTLELOG] Initial P2P snapshot failed: unknown exception");
				}
			}
		}
        return;
    }

	if ((IsRoundStartTrigger(functionName) || IsRoundEndTrigger(functionName))
		&& IsP2PProcessSide(side) && gP2PTimelineActive && !gP2PTimelineFinalized)
	{
		auto* gameState = currentWorld && currentWorld->GameState
			&& currentWorld->GameState->IsA(SDK::APBGameState::StaticClass())
			? static_cast<SDK::APBGameState*>(currentWorld->GameState)
			: nullptr;
		const bool roundEnded = IsRoundEndTrigger(functionName);
		const int32_t roundCount = gameState ? gameState->CurrentRoundCount : -1;
		const std::string roundKey = std::string(roundEnded ? "round-end|" : "round-start|")
			+ std::to_string(roundCount);
		if (gCapturedStages.insert(roundKey).second)
		{
			AppendP2PTimelineEvent(roundEnded ? "ROUND_ENDED" : "ROUND_STARTED", {
				{"round_index", roundCount},
				{"trigger", TriggerLeaf(functionName)},
				{"result", gameState ? SerializeRoundResult(gameState->GetLastRoundResult()) : json(nullptr)},
			});
			if (roundEnded)
			{
				try
				{
					Capture(side, object, functionName, parms);
				}
				catch (const std::exception& exception)
				{
					Log("[BATTLELOG] Round P2P checkpoint failed: "
						+ std::string(exception.what()));
				}
				catch (...)
				{
					Log("[BATTLELOG] Round P2P checkpoint failed: unknown exception");
				}
			}
		}
		return;
	}

    if (!IsCaptureTrigger(functionName))
        return;
	if (IsP2PProcessSide(side) && gP2PTimelineFinalized)
		return;

    std::string matchId;
    if (currentWorld
        && currentWorld->GameState
        && currentWorld->GameState->IsA(SDK::APBGameState::StaticClass()))
    {
        auto* gameState = static_cast<SDK::APBGameState*>(currentWorld->GameState);
        matchId = ToString(gameState->AssignedMatchID);
    }

    const std::string stageKey =
        (side == ProcessSide::Server ? "server|" : "client|")
        + matchId + "|" + functionName;
    if (!gCapturedStages.insert(stageKey).second)
        return;

    try
    {
        Capture(side, object, functionName, parms);
    }
    catch (const std::exception& exception)
    {
        Log("[BATTLELOG] Snapshot failed at " + functionName + ": " + exception.what());
    }
    catch (...)
    {
        Log("[BATTLELOG] Snapshot failed at " + functionName + ": unknown exception");
    }
}
}
