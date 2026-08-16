#include "LoadoutManager.h"

#include "LoadoutApplication.h"
#include "LoadoutSerializer.h"
#include "MetaserverClient.h"

#include <algorithm>
#include <chrono>
#include <cctype>
#include <cstdint>
#include <future>
#include <iomanip>
#include <optional>
#include <sstream>
#include <string>
#include <thread>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "../Debug/Debug.h"

using namespace SDK;

namespace
{
    using Clock = std::chrono::steady_clock;
    using TimePoint = Clock::time_point;
    using LoadoutApplication::ApplyResult;
    using LoadoutApplication::PlayerStateInventoryState;

    constexpr auto kRoleConfirmationGrace = std::chrono::seconds(1);
    constexpr auto kPostSpawnRetryWindow = std::chrono::seconds(2);
    constexpr auto kPostSpawnRetryInterval = std::chrono::milliseconds(50);

    std::string TrimAscii(std::string value)
    {
        auto isSpace = [](unsigned char ch) { return std::isspace(ch) != 0; };
        value.erase(value.begin(), std::find_if(value.begin(), value.end(), [&](char ch) {
            return !isSpace(static_cast<unsigned char>(ch));
        }));
        value.erase(std::find_if(value.rbegin(), value.rend(), [&](char ch) {
            return !isSpace(static_cast<unsigned char>(ch));
        }).base(), value.end());
        return value;
    }

    std::string ToLowerAscii(std::string value)
    {
        std::transform(value.begin(), value.end(), value.begin(), [](unsigned char ch) {
            return static_cast<char>(std::tolower(ch));
        });
        return value;
    }

    bool IsCanonicalPlayerId(const std::string& value)
    {
        if (value.size() < 3 || value.size() > 128 || value.rfind("p_", 0) != 0)
            return false;
        return std::all_of(value.begin() + 2, value.end(), [](unsigned char ch) {
            return std::isalnum(ch) != 0 || ch == '_' || ch == '-';
        });
    }

    bool IsValidRoomId(const std::string& value)
    {
        if (value.empty() || value.size() > 128) return false;
        return std::all_of(value.begin(), value.end(), [](unsigned char ch) {
            return std::isalnum(ch) != 0 || ch == '_' || ch == '-';
        });
    }

    bool IsLoopbackTunnelUrl(const std::string& input)
    {
        const std::string url = ToLowerAscii(TrimAscii(input));
        constexpr const char* prefix = "http://";
        if (url.rfind(prefix, 0) != 0) return false;

        const std::size_t authorityBegin = std::char_traits<char>::length(prefix);
        const std::size_t authorityEnd = url.find_first_of("/?#", authorityBegin);
        const std::string authority = url.substr(
            authorityBegin,
            authorityEnd == std::string::npos
                ? std::string::npos
                : authorityEnd - authorityBegin);
        if (authority.empty() || authority.find('@') != std::string::npos) return false;
        if (authorityEnd != std::string::npos && url.substr(authorityEnd) != "/") return false;

        std::string host = authority;
        std::string port;
        if (authority.front() == '[')
        {
            const std::size_t close = authority.find(']');
            if (close == std::string::npos) return false;
            host = authority.substr(0, close + 1);
            if (close + 1 < authority.size())
            {
                if (authority[close + 1] != ':') return false;
                port = authority.substr(close + 2);
            }
        }
        else
        {
            const std::size_t colon = authority.find(':');
            if (colon != std::string::npos)
            {
                if (authority.find(':', colon + 1) != std::string::npos) return false;
                host = authority.substr(0, colon);
                port = authority.substr(colon + 1);
            }
        }

        if (!port.empty())
        {
            if (!std::all_of(port.begin(), port.end(), [](unsigned char ch) {
                return std::isdigit(ch) != 0;
            })) return false;
            unsigned long value = 0;
            try { value = std::stoul(port); }
            catch (...) { return false; }
            if (value == 0 || value > 65535) return false;
        }
        else if (authority.back() == ':')
        {
            return false;
        }

        if (host == "localhost" || host == "[::1]") return true;
        std::vector<unsigned int> octets;
        std::size_t begin = 0;
        while (begin <= host.size())
        {
            const std::size_t dot = host.find('.', begin);
            const std::string part = host.substr(
                begin, dot == std::string::npos ? std::string::npos : dot - begin);
            if (part.empty() || part.size() > 3 ||
                !std::all_of(part.begin(), part.end(), [](unsigned char ch) {
                    return std::isdigit(ch) != 0;
                })) return false;
            const unsigned long value = std::stoul(part);
            if (value > 255) return false;
            octets.push_back(static_cast<unsigned int>(value));
            if (dot == std::string::npos) break;
            begin = dot + 1;
        }
        return octets.size() == 4 && octets.front() == 127;
    }

    bool IsUsableRoleId(const std::string& value)
    {
        if (value.empty() || value == "None" || value.size() > 128) return false;
        return std::none_of(value.begin(), value.end(), [](unsigned char ch) {
            return std::isspace(ch) != 0 || ch == '/' || ch == '\\' || ch == '"' || ch == '\'';
        });
    }

    std::string ResolveCanonicalPlayerId(APBPlayerController* playerController)
    {
        if (!playerController) return {};
        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
        }
        if (!playerState) return {};
        try
        {
            const std::string playerId = TrimAscii(playerState->GetUserIdstr().ToString());
            return IsCanonicalPlayerId(playerId) ? playerId : std::string{};
        }
        catch (...) { return {}; }
    }

    bool IsValidInventory(const FPBInventoryNetworkConfig& inventory)
    {
        if (inventory.CharacterSlots.Num() <= 0 ||
            inventory.CharacterSlots.Num() != inventory.InventoryItems.Num() ||
            inventory.CharacterSlots.Num() > 16)
        {
            return false;
        }

        std::unordered_set<int> slots;
        bool hasConcreteItem = false;
        for (int index = 0; index < inventory.CharacterSlots.Num(); ++index)
        {
            const int slot = static_cast<int>(inventory.CharacterSlots[index]);
            const std::string item = LoadoutSerializer::NameToString(
                inventory.InventoryItems[index]);
            if (slot <= static_cast<int>(EPBCharacterSlotType::None) ||
                slot >= static_cast<int>(EPBCharacterSlotType::EPBCharacterSlotType_MAX) ||
                item.empty() || !slots.insert(slot).second)
            {
                return false;
            }
            hasConcreteItem = hasConcreteItem || item != "None";
        }
        return hasConcreteItem;
    }

    std::size_t HashInventory(const FPBInventoryNetworkConfig& inventory)
    {
        std::ostringstream signature;
        const int count = (std::min)(
            inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
        for (int index = 0; index < count; ++index)
        {
            signature << static_cast<int>(inventory.CharacterSlots[index]) << ':'
                << LoadoutSerializer::NameToString(inventory.InventoryItems[index]) << ';';
        }
        return std::hash<std::string>{}(signature.str());
    }

    std::size_t CombineHash(std::size_t left, std::size_t right)
    {
        return left ^ (right + static_cast<std::size_t>(0x9e3779b9U) +
            (left << 6U) + (left >> 2U));
    }

    std::string HashText(std::size_t value)
    {
        std::ostringstream output;
        output << "0x" << std::hex << value;
        return output.str();
    }

    std::string PlayerTag(const std::string& playerId)
    {
        std::uint32_t hash = 2166136261U;
        for (const unsigned char byte : playerId)
        {
            hash ^= byte;
            hash *= 16777619U;
        }
        std::ostringstream output;
        output << std::hex << std::setw(8) << std::setfill('0') << hash;
        return output.str();
    }

    const char* ApplyResultName(ApplyResult result)
    {
        switch (result)
        {
        case ApplyResult::Pending: return "pending";
        case ApplyResult::Applied: return "applied";
        case ApplyResult::IdentityMismatch: return "identity-mismatch";
        case ApplyResult::Invalid: return "invalid";
        }
        return "unknown";
    }

    const char* SourceName(LoadoutStatePolicy::EffectiveSource source)
    {
        switch (source)
        {
        case LoadoutStatePolicy::EffectiveSource::RuntimeOverride: return "runtime";
        case LoadoutStatePolicy::EffectiveSource::MetaserverBaseline: return "baseline";
        case LoadoutStatePolicy::EffectiveSource::NativeDefault: return "native-default";
        }
        return "unknown";
    }
}

class LoadoutManager::Impl
{
public:
    enum class BaselineScope
    {
        RoomMember,
        CurrentTunnelUser,
    };

    struct ConnectionKey
    {
        std::string PlayerId;
        std::uint64_t Generation = 0;

        bool operator==(const ConnectionKey& other) const
        {
            return PlayerId == other.PlayerId && Generation == other.Generation;
        }
    };

    struct ConnectionKeyHash
    {
        std::size_t operator()(const ConnectionKey& key) const
        {
            return CombineHash(std::hash<std::string>{}(key.PlayerId),
                std::hash<std::uint64_t>{}(key.Generation));
        }
    };

    struct BaselineRole
    {
        nlohmann::json Snapshot;
        FPBInventoryNetworkConfig Inventory;
        std::int64_t Revision = 0;
        std::size_t ContentHash = 0;
    };

    struct RuntimeOverride
    {
        FPBInventoryNetworkConfig Inventory;
        std::size_t ContentHash = 0;
    };

    struct PostSpawnState
    {
        APBCharacter* Pawn = nullptr;
        FPBInventoryNetworkConfig ExpectedInventory;
        std::uint64_t EventGeneration = 0;
        std::size_t ContentHash = 0;
        ApplyResult Result = ApplyResult::Pending;
        bool Active = false;
        bool Applying = false;
        TimePoint Deadline{};
        TimePoint NextAttempt{};
    };

    struct PlayerConnection
    {
        ConnectionKey Key;
        APBPlayerController* Controller = nullptr;
        std::unordered_map<std::string, BaselineRole> Baselines;
        std::unordered_map<std::string, RuntimeOverride> RuntimeOverrides;
        bool FetchInFlight = false;
        bool FetchCompleted = false;
        bool FetchTerminal = false;
        unsigned int FailedAttempts = 0;
        TimePoint NextFetchAt{};
        std::string SelectedRoleId;
        LoadoutStatePolicy::PendingRoleConfirmation Pending;
        PostSpawnState PostSpawn;
    };

    struct FetchTask
    {
        ConnectionKey Key;
        std::uint64_t ServerEpoch = 0;
        std::future<LoadoutMetaserver::PlayerLoadoutsResult> Future;
    };

    struct PendingInventoryBinding
    {
        APBCharacter* Pawn = nullptr;
        TimePoint Deadline{};
        TimePoint NextAttempt{};
    };

    struct EffectiveInventory
    {
        LoadoutStatePolicy::EffectiveSource Source =
            LoadoutStatePolicy::EffectiveSource::NativeDefault;
        FPBInventoryNetworkConfig Inventory;
        std::size_t ContentHash = 0;
        std::int64_t Revision = 0;
        bool HasInventory = false;
    };

    LoadoutBridgeOptions Options;
    bool ServerActive = false;
    std::uint64_t ServerEpoch = 0;
    std::uint64_t NextConnectionGeneration = 1;
    std::string BaseUrl;
    std::string RoomId;
    BaselineScope Scope = BaselineScope::RoomMember;
    UWorld* BoundWorld = nullptr;
    bool HasBoundWorld = false;
    int InternalPreOrderDepth = 0;
    std::unordered_map<ConnectionKey, PlayerConnection, ConnectionKeyHash> Players;
    std::unordered_map<APBPlayerController*, ConnectionKey> ControllerBindings;
    std::vector<FetchTask> FetchTasks;
    std::vector<PendingInventoryBinding> PendingInventoryBindings;
    std::unordered_set<APBCharacter*> DestroyedCharacters;

    void BindCurrentWorld(UWorld* currentWorld)
    {
        if (!HasBoundWorld)
        {
            BoundWorld = currentWorld;
            HasBoundWorld = true;
            return;
        }
        if (currentWorld == BoundWorld) return;

        ++ServerEpoch;
        Players.clear();
        ControllerBindings.clear();
        PendingInventoryBindings.clear();
        DestroyedCharacters.clear();
        BoundWorld = currentWorld;
        ClientLog("[LOADOUT] stage=world-change result=stale-state-discarded");
    }

    PlayerConnection* Find(APBPlayerController* controller)
    {
        const auto binding = ControllerBindings.find(controller);
        if (binding == ControllerBindings.end()) return nullptr;
        return Find(binding->second);
    }

    PlayerConnection* Find(const ConnectionKey& key)
    {
        const auto player = Players.find(key);
        return player == Players.end() ? nullptr : &player->second;
    }

    bool IsCurrent(const FetchTask& task)
    {
        std::optional<LoadoutStatePolicy::ConnectionIdentity> active;
        if (PlayerConnection* player = Find(task.Key))
        {
            active = LoadoutStatePolicy::ConnectionIdentity{
                player->Key.PlayerId, player->Key.Generation, ServerEpoch,
            };
        }
        return LoadoutStatePolicy::IsResponseCurrent(
            active,
            LoadoutStatePolicy::ConnectionIdentity{
                task.Key.PlayerId, task.Key.Generation, task.ServerEpoch,
            });
    }

    void StartFetch(PlayerConnection& player)
    {
        if (!ServerActive || player.FetchInFlight || player.FetchCompleted ||
            player.FetchTerminal)
        {
            return;
        }

        player.FetchInFlight = true;
        const std::string baseUrl = BaseUrl;
        const std::string roomId = RoomId;
        const std::string playerId = player.Key.PlayerId;
        const BaselineScope scope = Scope;
        FetchTask task;
        task.Key = player.Key;
        task.ServerEpoch = ServerEpoch;
        task.Future = std::async(std::launch::async,
            [baseUrl, roomId, playerId, scope]() {
                LoadoutMetaserver::MetaserverClient client(baseUrl);
                if (scope == BaselineScope::CurrentTunnelUser)
                    return client.GetCurrentUserLoadouts();
                return client.GetRoomMemberLoadouts(roomId, playerId);
            });
        FetchTasks.push_back(std::move(task));
    }

    EffectiveInventory ResolveEffective(
        const PlayerConnection& player,
        const std::string& roleId) const
    {
        EffectiveInventory result;
        const auto runtime = player.RuntimeOverrides.find(roleId);
        const auto baseline = player.Baselines.find(roleId);
        result.Source = LoadoutStatePolicy::ChooseEffectiveSource(
            runtime != player.RuntimeOverrides.end(),
            baseline != player.Baselines.end());
        if (runtime != player.RuntimeOverrides.end())
        {
            result.Inventory = runtime->second.Inventory;
            result.ContentHash = runtime->second.ContentHash;
            result.HasInventory = true;
        }
        else if (baseline != player.Baselines.end())
        {
            result.Inventory = baseline->second.Inventory;
            result.ContentHash = baseline->second.ContentHash;
            result.Revision = baseline->second.Revision;
            result.HasInventory = true;
        }
        return result;
    }

    ApplyResult ApplyNativePreOrder(
        const ConnectionKey& key,
        APBPlayerController* expectedController,
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory,
        std::string& outDetail)
    {
        PlayerConnection* player = Find(key);
        if (!player || player->Controller != expectedController)
        {
            outDetail = "connection-stale";
            return ApplyResult::Invalid;
        }

        struct InternalPreOrderGuard
        {
            int& Depth;
            explicit InternalPreOrderGuard(int& depth) : Depth(depth) { ++Depth; }
            ~InternalPreOrderGuard() { --Depth; }
        } guard(InternalPreOrderDepth);
        return LoadoutApplication::PreSpawnApplyInventory(
            roleId, inventory, expectedController, outDetail);
    }

    bool PrepareEffectiveRole(PlayerConnection& player, const std::string& roleId)
    {
        const EffectiveInventory effective = ResolveEffective(player, roleId);
        if (!effective.HasInventory)
            return true; // Preserve the native default/current pre-ordering.

        const ConnectionKey key = player.Key;
        APBPlayerController* const controller = player.Controller;
        std::string detail;
        const ApplyResult result = ApplyNativePreOrder(
            key, controller, roleId, effective.Inventory, detail);
        ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
            " generation=" + std::to_string(key.Generation) +
            " stage=confirm-preorder role=" + roleId +
            " source=" + SourceName(effective.Source) +
            " revision=" + std::to_string(effective.Revision) +
            " inventory_hash=" + HashText(effective.ContentHash) +
            " result=" + ApplyResultName(result) + " detail=" + detail);
        return result == ApplyResult::Applied;
    }

    void ApplyLateBaselineForNextLife(
        const ConnectionKey& key,
        const std::string& roleId)
    {
        PlayerConnection* player = Find(key);
        if (!player || !player->Controller || player->SelectedRoleId != roleId ||
            player->RuntimeOverrides.contains(roleId))
        {
            return;
        }
        const auto baseline = player->Baselines.find(roleId);
        if (baseline == player->Baselines.end()) return;

        const FPBInventoryNetworkConfig inventory = baseline->second.Inventory;
        const std::int64_t revision = baseline->second.Revision;
        const std::size_t hash = baseline->second.ContentHash;
        APBPlayerController* const controller = player->Controller;
        std::string detail;
        const ApplyResult result = ApplyNativePreOrder(
            key, controller, roleId, inventory, detail);
        ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
            " generation=" + std::to_string(key.Generation) +
            " stage=late-baseline-next-life role=" + roleId +
            " revision=" + std::to_string(revision) +
            " inventory_hash=" + HashText(hash) +
            " result=" + ApplyResultName(result) + " detail=" + detail);
    }

    void HandleFetchResult(
        const ConnectionKey& key,
        const LoadoutMetaserver::PlayerLoadoutsResult& result)
    {
        PlayerConnection* player = Find(key);
        if (!player) return;
        player->FetchInFlight = false;

        if (result.Succeeded() && result.Value)
        {
            if (Scope == BaselineScope::CurrentTunnelUser &&
                result.Value->PlayerId != key.PlayerId)
            {
                player->FetchTerminal = true;
                ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
                    " generation=" + std::to_string(key.Generation) +
                    " stage=baseline-fetch result=identity-mismatch");
                return;
            }

            player->Baselines.clear();
            std::size_t aggregateHash = 0;
            for (const auto& role : result.Value->Loadouts)
            {
                if (!IsUsableRoleId(role.RoleId) || !role.NormalizedRole.is_object())
                    continue;

                nlohmann::json normalizedRole = role.NormalizedRole;
                normalizedRole["roleId"] = role.RoleId;
                nlohmann::json snapshot = {
                    {"schemaVersion", 2},
                    {"source", Scope == BaselineScope::CurrentTunnelUser
                        ? "metaserver-current-user-pve"
                        : "metaserver-room-host"},
                    {"roles", nlohmann::json::array({std::move(normalizedRole)})},
                };

                std::string detail;
                FPBInventoryNetworkConfig inventory{};
                if (!LoadoutApplication::TryBuildRoleInventory(
                    snapshot, role.RoleId, inventory, detail) ||
                    !IsValidInventory(inventory))
                {
                    ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
                        " stage=baseline-validate role=" + role.RoleId +
                        " result=rejected reason=" + detail);
                    continue;
                }

                BaselineRole baseline;
                baseline.Revision = role.Revision;
                baseline.ContentHash = CombineHash(
                    std::hash<std::string>{}(snapshot.dump()), HashInventory(inventory));
                baseline.Inventory = inventory;
                baseline.Snapshot = std::move(snapshot);
                aggregateHash = CombineHash(aggregateHash, baseline.ContentHash);
                player->Baselines.emplace(role.RoleId, std::move(baseline));
            }

            player->FetchCompleted = true;
            player->FetchTerminal = false;
            ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
                " generation=" + std::to_string(key.Generation) +
                " stage=baseline-fetch result=ready roles=" +
                std::to_string(player->Baselines.size()) +
                " set_hash=" + HashText(aggregateHash));

            const std::string selectedRole = player->SelectedRoleId;
            if (!selectedRole.empty() && !player->RuntimeOverrides.contains(selectedRole))
                ApplyLateBaselineForNextLife(key, selectedRole);
            return;
        }

        ++player->FailedAttempts;
        if (result.IsRetryable())
        {
            player->NextFetchAt = Clock::now() +
                LoadoutStatePolicy::RetryDelay(player->FailedAttempts);
        }
        else
        {
            player->FetchTerminal = true;
        }
        ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
            " generation=" + std::to_string(key.Generation) +
            " stage=baseline-fetch result=failed status=" +
            std::to_string(result.Http.StatusCode) +
            " retry=" + (result.IsRetryable() ? "1" : "0") +
            " reason=" + result.Http.ErrorMessage);
    }

    void ConsumeFetchTasks()
    {
        for (auto task = FetchTasks.begin(); task != FetchTasks.end();)
        {
            if (task->Future.wait_for(std::chrono::milliseconds(0)) !=
                std::future_status::ready)
            {
                ++task;
                continue;
            }
            try
            {
                auto result = task->Future.get();
                if (IsCurrent(*task)) HandleFetchResult(task->Key, result);
            }
            catch (const std::exception& exception)
            {
                PlayerConnection* player = Find(task->Key);
                if (player && IsCurrent(*task))
                {
                    player->FetchInFlight = false;
                    ++player->FailedAttempts;
                    player->NextFetchAt = Clock::now() +
                        LoadoutStatePolicy::RetryDelay(player->FailedAttempts);
                    ClientLog("[LOADOUT] player=" + PlayerTag(task->Key.PlayerId) +
                        " stage=baseline-fetch result=worker-exception reason=" +
                        exception.what());
                }
            }
            catch (...)
            {
                PlayerConnection* player = Find(task->Key);
                if (player && IsCurrent(*task))
                {
                    player->FetchInFlight = false;
                    ++player->FailedAttempts;
                    player->NextFetchAt = Clock::now() +
                        LoadoutStatePolicy::RetryDelay(player->FailedAttempts);
                }
            }
            task = FetchTasks.erase(task);
        }
    }

    void RecordRuntimeOverride(
        PlayerConnection& player,
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory)
    {
        RuntimeOverride runtime;
        runtime.Inventory = inventory;
        runtime.ContentHash = HashInventory(inventory);
        player.RuntimeOverrides[roleId] = std::move(runtime);
        if (player.SelectedRoleId == roleId)
            player.PostSpawn.Active = false;
    }

    void TryPostSpawnApply(const ConnectionKey& key, TimePoint now)
    {
        PlayerConnection* player = Find(key);
        if (!player || !player->Controller) return;
        APBPlayerController* const expectedController = player->Controller;
        PostSpawnState& post = player->PostSpawn;
        if (!post.Active || !post.Pawn || now < post.NextAttempt) return;

        const std::string roleId = player->SelectedRoleId;
        const auto baseline = player->Baselines.find(roleId);
        if (baseline == player->Baselines.end())
        {
            post.Active = false;
            post.Result = ApplyResult::Invalid;
            return;
        }

        APBCharacter* const pawn = post.Pawn;
        const std::uint64_t eventGeneration = post.EventGeneration;
        const std::size_t contentHash = post.ContentHash;
        const TimePoint deadline = post.Deadline;
        const FPBInventoryNetworkConfig expectedInventory = post.ExpectedInventory;
        const nlohmann::json snapshot = baseline->second.Snapshot;
        // Applying weapon parts/skins synchronously raises K2_InventorySpawned
        // again on this build. Mark the in-flight Pawn before crossing into
        // native code so that the nested event cannot replace this generation.
        post.Applying = true;
        const ApplyResult result = LoadoutApplication::PostSpawnApply(
            pawn, snapshot, expectedInventory);

        player = Find(key);
        if (!player || player->Controller != expectedController) return;
        PostSpawnState& current = player->PostSpawn;
        if (!current.Active || current.Pawn != pawn ||
            current.EventGeneration != eventGeneration ||
            current.ContentHash != contentHash)
        {
            return;
        }

        current.Applying = false;
        current.Result = result;
        if (result == ApplyResult::Pending && now < deadline)
        {
            current.NextAttempt = now + kPostSpawnRetryInterval;
            return;
        }
        current.Active = false;
        ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
            " generation=" + std::to_string(key.Generation) +
            " stage=detail-overlay role=" + roleId +
            " event_generation=" + std::to_string(eventGeneration) +
            " detail_hash=" + HashText(contentHash) +
            " result=" + ApplyResultName(result));
    }

    bool TryBindInventorySpawn(APBCharacter* character, TimePoint now)
    {
        if (!character || DestroyedCharacters.contains(character)) return true;
        APBPlayerController* const controller =
            LoadoutApplication::FindPlayerControllerForCharacter(character);
        if (DestroyedCharacters.contains(character)) return true;
        if (!controller) return false;

        PlayerConnection* player = Find(controller);
        if (!player) return true;
        if (player->PostSpawn.Active && player->PostSpawn.Applying &&
            player->PostSpawn.Pawn == character)
        {
            return true;
        }
        const ConnectionKey key = player->Key;
        APBPlayerController* const expectedController = player->Controller;
        const std::string liveRole =
            LoadoutApplication::ResolveLiveCharacterRoleId(character);
        player = Find(key);
        if (!player || player->Controller != expectedController) return true;
        if (!IsUsableRoleId(liveRole)) return false;
        if (player->SelectedRoleId.empty() || player->SelectedRoleId != liveRole)
        {
            ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
                " generation=" + std::to_string(key.Generation) +
                " stage=inventory-spawn result=stale-role selected=" +
                player->SelectedRoleId + " live=" + liveRole);
            return true;
        }

        const auto baseline = player->Baselines.find(liveRole);
        if (baseline == player->Baselines.end()) return true;
        const EffectiveInventory effective = ResolveEffective(*player, liveRole);
        if (!effective.HasInventory) return true;

        const PlayerStateInventoryState equipping =
            LoadoutApplication::InspectPlayerStateInventory(
                controller, LoadoutSerializer::NameFromString(liveRole),
                effective.Inventory, true);
        if (equipping == PlayerStateInventoryState::Mismatch)
        {
            // ClientRefreshRoleEquippingInventory is a client RPC on this
            // build; the client receives the correct six slots while the
            // authoritative PlayerState mirror can retain native defaults.
            // Keep the mismatch observable, but let the post-spawn overlay
            // validate the possessed role and each concrete weapon actor ID.
            ClientLog("[LOADOUT] player=" + PlayerTag(key.PlayerId) +
                " generation=" + std::to_string(key.Generation) +
                " stage=inventory-spawn role=" + liveRole +
                " source=" + SourceName(effective.Source) +
                " inventory_hash=" + HashText(effective.ContentHash) +
                " result=equipping-advisory-mismatch");
        }

        PostSpawnState& post = player->PostSpawn;
        ++post.EventGeneration;
        post.Pawn = character;
        post.ExpectedInventory = effective.Inventory;
        post.ContentHash = CombineHash(
            baseline->second.ContentHash, effective.ContentHash);
        post.Result = ApplyResult::Pending;
        post.Active = true;
        post.Applying = false;
        post.Deadline = now + kPostSpawnRetryWindow;
        post.NextAttempt = now;
        TryPostSpawnApply(key, now);
        return true;
    }
};

LoadoutManager::LoadoutManager()
    : impl_(std::make_unique<Impl>())
{
}

LoadoutManager::~LoadoutManager()
{
    StopServer();
}

LoadoutManager::LoadoutManager(LoadoutManager&&) noexcept = default;
LoadoutManager& LoadoutManager::operator=(LoadoutManager&&) noexcept = default;

bool LoadoutManager::StartServer(
    std::string baseUrl,
    std::string roomId,
    LoadoutBridgeOptions options)
{
    if (!impl_) return false;
    StopServer();
    baseUrl = TrimAscii(std::move(baseUrl));
    roomId = TrimAscii(std::move(roomId));
    if (!IsLoopbackTunnelUrl(baseUrl) || !IsValidRoomId(roomId))
    {
        ClientLog("[LOADOUT] stage=bridge-start result=invalid-tunnel-or-room");
        return false;
    }

    ++impl_->ServerEpoch;
    impl_->Options = options;
    impl_->BaseUrl = std::move(baseUrl);
    impl_->RoomId = std::move(roomId);
    impl_->Scope = Impl::BaselineScope::RoomMember;
    impl_->BoundWorld = nullptr;
    impl_->HasBoundWorld = false;
    impl_->ServerActive = true;
    ClientLog("[LOADOUT] stage=bridge-start result=ready baseline=" +
        std::string(impl_->Options.BaselineOverride ? "1" : "0") +
        " preorder=" + (impl_->Options.PreOrderIntercept ? "1" : "0") +
        " confirm_deferral=" + (impl_->Options.ConfirmDeferral ? "1" : "0") +
        " detail_overlay=" + (impl_->Options.SpawnApplication ? "1" : "0"));
    return true;
}

bool LoadoutManager::StartLocalPveServer(
    std::string baseUrl,
    LoadoutBridgeOptions options)
{
    if (!impl_) return false;
    StopServer();
    baseUrl = TrimAscii(std::move(baseUrl));
    if (!IsLoopbackTunnelUrl(baseUrl))
    {
        ClientLog("[LOADOUT] stage=bridge-start result=invalid-local-pve-tunnel");
        return false;
    }

    ++impl_->ServerEpoch;
    impl_->Options = options;
    impl_->BaseUrl = std::move(baseUrl);
    impl_->RoomId.clear();
    impl_->Scope = Impl::BaselineScope::CurrentTunnelUser;
    impl_->BoundWorld = nullptr;
    impl_->HasBoundWorld = false;
    impl_->ServerActive = true;
    ClientLog("[LOADOUT] stage=bridge-start result=ready scope=current-tunnel-user-pve baseline=" +
        std::string(impl_->Options.BaselineOverride ? "1" : "0") +
        " preorder=" + (impl_->Options.PreOrderIntercept ? "1" : "0") +
        " confirm_deferral=" + (impl_->Options.ConfirmDeferral ? "1" : "0") +
        " detail_overlay=" + (impl_->Options.SpawnApplication ? "1" : "0"));
    return true;
}

void LoadoutManager::StopServer()
{
    if (!impl_) return;
    impl_->ServerActive = false;
    ++impl_->ServerEpoch;
    impl_->Players.clear();
    impl_->ControllerBindings.clear();
    impl_->PendingInventoryBindings.clear();
    impl_->DestroyedCharacters.clear();
    impl_->BoundWorld = nullptr;
    impl_->HasBoundWorld = false;
    impl_->InternalPreOrderDepth = 0;

    for (auto& task : impl_->FetchTasks)
    {
        if (!task.Future.valid()) continue;
        try { (void)task.Future.get(); }
        catch (...) {}
    }
    impl_->FetchTasks.clear();
}

void LoadoutManager::OnPlayerConnected(APBPlayerController* playerController)
{
    if (!impl_ || !impl_->ServerActive || !playerController) return;
    impl_->BindCurrentWorld(UWorld::GetWorld());
    if (impl_->ControllerBindings.contains(playerController)) return;
    try
    {
        if (!playerController->HasAuthority()) return;
    }
    catch (...) { return; }

    const std::string playerId = ResolveCanonicalPlayerId(playerController);
    if (playerId.empty())
    {
        ClientLog("[LOADOUT] stage=player-connect result=noncanonical-player-id");
        return;
    }

    Impl::ConnectionKey key{playerId, impl_->NextConnectionGeneration++};
    Impl::PlayerConnection connection;
    connection.Key = key;
    connection.Controller = playerController;
    connection.NextFetchAt = Clock::now();
    impl_->Players.emplace(key, std::move(connection));
    impl_->ControllerBindings.emplace(playerController, key);

    Impl::PlayerConnection* player = impl_->Find(key);
    if (!player) return;
    ClientLog("[LOADOUT] player=" + PlayerTag(playerId) +
        " generation=" + std::to_string(key.Generation) +
        " stage=player-connect result=bound");
    if (impl_->Options.BaselineOverride)
        impl_->StartFetch(*player);
    else
        player->FetchTerminal = true;
}

void LoadoutManager::OnPlayerDisconnected(APBPlayerController* playerController)
{
    if (!impl_ || !playerController) return;
    APBCharacter* const character =
        LoadoutApplication::GetControllerCharacter(playerController);
    if (character)
    {
        std::erase_if(impl_->PendingInventoryBindings,
            [character](const auto& pending) { return pending.Pawn == character; });
    }
    const auto binding = impl_->ControllerBindings.find(playerController);
    if (binding == impl_->ControllerBindings.end()) return;
    impl_->Players.erase(binding->second);
    impl_->ControllerBindings.erase(binding);
}

void LoadoutManager::OnActorDestroyed(AActor* actor)
{
    if (!impl_ || !actor) return;
    if (actor->IsA(APBPlayerController::StaticClass()))
    {
        OnPlayerDisconnected(static_cast<APBPlayerController*>(actor));
        return;
    }
    if (!actor->IsA(APBCharacter::StaticClass()) ||
        !impl_->Options.SpawnApplication)
    {
        return;
    }

    auto* const character = static_cast<APBCharacter*>(actor);
    impl_->DestroyedCharacters.insert(character);
    std::erase_if(impl_->PendingInventoryBindings,
        [character](const auto& pending) { return pending.Pawn == character; });
    for (auto& entry : impl_->Players)
    {
        if (entry.second.PostSpawn.Pawn == character)
            entry.second.PostSpawn = Impl::PostSpawnState{};
    }
}

LoadoutRoleConfirmDecision LoadoutManager::BeginRoleConfirmation(
    APBPlayerController* playerController,
    const FName& roleId)
{
    if (!impl_ || !impl_->ServerActive || !playerController)
        return LoadoutRoleConfirmDecision::Fallback;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player) return LoadoutRoleConfirmDecision::Fallback;
    const std::string role = LoadoutSerializer::NameToString(roleId);
    if (!IsUsableRoleId(role)) return LoadoutRoleConfirmDecision::Fallback;

    const bool hasEffective = player->RuntimeOverrides.contains(role) ||
        player->Baselines.contains(role);
    LoadoutRoleConfirmDecision decision = hasEffective
        ? LoadoutRoleConfirmDecision::Ready
        : LoadoutRoleConfirmDecision::Fallback;
    if (impl_->Options.ConfirmDeferral)
    {
        decision = LoadoutStatePolicy::BeginRoleConfirmation(
            player->Pending, role, hasEffective,
            player->FetchCompleted || player->FetchTerminal,
            Clock::now(),
            std::chrono::duration_cast<std::chrono::milliseconds>(
                kRoleConfirmationGrace));
    }
    if (decision == LoadoutRoleConfirmDecision::Deferred)
        return decision;

    const Impl::ConnectionKey key = player->Key;
    if (decision == LoadoutRoleConfirmDecision::Ready &&
        !impl_->PrepareEffectiveRole(*player, role))
    {
        if (Impl::PlayerConnection* current = impl_->Find(key))
            current->Pending.ReplayDecision = LoadoutRoleConfirmDecision::Fallback;
        return LoadoutRoleConfirmDecision::Fallback;
    }
    return decision;
}

void LoadoutManager::CommitRoleConfirmationAfterOriginal(
    APBPlayerController* playerController,
    const FName& roleId)
{
    if (!impl_ || !impl_->ServerActive) return;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player) return;
    const std::string role = LoadoutSerializer::NameToString(roleId);
    if (!IsUsableRoleId(role)) return;
    player->SelectedRoleId = role;
    LoadoutStatePolicy::CompleteRoleConfirmation(player->Pending);
}

bool LoadoutManager::OnExternalPreOrderInventory(
    APBPlayerController* playerController,
    const FName& roleId,
    const FPBInventoryNetworkConfig& inventory)
{
    if (!impl_ || !impl_->ServerActive || !impl_->Options.PreOrderIntercept ||
        impl_->InternalPreOrderDepth > 0 || !playerController ||
        !IsValidInventory(inventory))
    {
        return false;
    }

    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player) return false;
    const std::string role = LoadoutSerializer::NameToString(roleId);
    if (!IsUsableRoleId(role)) return false;

    const PlayerStateInventoryState state =
        LoadoutApplication::InspectPlayerStateInventory(
            playerController, roleId, inventory, false);
    if (state != PlayerStateInventoryState::Match)
    {
        ClientLog("[LOADOUT] player=" + PlayerTag(player->Key.PlayerId) +
            " generation=" + std::to_string(player->Key.Generation) +
            " stage=external-preorder role=" + role +
            " inventory_hash=" + HashText(HashInventory(inventory)) +
            " result=native-rejected-or-unpublished");
        return false;
    }

    impl_->RecordRuntimeOverride(*player, role, inventory);
    ClientLog("[LOADOUT] player=" + PlayerTag(player->Key.PlayerId) +
        " generation=" + std::to_string(player->Key.Generation) +
        " stage=external-preorder role=" + role +
        " inventory_hash=" + HashText(HashInventory(inventory)) +
        " result=accepted");
    return true;
}

bool LoadoutManager::DeferExternalPreOrderInventoryIfLeaseConflict(
    APBPlayerController* playerController,
    const FName& roleId,
    const FPBInventoryNetworkConfig& inventory)
{
    (void)playerController;
    (void)roleId;
    (void)inventory;
    return false;
}

bool LoadoutManager::IsInternalPreOrderInProgress() const
{
    return impl_ && impl_->InternalPreOrderDepth > 0;
}

bool LoadoutManager::IsCharacterTombstoned(APBCharacter* character) const
{
    return impl_ && impl_->Options.SpawnApplication && character &&
        impl_->DestroyedCharacters.contains(character);
}

void LoadoutManager::OnInventorySpawned(APBCharacter* character)
{
    if (!impl_ || !impl_->ServerActive || !impl_->Options.SpawnApplication ||
        !character)
    {
        return;
    }
    if (impl_->DestroyedCharacters.contains(character))
    {
        APBPlayerController* const controller =
            LoadoutApplication::FindPlayerControllerForCharacter(character);
        if (!controller || controller->bActorIsBeingDestroyed ||
            character->bActorIsBeingDestroyed ||
            LoadoutApplication::GetControllerCharacter(controller) != character)
        {
            return;
        }
        impl_->DestroyedCharacters.erase(character);
    }
    else if (character->bActorIsBeingDestroyed)
    {
        return;
    }

    const TimePoint now = Clock::now();
    if (impl_->TryBindInventorySpawn(character, now)) return;
    const auto existing = std::find_if(
        impl_->PendingInventoryBindings.begin(),
        impl_->PendingInventoryBindings.end(),
        [character](const auto& pending) { return pending.Pawn == character; });
    if (existing == impl_->PendingInventoryBindings.end())
    {
        impl_->PendingInventoryBindings.push_back({
            character, now + kPostSpawnRetryWindow,
            now + kPostSpawnRetryInterval,
        });
    }
}

bool LoadoutManager::CanReleaseRoleSpawn(APBPlayerController* playerController)
{
    (void)playerController;
    return true;
}

void LoadoutManager::BeginSpawnDispatch(APBPlayerController* playerController)
{
    (void)playerController;
}

void LoadoutManager::CompleteSpawnDispatch(APBPlayerController* playerController)
{
    (void)playerController;
}

void LoadoutManager::FinalizeSpawnRequest(APBPlayerController* playerController)
{
    (void)playerController;
}

void LoadoutManager::AbandonSpawnRequest(APBPlayerController* playerController)
{
    (void)playerController;
}

void LoadoutManager::TickServer(float deltaSeconds)
{
    (void)deltaSeconds;
    if (!impl_ || !impl_->ServerActive) return;
    const TimePoint now = Clock::now();
    impl_->BindCurrentWorld(UWorld::GetWorld());
    impl_->ConsumeFetchTasks();

    std::vector<Impl::PendingInventoryBinding> pending =
        std::move(impl_->PendingInventoryBindings);
    impl_->PendingInventoryBindings.clear();
    for (auto& binding : pending)
    {
        bool keep = now < binding.NextAttempt;
        if (!keep)
        {
            const bool bound = impl_->TryBindInventorySpawn(binding.Pawn, now);
            keep = !bound && now < binding.Deadline &&
                !impl_->DestroyedCharacters.contains(binding.Pawn);
            if (keep) binding.NextAttempt = now + kPostSpawnRetryInterval;
        }
        if (keep)
        {
            const bool alreadyQueued = std::any_of(
                impl_->PendingInventoryBindings.begin(),
                impl_->PendingInventoryBindings.end(),
                [&binding](const auto& live) { return live.Pawn == binding.Pawn; });
            if (!alreadyQueued)
                impl_->PendingInventoryBindings.push_back(std::move(binding));
        }
    }

    std::vector<Impl::ConnectionKey> playerKeys;
    playerKeys.reserve(impl_->Players.size());
    for (const auto& entry : impl_->Players) playerKeys.push_back(entry.first);
    for (const Impl::ConnectionKey& key : playerKeys)
    {
        Impl::PlayerConnection* player = impl_->Find(key);
        if (!player) continue;
        if (!player->FetchInFlight && !player->FetchCompleted &&
            !player->FetchTerminal && now >= player->NextFetchAt)
        {
            impl_->StartFetch(*player);
        }
        impl_->TryPostSpawnApply(key, now);
    }

    struct Replay
    {
        Impl::ConnectionKey Key;
        APBPlayerController* Controller = nullptr;
        std::string RoleId;
    };
    std::vector<Replay> replays;
    for (auto& entry : impl_->Players)
    {
        Impl::PlayerConnection& player = entry.second;
        const bool hasEffective = player.RuntimeOverrides.contains(player.Pending.RoleId) ||
            player.Baselines.contains(player.Pending.RoleId);
        const auto decision = LoadoutStatePolicy::PollRoleConfirmation(
            player.Pending, hasEffective,
            player.FetchCompleted || player.FetchTerminal, now);
        if (decision)
            replays.push_back({player.Key, player.Controller, player.Pending.RoleId});
    }

    for (const Replay& replay : replays)
    {
        Impl::PlayerConnection* current = impl_->Find(replay.Key);
        if (!replay.Controller || !current || current->Controller != replay.Controller ||
            !current->Pending.Replaying || current->Pending.RoleId != replay.RoleId)
        {
            continue;
        }
        try
        {
            replay.Controller->ServerConfirmRoleSelection(
                LoadoutSerializer::NameFromString(replay.RoleId));
        }
        catch (...)
        {
            ClientLog("[LOADOUT] player=" + PlayerTag(replay.Key.PlayerId) +
                " generation=" + std::to_string(replay.Key.Generation) +
                " stage=confirm-replay result=exception");
        }
        if (Impl::PlayerConnection* player = impl_->Find(replay.Key);
            player && player->Pending.Replaying)
        {
            LoadoutStatePolicy::CompleteRoleConfirmation(player->Pending);
        }
    }
}
