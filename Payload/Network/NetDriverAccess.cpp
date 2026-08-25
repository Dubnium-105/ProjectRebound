#include "../framework.h"

#include <atomic>
#include <algorithm>
#include <cstring>
#include <iostream>

#include "NetDriverAccess.h"
#include "../SDK.hpp"

namespace {
std::atomic<SDK::UNetDriver*> g_cachedNetDriver{ nullptr };
std::atomic<SDK::UWorld*> g_cachedWorld{ nullptr };
std::atomic<int> g_lastSource{ static_cast<int>(NetDriverAccess::Source::None) };
std::atomic_bool g_hookArgumentRebindEnabled{ true };

SDK::UNetDriver* ScanForNetDriver(SDK::UWorld* world)
{
    if (!world || !SDK::UObject::GObjects) {
        return nullptr;
    }

    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; --i) {
        SDK::UObject* object = SDK::UObject::GObjects->GetByIndex(i);
        if (!object || object->IsDefaultObject()) {
            continue;
        }

        if (object->IsA(SDK::UIpNetDriver::StaticClass())) {
            auto* const candidate = static_cast<SDK::UNetDriver*>(object);
            if (candidate->World == world && world->NetDriver == candidate) {
                return candidate;
            }
        }
    }

    return nullptr;
}
}

void NetDriverAccess::Observe(SDK::UNetDriver* netDriver, SDK::UWorld* world, Source source)
{
    if (!netDriver) {
        return;
    }

    if (!world) {
        world = SDK::UWorld::GetWorld();
    }

    if (world) {
        bool worldOwnsDriver = world->NetDriver == netDriver;
        bool driverOwnsWorld = netDriver->World == world;
        if (!worldOwnsDriver && !driverOwnsWorld) {
            const bool bothUnbound = !world->NetDriver && !netDriver->World;
            const bool bootstrapScan =
                bothUnbound && source == Source::ObjectScan;
            const bool observedBootstrapTick =
                source == Source::HookArgument && !world->NetDriver &&
                !netDriver->ServerConnection &&
                g_cachedNetDriver.load(std::memory_order_acquire) == netDriver;
            if (!bootstrapScan && !observedBootstrapTick)
                return;

            // ResolveNamedUnboundForBootstrap selects a newly-created driver
            // by exact name while excluding every pre-existing instance. The
            // TickFlush hook is the runtime proof for the same cached driver if
            // GWorld advances from the loading world to the final map world.
            // Never replace a driver already owned by the destination world.
            SDK::UWorld* const oldWorld = netDriver->World;
            if (oldWorld && oldWorld != world && oldWorld->NetDriver == netDriver)
                oldWorld->NetDriver = nullptr;
            world->NetDriver = netDriver;
            netDriver->World = world;
            worldOwnsDriver = true;
            driverOwnsWorld = true;
            std::cout << "[SERVER] Bound unowned NetDriver from "
                << (observedBootstrapTick ? "TickFlush" : "bootstrap scan")
                << "." << std::endl;
        }
        if (worldOwnsDriver && !netDriver->World)
            netDriver->World = world;
        else if (driverOwnsWorld && !world->NetDriver)
        {
            // A TickFlush argument can outlive UWorld's public binding during
            // EndMatch teardown.  Observing that argument proves the driver is
            // still ticking, but it does not authorize mutating the world back
            // into a pre-teardown shape.  Dedicated multi-match restores the
            // validated pair explicitly at the instant it starts ServerTravel.
            if (source == Source::HookArgument &&
                !g_hookArgumentRebindEnabled.load(std::memory_order_acquire))
                return;
            world->NetDriver = netDriver;
            if (source == Source::Cached)
            {
                std::cout << "[SERVER] Restored World NetDriver from validated cached binding."
                    << std::endl;
            }
        }
        if (world->NetDriver != netDriver || netDriver->World != world)
            return;
        g_cachedWorld.store(world, std::memory_order_release);
    }

    g_cachedNetDriver.store(netDriver, std::memory_order_release);
    g_lastSource.store(static_cast<int>(source), std::memory_order_release);
}

void NetDriverAccess::SetHookArgumentRebindEnabled(const bool enabled)
{
    g_hookArgumentRebindEnabled.store(enabled, std::memory_order_release);
}

std::vector<SDK::UNetDriver*> NetDriverAccess::SnapshotNetDrivers()
{
    std::vector<SDK::UNetDriver*> drivers;
    if (!SDK::UObject::GObjects)
        return drivers;

    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; --i) {
        SDK::UObject* object = SDK::UObject::GObjects->GetByIndex(i);
        if (object && !object->IsDefaultObject() &&
            object->IsA(SDK::UIpNetDriver::StaticClass())) {
            drivers.push_back(static_cast<SDK::UNetDriver*>(object));
        }
    }
    return drivers;
}

SDK::UNetDriver* NetDriverAccess::ResolveNamedUnboundForBootstrap(
    SDK::UWorld* world,
    const SDK::FName& netDriverName,
    const std::vector<SDK::UNetDriver*>& excludedDrivers)
{
    if (!world || !SDK::UObject::GObjects)
        return nullptr;

    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; --i) {
        SDK::UObject* object = SDK::UObject::GObjects->GetByIndex(i);
        if (!object || object->IsDefaultObject() ||
            !object->IsA(SDK::UIpNetDriver::StaticClass())) {
            continue;
        }

        auto* const candidate = static_cast<SDK::UNetDriver*>(object);
        if (std::find(excludedDrivers.begin(), excludedDrivers.end(), candidate) !=
                excludedDrivers.end() ||
            candidate->NetDriverName.ComparisonIndex != netDriverName.ComparisonIndex ||
            candidate->NetDriverName.Number != netDriverName.Number ||
            (candidate->World && candidate->World != world) ||
            candidate->ServerConnection || candidate->ClientConnections.Num() != 0) {
            continue;
        }
        return candidate;
    }
    return nullptr;
}

void NetDriverAccess::ResetForWorldChange(SDK::UWorld* world)
{
    g_hookArgumentRebindEnabled.store(true, std::memory_order_release);
    SDK::UNetDriver* const cached = g_cachedNetDriver.load(std::memory_order_acquire);
    if (!world || !cached || cached->World != world || world->NetDriver != cached) {
        g_cachedNetDriver.store(nullptr, std::memory_order_release);
        g_cachedWorld.store(world, std::memory_order_release);
        g_lastSource.store(static_cast<int>(Source::None), std::memory_order_release);
        return;
    }

    g_cachedWorld.store(world, std::memory_order_release);
}

SDK::UNetDriver* NetDriverAccess::Resolve(
    bool allowObjectScan,
    bool restoreCachedBinding)
{
    SDK::UWorld* world = SDK::UWorld::GetWorld();
    if (world && world->NetDriver) {
        SDK::UNetDriver* const worldNetDriver = world->NetDriver;
        Observe(worldNetDriver, world, Source::World);
        if (worldNetDriver->World == world)
            return worldNetDriver;
    }

    SDK::UNetDriver* cachedNetDriver = g_cachedNetDriver.load(std::memory_order_acquire);
    SDK::UWorld* cachedWorld = g_cachedWorld.load(std::memory_order_acquire);
    if (cachedNetDriver && world && world == cachedWorld &&
        cachedNetDriver->World == world &&
        (!world->NetDriver || world->NetDriver == cachedNetDriver)) {
        if (!restoreCachedBinding)
            return cachedNetDriver;
        Observe(cachedNetDriver, world, Source::Cached);
        if (world->NetDriver == cachedNetDriver && cachedNetDriver->World == world)
            return cachedNetDriver;
    }

    if (!allowObjectScan) {
        return nullptr;
    }

    SDK::UNetDriver* scannedNetDriver = ScanForNetDriver(world);
    if (scannedNetDriver) {
        Observe(scannedNetDriver, world, Source::ObjectScan);
    }

    return scannedNetDriver;
}

bool NetDriverAccess::RestoreValidatedBinding(
    SDK::UWorld* world,
    SDK::UNetDriver* netDriver)
{
    if (!world || !netDriver || world != SDK::UWorld::GetWorld())
        return false;

    SDK::UNetDriver* const cachedNetDriver =
        g_cachedNetDriver.load(std::memory_order_acquire);
    SDK::UWorld* const cachedWorld =
        g_cachedWorld.load(std::memory_order_acquire);
    if (cachedNetDriver != netDriver || cachedWorld != world ||
        netDriver->World != world ||
        (world->NetDriver && world->NetDriver != netDriver))
    {
        return false;
    }

    Observe(netDriver, world, Source::Cached);
    return world->NetDriver == netDriver && netDriver->World == world;
}

bool NetDriverAccess::TryGetSnapshot(Snapshot& snapshot, bool allowObjectScan)
{
    snapshot = {};

    SDK::UNetDriver* netDriver = Resolve(allowObjectScan);
    if (!netDriver) {
        return false;
    }

    SDK::UWorld* world = SDK::UWorld::GetWorld();
    if (!world) {
        world = netDriver->World;
    }

    snapshot.NetDriver = netDriver;
    snapshot.World = world;
    snapshot.ServerConnection = netDriver->ServerConnection;
    snapshot.ClientConnectionCount = netDriver->ClientConnections.Num();
    snapshot.MaxClientRate = netDriver->MaxClientRate;
    snapshot.MaxInternetClientRate = netDriver->MaxInternetClientRate;
    snapshot.NetServerMaxTickRate = netDriver->NetServerMaxTickRate;
    snapshot.TimeSeconds = netDriver->Time;
    snapshot.WorldMatches = world && world->NetDriver == netDriver;
    snapshot.HasReplicationDriver = netDriver->ReplicationDriver != nullptr;
    snapshot.NetDriverNameComparisonIndex = netDriver->NetDriverName.ComparisonIndex;
    snapshot.NetDriverNameNumber = netDriver->NetDriverName.Number;
    snapshot.LastSource = static_cast<Source>(g_lastSource.load(std::memory_order_acquire));
    return true;
}

const char* NetDriverAccess::ToString(Source source)
{
    switch (source) {
    case Source::HookArgument:
        return "hook";
    case Source::World:
        return "world";
    case Source::ObjectScan:
        return "object-scan";
    case Source::Cached:
        return "cached";
    default:
        return "none";
    }
}

extern "C" __declspec(dllexport) void* PR_GetActiveNetDriver()
{
    return NetDriverAccess::Resolve();
}

extern "C" __declspec(dllexport) void* PR_GetActiveWorld()
{
    SDK::UWorld* world = SDK::UWorld::GetWorld();
    if (!world) {
        world = g_cachedWorld.load(std::memory_order_acquire);
    }

    return world;
}

extern "C" __declspec(dllexport) BOOL PR_GetNetDriverSnapshot(ProjectReboundNetDriverSnapshot* snapshot)
{
    if (!snapshot) {
        return FALSE;
    }

    std::memset(snapshot, 0, sizeof(*snapshot));

    NetDriverAccess::Snapshot internalSnapshot{};
    if (!NetDriverAccess::TryGetSnapshot(internalSnapshot)) {
        return FALSE;
    }

    snapshot->NetDriver = internalSnapshot.NetDriver;
    snapshot->World = internalSnapshot.World;
    snapshot->ServerConnection = internalSnapshot.ServerConnection;
    snapshot->ClientConnectionCount = internalSnapshot.ClientConnectionCount;
    snapshot->MaxClientRate = internalSnapshot.MaxClientRate;
    snapshot->MaxInternetClientRate = internalSnapshot.MaxInternetClientRate;
    snapshot->NetServerMaxTickRate = internalSnapshot.NetServerMaxTickRate;
    snapshot->TimeSeconds = internalSnapshot.TimeSeconds;
    snapshot->NetDriverNameComparisonIndex = internalSnapshot.NetDriverNameComparisonIndex;
    snapshot->NetDriverNameNumber = internalSnapshot.NetDriverNameNumber;
    snapshot->WorldMatches = internalSnapshot.WorldMatches ? TRUE : FALSE;
    snapshot->HasReplicationDriver = internalSnapshot.HasReplicationDriver ? TRUE : FALSE;
    snapshot->Source = static_cast<int32_t>(internalSnapshot.LastSource);
    return TRUE;
}
