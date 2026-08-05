#include "LoadoutManager.h"

#include "LoadoutApplication.h"
#include "LoadoutSerializer.h"
#include "MetaserverClient.h"

#include <algorithm>
#include <chrono>
#include <cctype>
#include <cstdint>
#include <future>
#include <functional>
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

    constexpr auto kRoleConfirmationGrace = std::chrono::seconds(1);
    constexpr auto kPreSpawnRetryInterval = std::chrono::milliseconds(50);
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
        if (value.size() < 3 || value.size() > 128 || value.rfind("p_", 0) != 0) return false;
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
            authorityEnd == std::string::npos ? std::string::npos : authorityEnd - authorityBegin);
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
        catch (...)
        {
            return {};
        }
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
        for (int i = 0; i < inventory.CharacterSlots.Num(); ++i)
        {
            const int slot = static_cast<int>(inventory.CharacterSlots[i]);
            const std::string item = LoadoutSerializer::NameToString(inventory.InventoryItems[i]);
            if (slot <= static_cast<int>(EPBCharacterSlotType::None) ||
                slot >= static_cast<int>(EPBCharacterSlotType::EPBCharacterSlotType_MAX) ||
                item.empty() || item == "None" || !slots.insert(slot).second)
            {
                return false;
            }
        }
        return true;
    }

    std::size_t HashInventory(const FPBInventoryNetworkConfig& inventory)
    {
        std::ostringstream signature;
        const int count = (std::min)(
            inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
        for (int i = 0; i < count; ++i)
        {
            signature << static_cast<int>(inventory.CharacterSlots[i]) << ':'
                      << LoadoutSerializer::NameToString(inventory.InventoryItems[i]) << ';';
        }
        return std::hash<std::string>{}(signature.str());
    }

    std::size_t CombineHash(std::size_t left, std::size_t right)
    {
        return left ^ (right + static_cast<std::size_t>(0x9e3779b9U) + (left << 6U) + (left >> 2U));
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

}

class LoadoutManager::Impl
{
public:
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
        std::int64_t Revision = 0;
        std::size_t ContentHash = 0;
    };

    struct RuntimeOverride
    {
        std::size_t ContentHash = 0;
        FPBInventoryNetworkConfig Inventory;
    };

    struct SpawnLease
    {
        ConnectionKey Owner;
        TimePoint Deadline{};
        bool SeedReady = false;
        std::uint64_t RequestGeneration = 0;
        APBCharacter* PriorPawn = nullptr;
        APBCharacter* ExpectedPawn = nullptr;
        APBCharacter* ObservedInPlacePawn = nullptr;
        bool DispatchInProgress = false;
        bool SpawnFinalized = false;
        TimePoint InventoryDeadline{};
    };

    struct PostSpawnState
    {
        APBCharacter* Pawn = nullptr;
        std::uint64_t SpawnGeneration = 0;
        std::size_t ContentHash = 0;
        ApplyResult Result = ApplyResult::Pending;
        bool Active = false;
        TimePoint Deadline{};
        TimePoint NextAttempt{};
    };

    struct PreSpawnState
    {
        bool Active = false;
        bool SpawnGate = false;
        std::string RoleId;
        TimePoint Deadline{};
        TimePoint NextAttempt{};
        std::string LastDetail;
    };

    struct PlayerConnection
    {
        ConnectionKey Key;
        APBPlayerController* Controller = nullptr;
        std::unordered_map<std::string, BaselineRole> Baselines;
        std::unordered_map<std::string, RuntimeOverride> RuntimeOverrides;
        std::unordered_set<std::string> ConfirmedRoles;

        bool FetchInFlight = false;
        bool FetchCompleted = false;
        bool FetchTerminal = false;
        unsigned int FailedAttempts = 0;
        TimePoint NextFetchAt{};

        std::string SelectedRoleId;
        std::uint64_t NextSpawnRequestGeneration = 1;
        std::uint64_t ActiveSpawnRequestGeneration = 0;
        std::string ActiveSpawnRoleId;
        LoadoutStatePolicy::PendingRoleConfirmation Pending;
        PreSpawnState PreSpawn;
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

    struct DeferredExternalPreOrder
    {
        ConnectionKey Key;
        std::string RoleId;
        FPBInventoryNetworkConfig Inventory;
        TimePoint Deadline{};
        TimePoint NextAttempt{};
        bool GraceStarted = false;
        bool SlowRetryLogged = false;
    };

    bool ServerActive = false;
    std::uint64_t ServerEpoch = 0;
    std::uint64_t NextConnectionGeneration = 1;
    std::string BaseUrl;
    std::string RoomId;
    UWorld* BoundWorld = nullptr;
    bool HasBoundWorld = false;
    int InternalPreOrderDepth = 0;

    std::unordered_map<ConnectionKey, PlayerConnection, ConnectionKeyHash> Players;
    std::unordered_map<APBPlayerController*, ConnectionKey> ControllerBindings;
    std::unordered_map<std::string, SpawnLease> SpawnLeases;
    std::vector<FetchTask> FetchTasks;
    std::vector<PendingInventoryBinding> PendingInventoryBindings;
    std::vector<DeferredExternalPreOrder> DeferredExternalPreOrders;
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
        SpawnLeases.clear();
        PendingInventoryBindings.clear();
        DeferredExternalPreOrders.clear();
        DestroyedCharacters.clear();
        BoundWorld = currentWorld;
        ClientLog("[LOADOUT] World changed; discarded stale player/loadout bindings");
    }

    PlayerConnection* Find(APBPlayerController* playerController)
    {
        auto binding = ControllerBindings.find(playerController);
        if (binding == ControllerBindings.end()) return nullptr;
        auto player = Players.find(binding->second);
        return player == Players.end() ? nullptr : &player->second;
    }

    PlayerConnection* Find(const ConnectionKey& key)
    {
        auto player = Players.find(key);
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
        if (!ServerActive || player.FetchInFlight || player.FetchCompleted || player.FetchTerminal)
            return;

        player.FetchInFlight = true;
        const std::string baseUrl = BaseUrl;
        const std::string roomId = RoomId;
        const std::string playerId = player.Key.PlayerId;

        FetchTask task;
        task.Key = player.Key;
        task.ServerEpoch = ServerEpoch;
        task.Future = std::async(std::launch::async,
            [baseUrl, roomId, playerId]() {
                LoadoutMetaserver::MetaserverClient client(baseUrl);
                return client.GetRoomMemberLoadouts(roomId, playerId);
            });
        FetchTasks.push_back(std::move(task));
    }

    ApplyResult ApplyBaselinePreSpawn(
        PlayerConnection& player,
        const std::string& roleId,
        std::string& outDetail)
    {
        if (!player.Controller || player.RuntimeOverrides.find(roleId) != player.RuntimeOverrides.end())
        {
            outDetail = "runtime-override-active";
            return ApplyResult::Invalid;
        }

        auto baseline = player.Baselines.find(roleId);
        if (baseline == player.Baselines.end())
        {
            outDetail = "baseline-role-missing";
            return ApplyResult::Invalid;
        }

        struct InternalPreOrderGuard
        {
            int& Depth;
            explicit InternalPreOrderGuard(int& depth) : Depth(depth) { ++Depth; }
            ~InternalPreOrderGuard() { --Depth; }
        } guard(InternalPreOrderDepth);
        return LoadoutApplication::PreSpawnApplyRole(
            baseline->second.Snapshot, roleId, player.Controller, outDetail);
    }

    ApplyResult ApplyRuntimePreSpawn(
        PlayerConnection& player,
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory,
        std::string& outDetail)
    {
        if (!player.Controller)
        {
            outDetail = "controller-missing";
            return ApplyResult::Invalid;
        }

        struct InternalPreOrderGuard
        {
            int& Depth;
            explicit InternalPreOrderGuard(int& depth) : Depth(depth) { ++Depth; }
            ~InternalPreOrderGuard() { --Depth; }
        } guard(InternalPreOrderDepth);
        return LoadoutApplication::PreSpawnApplyInventory(
            roleId, inventory, player.Controller, outDetail);
    }

    ApplyResult ApplyNativeDefaultPreSpawn(
        PlayerConnection& player,
        const std::string& roleId,
        std::string& outDetail)
    {
        const ConnectionKey key = player.Key;
        APBPlayerController* const expectedController = player.Controller;
        if (!expectedController)
        {
            outDetail = "controller-missing";
            return ApplyResult::Invalid;
        }

        // Loading a soft default asset can pump engine callbacks. Build the
        // plain inventory first, then revalidate the connection generation
        // before crossing the authoritative preorder RPC boundary.
        FPBInventoryNetworkConfig inventory{};
        const ApplyResult built = LoadoutApplication::TryBuildNativeDefaultInventory(
            roleId, inventory, outDetail);
        if (built != ApplyResult::Applied) return built;

        PlayerConnection* current = Find(key);
        if (!current || current->Controller != expectedController)
        {
            outDetail = "connection-changed-during-default-load";
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

    ApplyResult ApplyEffectivePreSpawn(
        PlayerConnection& player,
        const std::string& roleId,
        std::string& outDetail)
    {
        auto runtime = player.RuntimeOverrides.find(roleId);
        const auto source = LoadoutStatePolicy::ChooseEffectiveSource(
            runtime != player.RuntimeOverrides.end(),
            player.Baselines.find(roleId) != player.Baselines.end());
        if (source == LoadoutStatePolicy::EffectiveSource::RuntimeOverride)
        {
            // ServerPreOrderInventory can synchronously erase the connection;
            // keep the inventory backing storage independent of Players.
            FPBInventoryNetworkConfig inventory = runtime->second.Inventory;
            return ApplyRuntimePreSpawn(player, roleId, inventory, outDetail);
        }
        if (source == LoadoutStatePolicy::EffectiveSource::MetaserverBaseline)
            return ApplyBaselinePreSpawn(player, roleId, outDetail);
        return ApplyNativeDefaultPreSpawn(player, roleId, outDetail);
    }

    void ReleaseSpawnLeases(const ConnectionKey& key)
    {
        for (auto lease = SpawnLeases.begin(); lease != SpawnLeases.end();)
        {
            if (!(lease->second.Owner == key))
            {
                ++lease;
                continue;
            }
            if (PlayerConnection* player = Find(key);
                player && player->ActiveSpawnRequestGeneration ==
                    lease->second.RequestGeneration &&
                player->ActiveSpawnRoleId == lease->first)
            {
                player->ActiveSpawnRequestGeneration = 0;
                player->ActiveSpawnRoleId.clear();
            }
            lease = SpawnLeases.erase(lease);
        }
    }

    void ReleaseSpawnLease(
        const ConnectionKey& key,
        const std::string& roleId,
        std::uint64_t requestGeneration = 0)
    {
        auto lease = SpawnLeases.find(roleId);
        if (lease != SpawnLeases.end() && lease->second.Owner == key &&
            (requestGeneration == 0 ||
                lease->second.RequestGeneration == requestGeneration))
        {
            if (PlayerConnection* player = Find(key);
                player && player->ActiveSpawnRequestGeneration ==
                    lease->second.RequestGeneration &&
                player->ActiveSpawnRoleId == roleId)
            {
                player->ActiveSpawnRequestGeneration = 0;
                player->ActiveSpawnRoleId.clear();
            }
            SpawnLeases.erase(lease);
        }
    }

    bool HasDeferredExternalPreOrder(
        const ConnectionKey& key,
        const std::string& roleId) const
    {
        return std::any_of(
            DeferredExternalPreOrders.begin(),
            DeferredExternalPreOrders.end(),
            [&key, &roleId](const auto& pending) {
                return pending.Key == key && pending.RoleId == roleId;
            });
    }

    void QueueDeferredExternalPreOrder(
        const ConnectionKey& key,
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory,
        TimePoint now)
    {
        auto existing = std::find_if(
            DeferredExternalPreOrders.begin(),
            DeferredExternalPreOrders.end(),
            [&key, &roleId](const auto& pending) {
                return pending.Key == key && pending.RoleId == roleId;
            });
        DeferredExternalPreOrder deferred{
            key, roleId, inventory,
            TimePoint{}, now, false, false,
        };
        if (existing == DeferredExternalPreOrders.end())
            DeferredExternalPreOrders.push_back(std::move(deferred));
        else
            *existing = std::move(deferred);
    }

    void RecordRuntimeOverride(
        PlayerConnection& player,
        const std::string& roleId,
        const FPBInventoryNetworkConfig& inventory)
    {
        RuntimeOverride runtime;
        runtime.ContentHash = HashInventory(inventory);
        runtime.Inventory = inventory;
        player.RuntimeOverrides[roleId] = std::move(runtime);
        if (player.PreSpawn.Active && player.PreSpawn.RoleId == roleId)
        {
            player.PreSpawn.Active = false;
            player.PreSpawn.SpawnGate = false;
        }
        if (player.SelectedRoleId == roleId)
            player.PostSpawn.Active = false;
    }

    void ReplayDeferredExternalPreOrders(TimePoint now)
    {
        // Move the queue out before crossing into ProcessEvent. Logout or a
        // nested external submission may mutate the live queue synchronously.
        std::vector<DeferredExternalPreOrder> queued =
            std::move(DeferredExternalPreOrders);
        DeferredExternalPreOrders.clear();
        for (auto& pending : queued)
        {
            PlayerConnection* player = Find(pending.Key);
            if (!player || !player->Controller)
                continue;
            if (now < pending.NextAttempt)
            {
                DeferredExternalPreOrders.push_back(std::move(pending));
                continue;
            }

            auto lease = SpawnLeases.find(pending.RoleId);
            if (lease != SpawnLeases.end() && !lease->second.SeedReady &&
                lease->second.Deadline <= now)
            {
                const ConnectionKey expiredOwner = lease->second.Owner;
                ReleaseSpawnLease(
                    expiredOwner, pending.RoleId,
                    lease->second.RequestGeneration);
                lease = SpawnLeases.end();
            }
            if (lease != SpawnLeases.end())
            {
                pending.NextAttempt = now + kPreSpawnRetryInterval;
                DeferredExternalPreOrders.push_back(std::move(pending));
                continue;
            }

            // Waiting behind somebody else's role lease must not consume the
            // verification grace. Start it only when this exact inventory can
            // actually be asserted into FieldMod.
            if (!pending.GraceStarted)
            {
                pending.GraceStarted = true;
                pending.Deadline = now + kRoleConfirmationGrace;
            }

            const ConnectionKey key = pending.Key;
            const std::string roleId = pending.RoleId;
            FPBInventoryNetworkConfig inventory = pending.Inventory;

            std::string detail;
            const ApplyResult result =
                ApplyRuntimePreSpawn(*player, roleId, inventory, detail);
            player = Find(key);
            if (!player) continue;
            if (result == ApplyResult::Applied)
            {
                RecordRuntimeOverride(*player, roleId, inventory);
                ClientLog("[LOADOUT] Replayed deferred FieldMod override player=" +
                    key.PlayerId + " role=" + roleId);
                continue;
            }
            if (result != ApplyResult::Applied)
            {
                const bool inGrace = now < pending.Deadline;
                pending.NextAttempt = now + (inGrace
                    ? kPreSpawnRetryInterval
                    : std::chrono::milliseconds(500));
                if (!inGrace && !pending.SlowRetryLogged)
                {
                    pending.SlowRetryLogged = true;
                    ClientLog("[LOADOUT] FieldMod override still unverified; "
                        "keeping runtime choice fail-closed player=" +
                        key.PlayerId + " role=" + roleId + " " + detail);
                }
                if (!HasDeferredExternalPreOrder(key, roleId))
                    DeferredExternalPreOrders.push_back(std::move(pending));
                continue;
            }
        }
    }

    void BeginPreSpawnSeed(PlayerConnection& player, const std::string& roleId, TimePoint now)
    {
        if (player.RuntimeOverrides.find(roleId) != player.RuntimeOverrides.end()) return;
        player.PreSpawn.Active = true;
        player.PreSpawn.SpawnGate = false;
        player.PreSpawn.RoleId = roleId;
        player.PreSpawn.Deadline = now + kRoleConfirmationGrace;
        player.PreSpawn.NextAttempt = now;
        player.PreSpawn.LastDetail.clear();
    }

    void TryPreSpawnSeed(const ConnectionKey& key, TimePoint now)
    {
        PlayerConnection* player = Find(key);
        if (!player || !player->Controller) return;
        APBPlayerController* const expectedController = player->Controller;
        auto& seed = player->PreSpawn;
        if (!seed.Active || now < seed.NextAttempt) return;
        if (seed.SpawnGate) return; // Driven just-in-time by CanReleaseRoleSpawn.
        const std::string roleId = seed.RoleId;
        auto lease = SpawnLeases.find(roleId);
        if (lease != SpawnLeases.end() && !lease->second.SeedReady &&
            lease->second.Deadline <= now)
        {
            const ConnectionKey expiredOwner = lease->second.Owner;
            ReleaseSpawnLease(
                expiredOwner, roleId, lease->second.RequestGeneration);
            lease = SpawnLeases.end();
        }
        if (lease != SpawnLeases.end())
        {
            // Once any spawn has reserved this world+role cache, eager
            // next-life seeding must wait for its matching InventorySpawned.
            // This includes the same owner: a baseline arriving after the
            // one-second confirmation timeout must not alter the current
            // already-released native-default spawn.
            seed.NextAttempt = now + kPreSpawnRetryInterval;
            return;
        }
        if (player->RuntimeOverrides.find(roleId) != player->RuntimeOverrides.end())
        {
            seed.Active = false;
            seed.SpawnGate = false;
            return;
        }

        const TimePoint deadline = seed.Deadline;
        std::string detail;
        const ApplyResult result = ApplyBaselinePreSpawn(*player, roleId, detail);

        // ServerPreOrderInventory is a synchronous engine boundary and may
        // trigger Logout/Destroy. Never retain map references across it.
        player = Find(key);
        if (!player || player->Controller != expectedController) return;
        auto& currentSeed = player->PreSpawn;
        if (!currentSeed.Active || currentSeed.SpawnGate ||
            currentSeed.RoleId != roleId)
        {
            return;
        }
        currentSeed.LastDetail = std::move(detail);
        if (result == ApplyResult::Pending && now < deadline)
        {
            currentSeed.NextAttempt = now + kPreSpawnRetryInterval;
            return;
        }

        currentSeed.Active = false;
        currentSeed.SpawnGate = false;
        ClientLog("[LOADOUT] Pre-spawn baseline player=" + key.PlayerId +
            " role=" + roleId + " result=" + ApplyResultName(result) +
            " " + currentSeed.LastDetail);
    }

    void HandleFetchResult(
        const ConnectionKey& key,
        const LoadoutMetaserver::PlayerLoadoutsResult& result)
    {
        PlayerConnection* player = Find(key);
        if (!player) return; // Disconnected/replaced connection: stale result.

        player->FetchInFlight = false;
        if (result.Succeeded() && result.Value)
        {
            player->Baselines.clear();
            for (const auto& role : result.Value->Loadouts)
            {
                if (!IsUsableRoleId(role.RoleId) || !role.NormalizedRole.is_object()) continue;

                nlohmann::json normalizedRole = role.NormalizedRole;
                normalizedRole["roleId"] = role.RoleId;
                nlohmann::json snapshot = {
                    {"schemaVersion", 2},
                    {"source", "metaserver-room-host"},
                    {"roles", nlohmann::json::array({std::move(normalizedRole)})},
                };

                std::string detail;
                FPBInventoryNetworkConfig inventory{};
                if (!LoadoutApplication::TryBuildRoleInventory(snapshot, role.RoleId, inventory, detail))
                {
                    ClientLog("[LOADOUT] Rejected invalid role player=" + key.PlayerId +
                        " role=" + role.RoleId + " reason=" + detail);
                    continue;
                }

                BaselineRole baseline;
                baseline.Revision = role.Revision;
                baseline.ContentHash = std::hash<std::string>{}(snapshot.dump());
                baseline.Snapshot = std::move(snapshot);
                player->Baselines.emplace(role.RoleId, std::move(baseline));
            }

            player->FetchCompleted = true;
            player->FetchTerminal = false;
            ClientLog("[LOADOUT] Loaded room baseline player=" + key.PlayerId +
                " roles=" + std::to_string(player->Baselines.size()) +
                (result.Http.RequestId.empty() ? "" : " request=" + result.Http.RequestId));

            // A loadout that arrives after the confirmation grace is only
            // written to the next-spawn FieldMod cache; it never replaces the
            // current pawn or a player-selected runtime override.
            if (!player->SelectedRoleId.empty() &&
                player->RuntimeOverrides.find(player->SelectedRoleId) == player->RuntimeOverrides.end())
            {
                BeginPreSpawnSeed(*player, player->SelectedRoleId, Clock::now());
                TryPreSpawnSeed(key, Clock::now());
            }
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

        std::ostringstream message;
        message << "[LOADOUT] Room baseline fetch failed player=" << key.PlayerId
                << " status=" << result.Http.StatusCode
                << " error=" << result.Http.ErrorMessage
                << " retry=" << (result.IsRetryable() ? "yes" : "no");
        if (!result.Http.RequestId.empty()) message << " request=" << result.Http.RequestId;
        ClientLog(message.str());
    }

    void ConsumeFetchTasks()
    {
        for (auto task = FetchTasks.begin(); task != FetchTasks.end();)
        {
            if (task->Future.wait_for(std::chrono::milliseconds(0)) != std::future_status::ready)
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
                    ClientLog("[LOADOUT] Fetch worker exception player=" + task->Key.PlayerId +
                        " error=" + exception.what());
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

    void TryPostSpawnApply(const ConnectionKey& key, TimePoint now)
    {
        PlayerConnection* player = Find(key);
        if (!player || !player->Controller) return;
        APBPlayerController* const expectedController = player->Controller;
        auto& post = player->PostSpawn;
        if (!post.Active || !post.Pawn || now < post.NextAttempt) return;

        const std::string roleId = player->SelectedRoleId;
        auto baseline = player->Baselines.find(roleId);
        if (baseline == player->Baselines.end())
        {
            post.Active = false;
            post.Result = ApplyResult::Invalid;
            return;
        }

        const APBCharacter* const expectedPawn = post.Pawn;
        const std::uint64_t expectedSpawnGeneration = post.SpawnGeneration;
        const std::size_t expectedContentHash = post.ContentHash;
        const TimePoint deadline = post.Deadline;
        nlohmann::json snapshot = baseline->second.Snapshot;
        const bool runtimeOverrideActive =
            player->RuntimeOverrides.find(roleId) != player->RuntimeOverrides.end();

        const ApplyResult result = LoadoutApplication::PostSpawnApply(
            const_cast<APBCharacter*>(expectedPawn), snapshot, runtimeOverrideActive);

        // OnRep/refresh/ForceNetUpdate can synchronously tear down this
        // connection or enqueue a newer inventory generation on the same Pawn.
        player = Find(key);
        if (!player || player->Controller != expectedController) return;
        auto& currentPost = player->PostSpawn;
        if (!currentPost.Active || currentPost.Pawn != expectedPawn ||
            currentPost.SpawnGeneration != expectedSpawnGeneration ||
            currentPost.ContentHash != expectedContentHash)
        {
            return;
        }
        currentPost.Result = result;
        if (result == ApplyResult::Pending && now < deadline)
        {
            currentPost.NextAttempt = now + kPostSpawnRetryInterval;
            return;
        }

        currentPost.Active = false;
        ClientLog("[LOADOUT] Post-spawn apply player=" + key.PlayerId +
            " role=" + roleId +
            " result=" + ApplyResultName(result));
    }

    bool TryBindInventorySpawn(APBCharacter* character, TimePoint now)
    {
        if (!character) return true;
        if (DestroyedCharacters.contains(character)) return true;
        APBPlayerController* playerController =
            LoadoutApplication::FindPlayerControllerForCharacter(character);
        if (DestroyedCharacters.contains(character)) return true;
        if (!playerController) return false;

        PlayerConnection* player = Find(playerController);
        // The Pawn is already associated with a controller but that connection
        // is no longer managed (logout, rejected id, or bridge disabled).
        // Retrying it cannot establish a valid generation binding.
        if (!player) return true;
        const ConnectionKey key = player->Key;
        APBPlayerController* const expectedController = player->Controller;

        const std::string liveRole = LoadoutApplication::ResolveLiveCharacterRoleId(character);
        if (DestroyedCharacters.contains(character)) return true;
        player = Find(key);
        if (!player || player->Controller != expectedController) return true;
        if (!IsUsableRoleId(liveRole)) return false;
        if (!player->SelectedRoleId.empty() && player->SelectedRoleId != liveRole)
        {
            // A late K2 event from the previous role must not roll the
            // connection back or release a newer role's spawn lease.
            ClientLog("[LOADOUT] Ignored stale InventorySpawned player=" +
                player->Key.PlayerId + " selected=" + player->SelectedRoleId +
                " live=" + liveRole);
            return true;
        }
        player->SelectedRoleId = liveRole;
        auto lease = SpawnLeases.find(liveRole);
        if (lease != SpawnLeases.end() && lease->second.Owner == key)
        {
            const bool generationMatches =
                lease->second.RequestGeneration != 0 &&
                player->ActiveSpawnRequestGeneration ==
                    lease->second.RequestGeneration &&
                player->ActiveSpawnRoleId == liveRole;
            if (!generationMatches || !lease->second.SeedReady)
            {
                ClientLog("[LOADOUT] Ignored stale InventorySpawned generation player=" +
                    key.PlayerId + " role=" + liveRole);
                return true;
            }

            if (lease->second.ExpectedPawn &&
                lease->second.ExpectedPawn != character)
            {
                ClientLog("[LOADOUT] Ignored non-matching InventorySpawned pawn player=" +
                    key.PlayerId + " role=" + liveRole);
                return true;
            }
            if (!lease->second.ExpectedPawn)
            {
                if (!lease->second.DispatchInProgress)
                {
                    ClientLog("[LOADOUT] Ignored pre-dispatch InventorySpawned player=" +
                        key.PlayerId + " role=" + liveRole);
                    return true;
                }
                if (lease->second.PriorPawn == character)
                {
                    // An in-place QuickRespawn and an old delayed K2 share the
                    // same pointer. Confirm it against the post-dispatch alive
                    // state before consuming this request generation.
                    lease->second.ObservedInPlacePawn = character;
                    return true;
                }
                lease->second.ExpectedPawn = character;
            }

            // The exact owner + request generation + Pawn consumed this
            // world's role cache. A peer may now seed the same role.
            ReleaseSpawnLease(
                key, liveRole, lease->second.RequestGeneration);
        }

        auto baseline = player->Baselines.find(liveRole);
        if (baseline == player->Baselines.end())
        {
            // A timeout/default spawn is deliberately not modified when its
            // baseline arrives later; the fetch handler seeds the next life.
            return true;
        }

        std::size_t contentHash = baseline->second.ContentHash;
        auto runtime = player->RuntimeOverrides.find(liveRole);
        if (runtime != player->RuntimeOverrides.end())
            contentHash = CombineHash(contentHash, runtime->second.ContentHash);

        auto& post = player->PostSpawn;
        // K2_InventorySpawned is the authoritative inventory generation
        // signal. It may fire again on the same Pawn during a restart.
        ++post.SpawnGeneration;
        post.Pawn = character;
        post.ContentHash = contentHash;
        post.Result = ApplyResult::Pending;
        post.Active = true;
        post.Deadline = now + kPostSpawnRetryWindow;
        post.NextAttempt = now;
        TryPostSpawnApply(key, now);
        return true;
    }

    void ExpireFinalizedSpawnLeases(TimePoint now)
    {
        struct Candidate
        {
            std::string RoleId;
            ConnectionKey Key;
            std::uint64_t RequestGeneration = 0;
            APBCharacter* ExpectedPawn = nullptr;
            APBPlayerController* Controller = nullptr;
        };
        std::vector<Candidate> candidates;
        for (const auto& entry : SpawnLeases)
        {
            const SpawnLease& lease = entry.second;
            if (!lease.SeedReady || !lease.SpawnFinalized ||
                lease.DispatchInProgress || now < lease.InventoryDeadline)
            {
                continue;
            }
            PlayerConnection* player = Find(lease.Owner);
            candidates.push_back({
                entry.first, lease.Owner, lease.RequestGeneration,
                lease.ExpectedPawn, player ? player->Controller : nullptr,
            });
        }

        for (const Candidate& candidate : candidates)
        {
            PlayerConnection* player = Find(candidate.Key);
            auto lease = SpawnLeases.find(candidate.RoleId);
            if (!player || !candidate.Controller ||
                player->Controller != candidate.Controller ||
                player->SelectedRoleId != candidate.RoleId ||
                player->ActiveSpawnRoleId != candidate.RoleId ||
                player->ActiveSpawnRequestGeneration != candidate.RequestGeneration ||
                lease == SpawnLeases.end() ||
                !(lease->second.Owner == candidate.Key) ||
                lease->second.RequestGeneration != candidate.RequestGeneration ||
                lease->second.ExpectedPawn != candidate.ExpectedPawn)
            {
                continue;
            }

            APBCharacter* const currentPawn =
                LoadoutApplication::GetControllerCharacter(candidate.Controller);
            const std::string currentRoleId = currentPawn
                ? LoadoutApplication::ResolveLiveCharacterRoleId(currentPawn)
                : std::string{};
            const bool currentPawnAlive = currentPawn &&
                !DestroyedCharacters.contains(currentPawn) &&
                LoadoutApplication::IsCharacterAlive(currentPawn);

            player = Find(candidate.Key);
            lease = SpawnLeases.find(candidate.RoleId);
            if (!player || player->Controller != candidate.Controller ||
                player->ActiveSpawnRoleId != candidate.RoleId ||
                player->ActiveSpawnRequestGeneration != candidate.RequestGeneration ||
                lease == SpawnLeases.end() ||
                !(lease->second.Owner == candidate.Key) ||
                lease->second.RequestGeneration != candidate.RequestGeneration)
            {
                continue;
            }

            const bool canUseFallback = currentPawnAlive &&
                currentPawn == candidate.ExpectedPawn &&
                currentRoleId == candidate.RoleId;
            ReleaseSpawnLease(
                candidate.Key, candidate.RoleId,
                candidate.RequestGeneration);
            if (canUseFallback)
            {
                ClientLog("[LOADOUT] InventorySpawned fallback after finalized Pawn player=" +
                    candidate.Key.PlayerId + " role=" + candidate.RoleId);
                (void)TryBindInventorySpawn(currentPawn, now);
            }
        }
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

bool LoadoutManager::StartServer(std::string baseUrl, std::string roomId)
{
    if (!impl_) return false;
    StopServer();

    baseUrl = TrimAscii(std::move(baseUrl));
    roomId = TrimAscii(std::move(roomId));
    if (!IsLoopbackTunnelUrl(baseUrl) || !IsValidRoomId(roomId))
    {
        ClientLog("[LOADOUT] Server bridge disabled: invalid loopback MetaTunnel URL or room id");
        return false;
    }

    ++impl_->ServerEpoch;
    impl_->BaseUrl = std::move(baseUrl);
    impl_->RoomId = std::move(roomId);
    // The first authoritative TickServer call binds the UWorld. MainThread is
    // an injected bootstrap thread and must not inspect UObject state.
    impl_->BoundWorld = nullptr;
    impl_->HasBoundWorld = false;
    impl_->ServerActive = true;
    ClientLog("[LOADOUT] Server bridge started room=" + impl_->RoomId +
        " tunnel=" + impl_->BaseUrl);
    return true;
}

void LoadoutManager::StopServer()
{
    if (!impl_) return;
    impl_->ServerActive = false;
    ++impl_->ServerEpoch;
    impl_->Players.clear();
    impl_->ControllerBindings.clear();
    impl_->SpawnLeases.clear();
    impl_->PendingInventoryBindings.clear();
    impl_->DeferredExternalPreOrders.clear();
    impl_->DestroyedCharacters.clear();
    impl_->BoundWorld = nullptr;
    impl_->HasBoundWorld = false;
    impl_->InternalPreOrderDepth = 0;

    // Explicit shutdown runs outside the loader lock and HTTP requests have
    // bounded timeouts. Join here so no worker can execute DLL code after the
    // module has been unloaded.
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
    if (!impl_ || !impl_->ServerActive || !playerController)
        return;

    // PostLogin can be the first callback in a newly travelled world, before
    // TickServer observes the generation. Bind/reset first so this connection
    // is not inserted into the stale map and then discarded one frame later.
    impl_->BindCurrentWorld(UWorld::GetWorld());
    if (impl_->ControllerBindings.find(playerController) != impl_->ControllerBindings.end())
    {
        return;
    }

    try
    {
        if (!playerController->HasAuthority()) return;
    }
    catch (...) { return; }

    const std::string playerId = ResolveCanonicalPlayerId(playerController);
    if (playerId.empty())
    {
        ClientLog("[LOADOUT] Player loadout disabled: GetUserIdstr is not a canonical p_ id");
        return;
    }

    Impl::ConnectionKey key{playerId, impl_->NextConnectionGeneration++};
    Impl::PlayerConnection connection;
    connection.Key = key;
    connection.Controller = playerController;
    connection.NextFetchAt = Clock::now();

    impl_->Players.emplace(key, std::move(connection));
    impl_->ControllerBindings.emplace(playerController, key);
    if (auto* player = impl_->Find(key)) impl_->StartFetch(*player);
}

void LoadoutManager::OnPlayerDisconnected(APBPlayerController* playerController)
{
    if (!impl_ || !playerController) return;
    APBCharacter* character = LoadoutApplication::GetControllerCharacter(playerController);
    if (character)
    {
        std::erase_if(impl_->PendingInventoryBindings, [character](const auto& pending) {
            return pending.Pawn == character;
        });
    }
    auto binding = impl_->ControllerBindings.find(playerController);
    if (binding == impl_->ControllerBindings.end()) return;
    const Impl::ConnectionKey disconnectedKey = binding->second;
    impl_->ReleaseSpawnLeases(binding->second);
    impl_->Players.erase(binding->second);
    impl_->ControllerBindings.erase(binding);
    std::erase_if(impl_->DeferredExternalPreOrders,
        [&disconnectedKey](const auto& pending) {
            return pending.Key == disconnectedKey;
        });
}

void LoadoutManager::OnActorDestroyed(AActor* actor)
{
    if (!impl_ || !actor) return;

    if (actor->IsA(APBPlayerController::StaticClass()))
    {
        OnPlayerDisconnected(static_cast<APBPlayerController*>(actor));
        return;
    }

    if (!actor->IsA(APBCharacter::StaticClass())) return;
    auto* character = static_cast<APBCharacter*>(actor);
    impl_->DestroyedCharacters.insert(character);
    struct LeaseToRelease
    {
        std::string RoleId;
        Impl::ConnectionKey Owner;
        std::uint64_t RequestGeneration = 0;
    };
    std::vector<LeaseToRelease> leasesToRelease;
    for (const auto& entry : impl_->SpawnLeases)
    {
        if (entry.second.ExpectedPawn == character)
        {
            leasesToRelease.push_back({
                entry.first, entry.second.Owner,
                entry.second.RequestGeneration,
            });
        }
    }
    for (const auto& lease : leasesToRelease)
    {
        impl_->ReleaseSpawnLease(
            lease.Owner, lease.RoleId, lease.RequestGeneration);
    }
    std::erase_if(impl_->PendingInventoryBindings, [character](const auto& pending) {
        return pending.Pawn == character;
    });
    for (auto& entry : impl_->Players)
    {
        auto& post = entry.second.PostSpawn;
        if (post.Pawn == character)
            post = Impl::PostSpawnState{};
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

    return LoadoutStatePolicy::BeginRoleConfirmation(
        player->Pending,
        role,
        player->Baselines.find(role) != player->Baselines.end(),
        player->FetchCompleted || player->FetchTerminal,
        Clock::now(),
        std::chrono::duration_cast<std::chrono::milliseconds>(kRoleConfirmationGrace));
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

    if (player->ActiveSpawnRequestGeneration != 0 &&
        !player->ActiveSpawnRoleId.empty() &&
        player->ActiveSpawnRoleId != role)
    {
        const Impl::ConnectionKey key = player->Key;
        const std::string activeRole = player->ActiveSpawnRoleId;
        const std::uint64_t activeGeneration =
            player->ActiveSpawnRequestGeneration;
        impl_->ReleaseSpawnLease(key, activeRole, activeGeneration);
        player = impl_->Find(key);
        if (!player || player->Controller != playerController) return;
    }

    player->SelectedRoleId = role;
    player->ConfirmedRoles.insert(role);
    LoadoutStatePolicy::CompleteRoleConfirmation(player->Pending);
    // The world FieldMod cache is keyed only by role, not by controller.
    // CanReleaseRoleSpawn performs the authoritative just-in-time write under
    // a per-role lease immediately before every managed spawn/re-spawn.
}

bool LoadoutManager::OnExternalPreOrderInventory(
    APBPlayerController* playerController,
    const FName& roleId,
    const FPBInventoryNetworkConfig& inventory)
{
    if (!impl_ || !impl_->ServerActive || impl_->InternalPreOrderDepth > 0 ||
        !playerController || !IsValidInventory(inventory))
    {
        return false;
    }

    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player) return false;

    const std::string role = LoadoutSerializer::NameToString(roleId);
    if (!IsUsableRoleId(role)) return false;

    // Before the first confirmation the native selection flow can seed its
    // defaults, so do not classify that as an in-match override. Once this
    // connection has played any role, a FieldMod submission for a *new* role
    // must still outrank its metaserver baseline (A -> B is the common case).
    if (player->ConfirmedRoles.empty()) return false;

    if (LoadoutApplication::InspectFieldModCache(roleId, inventory) ==
        LoadoutApplication::FieldModCacheState::Match)
    {
        impl_->RecordRuntimeOverride(*player, role, inventory);
        ClientLog("[LOADOUT] Runtime FieldMod override player=" + player->Key.PlayerId +
            " role=" + role);
        return true;
    }

    // The native RPC is void and some builds publish the FieldMod map one or
    // more frames later. Reassert and verify before promoting the submission
    // to the highest-priority runtime source; CanReleaseRoleSpawn remains
    // blocked while this pending record exists.
    impl_->QueueDeferredExternalPreOrder(
        player->Key, role, inventory, Clock::now());
    ClientLog("[LOADOUT] Verifying external FieldMod override player=" +
        player->Key.PlayerId + " role=" + role);
    return true;
}

bool LoadoutManager::DeferExternalPreOrderInventoryIfLeaseConflict(
    APBPlayerController* playerController,
    const FName& roleId,
    const FPBInventoryNetworkConfig& inventory)
{
    if (!impl_ || !impl_->ServerActive || impl_->InternalPreOrderDepth > 0 ||
        !playerController || !IsValidInventory(inventory))
    {
        return false;
    }

    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player) return false;
    const std::string role = LoadoutSerializer::NameToString(roleId);
    if (!IsUsableRoleId(role) || player->ConfirmedRoles.empty()) return false;

    const TimePoint now = Clock::now();
    auto lease = impl_->SpawnLeases.find(role);
    if (lease != impl_->SpawnLeases.end() && !lease->second.SeedReady &&
        lease->second.Deadline <= now)
    {
        const Impl::ConnectionKey expiredOwner = lease->second.Owner;
        impl_->ReleaseSpawnLease(
            expiredOwner, role, lease->second.RequestGeneration);
        lease = impl_->SpawnLeases.end();
    }
    if (lease == impl_->SpawnLeases.end()) return false;

    impl_->QueueDeferredExternalPreOrder(
        player->Key, role, inventory, now);

    ClientLog("[LOADOUT] Deferred FieldMod write behind active spawn lease player=" +
        player->Key.PlayerId + " role=" + role);
    return true;
}

bool LoadoutManager::IsInternalPreOrderInProgress() const
{
    return impl_ && impl_->InternalPreOrderDepth > 0;
}

bool LoadoutManager::IsCharacterTombstoned(APBCharacter* character) const
{
    return impl_ && character && impl_->DestroyedCharacters.contains(character);
}

void LoadoutManager::OnInventorySpawned(APBCharacter* character)
{
    if (!impl_ || !impl_->ServerActive || !character) return;
    if (impl_->DestroyedCharacters.contains(character))
    {
        // The native K2 body may synchronously destroy its own Pawn. Preserve
        // that tombstone. Clear it only when this address has demonstrably
        // been recycled into the currently bound live character.
        APBPlayerController* const liveController =
            LoadoutApplication::FindPlayerControllerForCharacter(character);
        if (!liveController || liveController->bActorIsBeingDestroyed ||
            LoadoutApplication::GetControllerCharacter(liveController) != character ||
            character->bActorIsBeingDestroyed)
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

    auto existing = std::find_if(
        impl_->PendingInventoryBindings.begin(), impl_->PendingInventoryBindings.end(),
        [character](const auto& pending) { return pending.Pawn == character; });
    if (existing == impl_->PendingInventoryBindings.end())
    {
        impl_->PendingInventoryBindings.push_back({
            character, now + kPostSpawnRetryWindow, now + kPostSpawnRetryInterval,
        });
    }
}

bool LoadoutManager::CanReleaseRoleSpawn(APBPlayerController* playerController)
{
    if (!impl_ || !impl_->ServerActive || !playerController) return true;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player || player->SelectedRoleId.empty()) return true;

    const TimePoint now = Clock::now();
    const Impl::ConnectionKey key = player->Key;
    APBPlayerController* const expectedController = player->Controller;
    const std::string roleId = player->SelectedRoleId;
    std::uint64_t requestGeneration = 0;
    const auto findCurrentPlayer = [&]() -> Impl::PlayerConnection* {
        Impl::PlayerConnection* current = impl_->Find(key);
        if (!current || current->Controller != expectedController ||
            current->SelectedRoleId != roleId)
        {
            return nullptr;
        }
        return current;
    };
    const auto findOwnedLease = [&]() {
        auto current = impl_->SpawnLeases.find(roleId);
        if (current == impl_->SpawnLeases.end() ||
            !(current->second.Owner == key) ||
            (requestGeneration != 0 &&
                current->second.RequestGeneration != requestGeneration))
        {
            return impl_->SpawnLeases.end();
        }
        return current;
    };
    const auto abandonStaleCall = [&]() {
        impl_->ReleaseSpawnLease(key, roleId, requestGeneration);
        return false;
    };

    if (impl_->HasDeferredExternalPreOrder(key, roleId))
        return false;
    auto lease = impl_->SpawnLeases.find(roleId);
    if (lease != impl_->SpawnLeases.end() && !lease->second.SeedReady &&
        lease->second.Deadline <= now)
    {
        const Impl::ConnectionKey expiredOwner = lease->second.Owner;
        impl_->ReleaseSpawnLease(
            expiredOwner, roleId, lease->second.RequestGeneration);
        lease = impl_->SpawnLeases.end();
    }
    if (lease != impl_->SpawnLeases.end() && !(lease->second.Owner == key))
    {
        // UPBFieldModManager exposes only a world+role cache. Serialize players
        // sharing a role until K2_InventorySpawned consumes the current seed.
        return false;
    }

    if (lease != impl_->SpawnLeases.end())
        requestGeneration = lease->second.RequestGeneration;
    if (lease != impl_->SpawnLeases.end() &&
        (player->ActiveSpawnRequestGeneration != requestGeneration ||
            player->ActiveSpawnRoleId != roleId))
    {
        impl_->ReleaseSpawnLease(key, roleId, requestGeneration);
        lease = impl_->SpawnLeases.end();
        requestGeneration = 0;
    }

    if (lease != impl_->SpawnLeases.end() && lease->second.SeedReady)
    {
        // PlayerCanRestart and nested restart paths can query the same spawn
        // more than once. Once this owner's exact seed is verified, keep the
        // lease and allow repeated checks without rewriting the shared cache.
        lease->second.Deadline = now + kPostSpawnRetryWindow;
        return true;
    }

    // Reserve before the first write, not only after verification. Otherwise
    // two same-role players can both observe an empty lease while FieldMod is
    // returning not-yet-visible and overwrite each other's pending seed.
    if (lease == impl_->SpawnLeases.end())
    {
        const std::uint64_t newRequestGeneration =
            player->NextSpawnRequestGeneration++;
        player->ActiveSpawnRequestGeneration = newRequestGeneration;
        player->ActiveSpawnRoleId = roleId;
        impl_->SpawnLeases[roleId] = Impl::SpawnLease{
            key, now + kPostSpawnRetryWindow, false,
            newRequestGeneration, nullptr, nullptr, nullptr, false, false, TimePoint{},
        };
        lease = impl_->SpawnLeases.find(roleId);
    }
    // Pin all post-SDK validation in this stack frame to the exact lease; a
    // nested same-owner/same-role request must not be mistaken for it.
    if (lease != impl_->SpawnLeases.end())
        requestGeneration = lease->second.RequestGeneration;

    if (!player->PreSpawn.Active || !player->PreSpawn.SpawnGate ||
        player->PreSpawn.RoleId != roleId)
    {
        player->PreSpawn.Active = true;
        player->PreSpawn.SpawnGate = true;
        player->PreSpawn.RoleId = roleId;
        player->PreSpawn.Deadline = now + kRoleConfirmationGrace;
        player->PreSpawn.NextAttempt = now;
        player->PreSpawn.LastDetail.clear();
    }
    if (now < player->PreSpawn.NextAttempt)
    {
        impl_->SpawnLeases[roleId].Deadline = now + kPostSpawnRetryWindow;
        return false;
    }

    const auto effectiveSource = LoadoutStatePolicy::ChooseEffectiveSource(
        player->RuntimeOverrides.find(roleId) != player->RuntimeOverrides.end(),
        player->Baselines.find(roleId) != player->Baselines.end());
    std::string detail;
    const ApplyResult result = impl_->ApplyEffectivePreSpawn(
        *player, roleId, detail);

    // Every Apply* may issue ProcessEvent or load an asset. Logout, actor
    // destruction, or a nested role transition can invalidate both the player
    // node and the lease while that call is on the stack.
    player = findCurrentPlayer();
    lease = findOwnedLease();
    if (!player || lease == impl_->SpawnLeases.end())
        return abandonStaleCall();
    if (!player->PreSpawn.Active || !player->PreSpawn.SpawnGate ||
        player->PreSpawn.RoleId != roleId)
    {
        return abandonStaleCall();
    }
    player->PreSpawn.LastDetail = detail;
    if (result == ApplyResult::Pending && now < player->PreSpawn.Deadline)
    {
        lease->second.Deadline = now + kPostSpawnRetryWindow;
        player->PreSpawn.NextAttempt = now + kPreSpawnRetryInterval;
        return false;
    }

    if (result == ApplyResult::Pending)
    {
        const auto fieldModCacheAbsent = [](const std::string& detail) {
            return detail.find("fieldmod=missing") != std::string::npos ||
                detail.find("fieldmod=role-missing") != std::string::npos;
        };
        // A missing FieldMod subsystem means there is no shared role cache to
        // leak. After the bounded grace period the engine may safely create
        // its native default actors; retain the lease through the real
        // InventorySpawned event so a late subsystem cannot be overwritten by
        // another connection in the same generation.
        if (player->PreSpawn.LastDetail.find("fieldmod=missing") != std::string::npos ||
            (effectiveSource == LoadoutStatePolicy::EffectiveSource::NativeDefault &&
                player->PreSpawn.LastDetail.find("fieldmod=role-missing") != std::string::npos))
        {
            player->PreSpawn.Active = false;
            player->PreSpawn.SpawnGate = false;
            lease->second.SeedReady = true;
            lease->second.Deadline = now + kPostSpawnRetryWindow;
            ClientLog("[LOADOUT] FieldMod unavailable; using native spawn default player=" +
                key.PlayerId + " role=" + roleId);
            return true;
        }

        // A baseline that cannot become visible within the bounded gate falls
        // back to a complete role DefaultConfig. Runtime overrides never do:
        // they are this match's highest-priority user choice and remain
        // fail-closed until their exact cache entry is visible.
        if (effectiveSource == LoadoutStatePolicy::EffectiveSource::MetaserverBaseline)
        {
            std::string fallbackDetail;
            const ApplyResult fallback = impl_->ApplyNativeDefaultPreSpawn(
                *player, roleId, fallbackDetail);

            player = findCurrentPlayer();
            lease = findOwnedLease();
            if (!player || lease == impl_->SpawnLeases.end())
                return abandonStaleCall();
            if (!player->PreSpawn.Active || !player->PreSpawn.SpawnGate ||
                player->PreSpawn.RoleId != roleId)
            {
                return abandonStaleCall();
            }
            player->PreSpawn.LastDetail = std::move(fallbackDetail);
            if (fallback == ApplyResult::Applied ||
                (fallback == ApplyResult::Pending &&
                    fieldModCacheAbsent(player->PreSpawn.LastDetail)))
            {
                player->PreSpawn.Active = false;
                player->PreSpawn.SpawnGate = false;
                lease->second.SeedReady = true;
                lease->second.Deadline = now + kPostSpawnRetryWindow;
                return true;
            }
        }

        // RoleMissing/Mismatch/not-yet-visible can represent another
        // connection's stale cache. Never release a spawn against it.
        lease->second.Deadline = now + kPostSpawnRetryWindow;
        player->PreSpawn.NextAttempt = now + kPreSpawnRetryInterval;
        return false;
    }

    if (result != ApplyResult::Applied)
    {
        // Invalid metaserver data must degrade to a complete native role
        // default, not to "no write", because the shared role cache may still
        // contain a different player's custom inventory.
        std::string fallbackDetail;
        const ApplyResult fallback = impl_->ApplyNativeDefaultPreSpawn(
            *player, roleId, fallbackDetail);

        player = findCurrentPlayer();
        lease = findOwnedLease();
        if (!player || lease == impl_->SpawnLeases.end())
            return abandonStaleCall();
        if (!player->PreSpawn.Active || !player->PreSpawn.SpawnGate ||
            player->PreSpawn.RoleId != roleId)
        {
            return abandonStaleCall();
        }
        player->PreSpawn.LastDetail = std::move(fallbackDetail);
        const bool fallbackWithoutFieldMod = fallback == ApplyResult::Pending &&
            (player->PreSpawn.LastDetail.find("fieldmod=missing") != std::string::npos ||
                player->PreSpawn.LastDetail.find("fieldmod=role-missing") != std::string::npos);
        if (fallback != ApplyResult::Applied && !fallbackWithoutFieldMod)
        {
            lease->second.Deadline = now + kPostSpawnRetryWindow;
            player->PreSpawn.NextAttempt = now + kPreSpawnRetryInterval;
            return false;
        }
    }

    player->PreSpawn.Active = false;
    player->PreSpawn.SpawnGate = false;
    lease = findOwnedLease();
    if (lease == impl_->SpawnLeases.end()) return false;
    lease->second.SeedReady = true;
    // Keep the role cache exclusive until the exact spawn generation consumes
    // it. CompleteSpawnDispatch releases an observed synchronous failure;
    // wall-clock expiry is deliberately not used after SeedReady.
    lease->second.Deadline = now + kPostSpawnRetryWindow;
    return true;
}

void LoadoutManager::BeginSpawnDispatch(APBPlayerController* playerController)
{
    if (!impl_ || !impl_->ServerActive || !playerController) return;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player || player->ActiveSpawnRoleId.empty() ||
        player->ActiveSpawnRequestGeneration == 0 ||
        player->SelectedRoleId != player->ActiveSpawnRoleId)
    {
        return;
    }

    auto lease = impl_->SpawnLeases.find(player->ActiveSpawnRoleId);
    if (lease == impl_->SpawnLeases.end() ||
        !(lease->second.Owner == player->Key) ||
        !lease->second.SeedReady ||
        lease->second.RequestGeneration == 0 ||
        lease->second.RequestGeneration != player->ActiveSpawnRequestGeneration)
    {
        return;
    }

    lease->second.PriorPawn =
        LoadoutApplication::GetControllerCharacter(playerController);
    lease->second.ExpectedPawn = nullptr;
    lease->second.ObservedInPlacePawn = nullptr;
    lease->second.DispatchInProgress = true;
    lease->second.SpawnFinalized = false;
    lease->second.InventoryDeadline = TimePoint{};
}

void LoadoutManager::CompleteSpawnDispatch(APBPlayerController* playerController)
{
    if (!impl_ || !impl_->ServerActive || !playerController) return;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player || player->ActiveSpawnRoleId.empty() ||
        player->ActiveSpawnRequestGeneration == 0)
    {
        return;
    }

    const Impl::ConnectionKey key = player->Key;
    const std::string roleId = player->ActiveSpawnRoleId;
    APBPlayerController* const expectedController = player->Controller;
    const std::uint64_t requestGeneration =
        player->ActiveSpawnRequestGeneration;
    auto lease = impl_->SpawnLeases.find(roleId);
    if (lease == impl_->SpawnLeases.end())
    {
        // InventorySpawned may have consumed the lease synchronously inside
        // the restart call.
        if (player->ActiveSpawnRoleId == roleId &&
            player->ActiveSpawnRequestGeneration == requestGeneration)
        {
            player->ActiveSpawnRequestGeneration = 0;
            player->ActiveSpawnRoleId.clear();
        }
        return;
    }
    if (!(lease->second.Owner == key) ||
        lease->second.RequestGeneration != requestGeneration ||
        !lease->second.DispatchInProgress)
    {
        return;
    }

    APBCharacter* const currentPawn =
        LoadoutApplication::GetControllerCharacter(expectedController);
    const std::string currentRoleId = currentPawn
        ? LoadoutApplication::ResolveLiveCharacterRoleId(currentPawn)
        : std::string{};
    bool currentPawnAlive = false;
    if (currentPawn && !impl_->DestroyedCharacters.contains(currentPawn))
        currentPawnAlive = LoadoutApplication::IsCharacterAlive(currentPawn);

    // IsAlive is an SDK boundary. Validate the connection and exact request
    // generation again before changing the lease.
    player = impl_->Find(key);
    lease = impl_->SpawnLeases.find(roleId);
    if (!player || player->Controller != expectedController ||
        player->SelectedRoleId != roleId ||
        player->ActiveSpawnRoleId != roleId ||
        player->ActiveSpawnRequestGeneration != requestGeneration ||
        lease == impl_->SpawnLeases.end() ||
        !(lease->second.Owner == key) ||
        lease->second.RequestGeneration != requestGeneration)
    {
        return;
    }

    lease->second.DispatchInProgress = false;
    if (currentPawn && currentPawnAlive && currentRoleId == roleId &&
        !impl_->DestroyedCharacters.contains(currentPawn))
    {
        // InventorySpawned may follow the synchronous restart wrapper. Bind it
        // to this request generation and retain exclusivity until that event.
        const bool consumeObservedInPlace =
            lease->second.ObservedInPlacePawn == currentPawn;
        lease->second.ExpectedPawn = currentPawn;
        if (consumeObservedInPlace)
        {
            // The ambiguous same-Pawn K2 was observed inside this dispatch and
            // the wrapper has now confirmed that Pawn became live. Consume the
            // exact generation, then run the normal post-spawn binding path.
            impl_->ReleaseSpawnLease(key, roleId, requestGeneration);
            (void)impl_->TryBindInventorySpawn(currentPawn, Clock::now());
        }
        return;
    }

    // Unreal's RestartPlayer paths are synchronous. No new/live Pawn at the
    // return boundary is an observed failed dispatch, not a wall-clock guess;
    // release so LateJoin can make its next explicit attempt.
    ClientLog("[LOADOUT] Spawn dispatch produced no live Pawn player=" +
        key.PlayerId + " role=" + roleId);
    impl_->ReleaseSpawnLease(key, roleId, requestGeneration);
}

void LoadoutManager::FinalizeSpawnRequest(APBPlayerController* playerController)
{
    if (!impl_ || !impl_->ServerActive || !playerController) return;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player || player->ActiveSpawnRoleId.empty() ||
        player->ActiveSpawnRequestGeneration == 0)
    {
        return;
    }

    const Impl::ConnectionKey key = player->Key;
    const std::string roleId = player->ActiveSpawnRoleId;
    APBPlayerController* const expectedController = player->Controller;
    const std::uint64_t requestGeneration =
        player->ActiveSpawnRequestGeneration;
    auto lease = impl_->SpawnLeases.find(roleId);
    if (lease == impl_->SpawnLeases.end() ||
        !(lease->second.Owner == key) ||
        lease->second.RequestGeneration != requestGeneration)
    {
        return;
    }

    APBCharacter* const currentPawn =
        LoadoutApplication::GetControllerCharacter(expectedController);
    const std::string currentRoleId = currentPawn
        ? LoadoutApplication::ResolveLiveCharacterRoleId(currentPawn)
        : std::string{};
    const bool currentPawnAlive = currentPawn &&
        !impl_->DestroyedCharacters.contains(currentPawn) &&
        LoadoutApplication::IsCharacterAlive(currentPawn);

    player = impl_->Find(key);
    lease = impl_->SpawnLeases.find(roleId);
    if (!player || player->Controller != expectedController ||
        player->SelectedRoleId != roleId ||
        player->ActiveSpawnRoleId != roleId ||
        player->ActiveSpawnRequestGeneration != requestGeneration ||
        lease == impl_->SpawnLeases.end() ||
        !(lease->second.Owner == key) ||
        lease->second.RequestGeneration != requestGeneration)
    {
        return;
    }

    if (currentPawnAlive && currentRoleId == roleId &&
        (!lease->second.ExpectedPawn || lease->second.ExpectedPawn == currentPawn))
    {
        lease->second.ExpectedPawn = currentPawn;
        lease->second.SpawnFinalized = true;
        lease->second.InventoryDeadline = Clock::now() + kPostSpawnRetryWindow;
        return;
    }

    // LateJoin should only finalize a live Pawn of the requested role. If that
    // invariant no longer holds, abandon this exact request rather than leave
    // a permanent SeedReady lease behind.
    impl_->ReleaseSpawnLease(key, roleId, requestGeneration);
}

void LoadoutManager::AbandonSpawnRequest(APBPlayerController* playerController)
{
    if (!impl_ || !playerController) return;
    Impl::PlayerConnection* player = impl_->Find(playerController);
    if (!player || player->ActiveSpawnRoleId.empty()) return;
    const std::uint64_t requestGeneration =
        player->ActiveSpawnRequestGeneration;
    if (requestGeneration == 0) return;
    impl_->ReleaseSpawnLease(
        player->Key, player->ActiveSpawnRoleId, requestGeneration);
}

void LoadoutManager::TickServer(float deltaSeconds)
{
    (void)deltaSeconds;
    if (!impl_ || !impl_->ServerActive) return;

    const TimePoint now = Clock::now();
    impl_->BindCurrentWorld(UWorld::GetWorld());
    impl_->ConsumeFetchTasks();
    impl_->ReplayDeferredExternalPreOrders(now);
    impl_->ExpireFinalizedSpawnLeases(now);

    // Move the retry list out before crossing any SDK boundary. Destroy and
    // logout callbacks are then free to mutate the live queue without
    // invalidating this iteration; retries merge back by Pawn identity.
    std::vector<Impl::PendingInventoryBinding> pendingBindings =
        std::move(impl_->PendingInventoryBindings);
    impl_->PendingInventoryBindings.clear();
    for (auto& pending : pendingBindings)
    {
        bool keepPending = now < pending.NextAttempt;
        if (!keepPending)
        {
            const bool bound = impl_->TryBindInventorySpawn(pending.Pawn, now);
            keepPending = !bound && now < pending.Deadline &&
                !impl_->DestroyedCharacters.contains(pending.Pawn);
            if (keepPending)
                pending.NextAttempt = now + kPostSpawnRetryInterval;
        }

        if (keepPending)
        {
            const bool alreadyQueued = std::any_of(
                impl_->PendingInventoryBindings.begin(),
                impl_->PendingInventoryBindings.end(),
                [&pending](const auto& live) { return live.Pawn == pending.Pawn; });
            if (!alreadyQueued)
                impl_->PendingInventoryBindings.push_back(std::move(pending));
        }
    }

    std::vector<Impl::ConnectionKey> playerKeys;
    playerKeys.reserve(impl_->Players.size());
    for (const auto& entry : impl_->Players)
        playerKeys.push_back(entry.first);

    for (const Impl::ConnectionKey& key : playerKeys)
    {
        Impl::PlayerConnection* player = impl_->Find(key);
        if (!player) continue;
        if (!player->FetchInFlight && !player->FetchCompleted && !player->FetchTerminal &&
            now >= player->NextFetchAt)
        {
            impl_->StartFetch(*player);
        }
        impl_->TryPreSpawnSeed(key, now);
        impl_->TryPostSpawnApply(key, now);
    }

    struct Replay
    {
        Impl::ConnectionKey Key;
        APBPlayerController* Controller = nullptr;
        std::string RoleId;
        LoadoutRoleConfirmDecision Decision = LoadoutRoleConfirmDecision::Fallback;
    };
    std::vector<Replay> replays;

    for (auto& entry : impl_->Players)
    {
        auto& player = entry.second;
        const auto decision = LoadoutStatePolicy::PollRoleConfirmation(
            player.Pending,
            player.Baselines.find(player.Pending.RoleId) != player.Baselines.end(),
            player.FetchCompleted || player.FetchTerminal,
            now);
        if (!decision) continue;
        replays.push_back({player.Key, player.Controller, player.Pending.RoleId, *decision});
    }

    // Invoke SDK wrappers outside map iteration. ProcessEvent synchronously
    // re-enters BeginRoleConfirmation, observes Replaying, and cannot defer a
    // second time.
    for (const Replay& replay : replays)
    {
        Impl::PlayerConnection* current = impl_->Find(replay.Key);
        if (!replay.Controller || !current ||
            current->Controller != replay.Controller ||
            !current->Pending.Replaying ||
            current->Pending.RoleId != replay.RoleId)
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
            ClientLog("[LOADOUT] Deferred role confirmation replay failed player=" +
                replay.Key.PlayerId);
        }

        if (auto* player = impl_->Find(replay.Key); player && player->Pending.Replaying)
        {
            // CommitRoleConfirmationAfterOriginal normally clears this. The
            // fallback prevents endless replay if the hook was not installed.
            LoadoutStatePolicy::CompleteRoleConfirmation(player->Pending);
        }
    }
}

// ---------------------------------------------------------------------
// Compatibility/client entry points. Client archive/UI state is native.
// ---------------------------------------------------------------------

void LoadoutManager::PreloadSnapshot() {}
void LoadoutManager::NotifyMenuConstructed() {}
void LoadoutManager::RememberMenuSelectedRole(const FName& roleId) { (void)roleId; }

void LoadoutManager::OnRoleSelectionConfirmed(
    APBPlayerController* playerController,
    const FName& roleId,
    bool isAuthoritative)
{
    if (isAuthoritative) CommitRoleConfirmationAfterOriginal(playerController, roleId);
}

void LoadoutManager::OnClientProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnServerProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::TickClient() {}

void LoadoutManager::OnServerLoadoutDataReceived(
    APBPlayerController* playerController,
    const std::string& jsonPayload)
{
    (void)playerController;
    (void)jsonPayload;
}
