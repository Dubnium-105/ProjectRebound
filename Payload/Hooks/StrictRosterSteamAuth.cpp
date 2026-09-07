#include "StrictRosterSteamAuth.h"
#include "../ClientLogic/NativeLoginGrantPolicy.h"

#define WIN32_LEAN_AND_MEAN
#include <Windows.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <charconv>
#include <condition_variable>
#include <cstdint>
#include <cstring>
#include <deque>
#include <limits>
#include <mutex>
#include <string>
#include <string_view>
#include <thread>
#include <unordered_map>
#include <vector>

namespace StrictRosterSteamAuth
{
    namespace
    {
        using RegisterCallback = void(__cdecl *)(void*, int);
        using UnregisterCallback = void(__cdecl *)(void*);
        using SteamUserAccessor = void*(__cdecl *)();
        using SteamUserGetAuthTicket = std::uint32_t(__cdecl *)(
            void*, void*, int, std::uint32_t*, void*);
        using SteamUserCancelAuthTicket = void(__cdecl *)(void*, std::uint32_t);
        using SteamGameServerAccessor = void*(__cdecl *)();
        using SteamGameServerBeginAuthSession = int(__cdecl *)(
            void*, const void*, int, std::uint64_t);
        using SteamGameServerEndAuthSession = void(__cdecl *)(
            void*, std::uint64_t);
        using SteamInternalGameServerInit = bool(__cdecl *)(
            std::uint32_t,
            std::uint16_t,
            std::uint16_t,
            std::uint16_t,
            int,
            const char*);
        using SteamGameServerRunCallbacks = void(__cdecl *)();
        using SteamGameServerGetHSteamPipe = int(__cdecl *)();
        using SteamGameServerShutdown = void(__cdecl *)();
        using SteamGameServerSetString = void(__cdecl *)(void*, const char*);
        using SteamGameServerSetBool = void(__cdecl *)(void*, bool);
        using SteamGameServerSetInt = void(__cdecl *)(void*, int);
        using SteamGameServerLogOnAnonymous = void(__cdecl *)(void*);

        constexpr std::uint32_t kGetAuthSessionTicketResponseCallback = 163U;
        constexpr std::size_t kGetAuthSessionTicketResponseBytes = 8U;
        constexpr std::int32_t kEResultOk = 1;

        // This is the fixed SDK CCallbackBase ABI: the callback flag is one
        // byte (0x02 means gameserver), followed by the callback id.  Keeping
        // the layout exact matters because Steam invokes the registered object
        // directly from its dispatcher.
        struct CallbackVTable
        {
            void(__cdecl *run)(void*, void*);
            void(__cdecl *runWithCallResult)(
                void*, void*, bool, std::uint64_t);
            int(__cdecl *getCallbackSizeBytes)(void*);
        };

        struct CallbackObject
        {
            CallbackVTable* vtable = nullptr;
            std::uint8_t callbackFlags = 0;
            std::uint8_t reserved[3]{};
            std::int32_t callbackId = static_cast<std::int32_t>(
                kValidateAuthTicketResponseCallback);
        };

        struct ExpectedGrant
        {
            std::string grantJti;
            std::string nativeConnectionNonce;
            std::uint64_t expiresAt = 0;
            bool callbackOk = false;
            bool authStarted = false;
        };

        struct ConnectionBinding
        {
            std::uint64_t steamId = 0;
            std::string grantJti;
            std::uint64_t expiresAt = 0;
        };

        std::mutex gMutex;
        // A callback is accepted only after this exact platform identity and
        // Grant JTI were armed by the current PreLogin attempt.  There is no
        // generic SteamID proof pool that another connection can consume.
        std::unordered_map<std::uint64_t, ExpectedGrant> gExpectedGrants;
        std::unordered_map<std::string, ConnectionBinding> gBindings;
        std::unordered_map<std::string, std::uint64_t> gServerAuthSessions;
        std::deque<std::string> gRevokedConnectionNonces;
        std::condition_variable gProofChanged;
        CallbackObject gCallback;
        CallbackObject gClientTicketCallback;
        CallbackVTable gCallbackVTable{};
        CallbackVTable gClientTicketCallbackVTable{};
        RegisterCallback gRegisterCallback = nullptr;
        UnregisterCallback gUnregisterCallback = nullptr;
        SteamUserAccessor gSteamUserAccessor = nullptr;
        SteamUserGetAuthTicket gSteamUserGetAuthTicket = nullptr;
        SteamUserCancelAuthTicket gSteamUserCancelAuthTicket = nullptr;
        SteamGameServerAccessor gSteamGameServerAccessor = nullptr;
        SteamGameServerBeginAuthSession gSteamGameServerBeginAuthSession = nullptr;
        SteamGameServerEndAuthSession gSteamGameServerEndAuthSession = nullptr;
        SteamInternalGameServerInit gSteamInternalGameServerInit = nullptr;
        SteamGameServerRunCallbacks gSteamGameServerRunCallbacks = nullptr;
        SteamGameServerGetHSteamPipe gSteamGameServerGetHSteamPipe = nullptr;
        SteamGameServerShutdown gSteamGameServerShutdown = nullptr;
        SteamGameServerSetString gSteamGameServerSetProduct = nullptr;
        SteamGameServerSetString gSteamGameServerSetGameDescription = nullptr;
        SteamGameServerSetString gSteamGameServerSetModDir = nullptr;
        SteamGameServerSetBool gSteamGameServerSetDedicatedServer = nullptr;
        SteamGameServerSetInt gSteamGameServerSetMaxPlayerCount = nullptr;
        SteamGameServerLogOnAnonymous gSteamGameServerLogOnAnonymous = nullptr;
        void* gSteamUser = nullptr;
        void* gSteamGameServer = nullptr;
        bool gGameServerInitializationOwned = false;
        std::atomic_bool gGameServerPumpStop{false};
        std::thread gGameServerPump;
        std::vector<std::uint8_t> gClientTicket;
        std::uint32_t gClientTicketHandle = 0;
        bool gClientTicketRequested = false;
        // GetAuthSessionTicket may dispatch callback 163 before the native
        // call has returned its handle.  Keep that bounded early callback
        // set until the returned handle is known; never accept a callback by
        // SteamID or by arrival time alone.
        struct EarlyTicketCallback
        {
            std::uint32_t handle = 0;
            std::int32_t result = -1;
        };
        std::array<EarlyTicketCallback, 8> gEarlyTicketCallbacks{};
        std::size_t gEarlyTicketCallbackCount = 0;
        bool gClientTicketRequestInFlight = false;
        bool gClientTicketCancellationRequested = false;
        bool gClientTicketReady = false;
        bool gClientTicketFailed = false;
        bool gClientTicketCallbackRegistered = false;
        bool gRegistered = false;
        bool gShuttingDown = false;

        bool ParseDecimalSteamId(
            const std::string_view value,
            std::uint64_t& steamId) noexcept
        {
            if (value.empty() || value.size() > 20U)
                return false;
            for (const char character : value)
            {
                if (character < '0' || character > '9')
                    return false;
            }
            const auto parsed = std::from_chars(
                value.data(), value.data() + value.size(), steamId);
            return parsed.ec == std::errc{} &&
                parsed.ptr == value.data() + value.size() && steamId != 0;
        }

        bool IsSafeGrantJti(const std::string_view value) noexcept
        {
            if (value.empty() || value.size() > 96U)
                return false;
            for (const unsigned char character : value)
            {
                if (!(character >= 'a' && character <= 'z') &&
                    !(character >= 'A' && character <= 'Z') &&
                    !(character >= '0' && character <= '9') &&
                    character != '-' && character != '_' && character != '.')
                {
                    return false;
                }
            }
            return true;
        }

        std::uint64_t AddLifetime(
            const std::uint64_t nowMilliseconds,
            const std::uint64_t lifetime) noexcept
        {
            return nowMilliseconds > (std::numeric_limits<std::uint64_t>::max)() -
                    lifetime
                ? (std::numeric_limits<std::uint64_t>::max)()
                : nowMilliseconds + lifetime;
        }

        void SecureClearTicketBytes(
            std::vector<std::uint8_t>& bytes) noexcept
        {
            if (!bytes.empty())
                SecureZeroMemory(bytes.data(), bytes.size());
            bytes.clear();
        }

        void PurgeExpiredLocked(const std::uint64_t nowMilliseconds) noexcept
        {
            for (auto it = gExpectedGrants.begin(); it != gExpectedGrants.end();)
            {
                if (it->second.expiresAt <= nowMilliseconds)
                    it = gExpectedGrants.erase(it);
                else
                    ++it;
            }
            for (auto it = gBindings.begin(); it != gBindings.end();)
            {
                // A live binding is released by the native disconnect,
                // Steam revocation, or allocation cleanup path.  It must not
                // silently expire while the UE connection is still alive.
                if (it->second.expiresAt !=
                        (std::numeric_limits<std::uint64_t>::max)() &&
                    it->second.expiresAt <= nowMilliseconds)
                    it = gBindings.erase(it);
                else
                    ++it;
            }
        }

        std::vector<std::uint64_t> RevokeSteamIdLocked(
            const std::uint64_t steamId)
        {
            gExpectedGrants.erase(steamId);
            for (auto it = gBindings.begin(); it != gBindings.end();)
            {
                if (it->second.steamId == steamId)
                {
                    try
                    {
                        if (gRevokedConnectionNonces.size() < 256U)
                            gRevokedConnectionNonces.push_back(it->first);
                    }
                    catch (...)
                    {
                        // The authoritative binding is still removed even if
                        // best-effort game-thread kick queueing is exhausted.
                    }
                    it = gBindings.erase(it);
                }
                else
                    ++it;
            }
            std::vector<std::uint64_t> sessions;
            for (auto it = gServerAuthSessions.begin();
                it != gServerAuthSessions.end();)
            {
                if (it->second == steamId)
                {
                    sessions.push_back(it->second);
                    it = gServerAuthSessions.erase(it);
                }
                else
                    ++it;
            }
            return sessions;
        }

        void GameServerCallbackPumpMain() noexcept
        {
            while (!gGameServerPumpStop.load(std::memory_order_acquire))
            {
                const auto runCallbacks = gSteamGameServerRunCallbacks;
                if (runCallbacks)
                    runCallbacks();
                Sleep(10U);
            }
        }

        std::uint16_t RequestedGamePort() noexcept
        {
            const char* const commandLine = GetCommandLineA();
            if (!commandLine)
                return 0;
            const std::string_view command(commandLine);
            constexpr std::string_view prefix = "-port=";
            const std::size_t position = command.find(prefix);
            if (position == std::string_view::npos)
                return 0;
            const auto begin = command.data() + position + prefix.size();
            const auto end = command.data() + command.size();
            unsigned int port = 0;
            const auto parsed = std::from_chars(begin, end, port);
            return parsed.ec == std::errc{} && parsed.ptr != begin &&
                    port >= 1U && port <= 65535U
                ? static_cast<std::uint16_t>(port)
                : 0;
        }

        void __cdecl OnCallback(void*, void* payload) noexcept
        {
            ObserveValidateAuthTicketResponse(
                payload,
                kValidateAuthTicketResponseBytes,
                static_cast<std::uint64_t>(GetTickCount64()));
        }

        void __cdecl OnCallbackWithCallResult(
            void*,
            void* payload,
            const bool,
            const std::uint64_t) noexcept
        {
            OnCallback(nullptr, payload);
        }

        int __cdecl CallbackSize(void*) noexcept
        {
            return static_cast<int>(kValidateAuthTicketResponseBytes);
        }

        void __cdecl OnTicketCallback(void*, void* payload) noexcept
        {
            if (!payload || kGetAuthSessionTicketResponseBytes > 8U)
                return;
            std::uint32_t handle = 0;
            std::int32_t result = -1;
            std::memcpy(&handle, payload, sizeof(handle));
            std::memcpy(
                &result,
                static_cast<const std::uint8_t*>(payload) + sizeof(handle),
                sizeof(result));
            std::lock_guard lock(gMutex);
            if (gClientTicketRequestInFlight && !gClientTicketRequested)
            {
                // The Steam callback can race the return from
                // GetAuthSessionTicket.  The handle is the only binding
                // available once the call returns, so retain a small bounded
                // set and match it then.  A callback for another request is
                // ignored after the set is cleared; it can never authorize
                // this carrier by identity or timing alone.
                if (gEarlyTicketCallbackCount < gEarlyTicketCallbacks.size())
                {
                    gEarlyTicketCallbacks[gEarlyTicketCallbackCount++] =
                        EarlyTicketCallback{handle, result};
                }
                return;
            }
            if (!gClientTicketRequested || handle != gClientTicketHandle)
                return;
            if (result == kEResultOk)
            {
                gClientTicketReady = !gClientTicket.empty();
                gClientTicketFailed = !gClientTicketReady;
            }
            else
            {
                gClientTicketReady = false;
                gClientTicketFailed = true;
                SecureClearTicketBytes(gClientTicket);
            }
        }

        void __cdecl OnTicketCallbackWithCallResult(
            void*, void* payload, const bool, const std::uint64_t) noexcept
        {
            OnTicketCallback(nullptr, payload);
        }

        int __cdecl TicketCallbackSize(void*) noexcept
        {
            return static_cast<int>(kGetAuthSessionTicketResponseBytes);
        }

        bool ResolveSteamCallbacksLocked(HMODULE steamApi) noexcept
        {
            if (!gRegisterCallback)
                gRegisterCallback = reinterpret_cast<RegisterCallback>(
                    GetProcAddress(steamApi, "SteamAPI_RegisterCallback"));
            if (!gUnregisterCallback)
                gUnregisterCallback = reinterpret_cast<UnregisterCallback>(
                    GetProcAddress(steamApi, "SteamAPI_UnregisterCallback"));
            return gRegisterCallback && gUnregisterCallback;
        }
    }

    bool Initialize(const bool gameserver) noexcept
    {
        std::lock_guard lock(gMutex);
        if (gRegistered)
            return true;
        if (gShuttingDown)
            return false;

        HMODULE steamApi = GetModuleHandleA("steam_api64.dll");
        if (!steamApi)
            return false;
        if (!ResolveSteamCallbacksLocked(steamApi))
        {
            gRegisterCallback = nullptr;
            gUnregisterCallback = nullptr;
            return false;
        }

        if (gameserver)
        {
            gSteamInternalGameServerInit =
                reinterpret_cast<SteamInternalGameServerInit>(GetProcAddress(
                    steamApi, "SteamInternal_GameServer_Init"));
            gSteamGameServerRunCallbacks =
                reinterpret_cast<SteamGameServerRunCallbacks>(GetProcAddress(
                    steamApi, "SteamGameServer_RunCallbacks"));
            gSteamGameServerGetHSteamPipe =
                reinterpret_cast<SteamGameServerGetHSteamPipe>(GetProcAddress(
                    steamApi, "SteamGameServer_GetHSteamPipe"));
            gSteamGameServerShutdown =
                reinterpret_cast<SteamGameServerShutdown>(GetProcAddress(
                    steamApi, "SteamGameServer_Shutdown"));
            gSteamGameServerAccessor = reinterpret_cast<SteamGameServerAccessor>(
                GetProcAddress(steamApi, "SteamAPI_SteamGameServer_v015"));
            gSteamGameServerBeginAuthSession =
                reinterpret_cast<SteamGameServerBeginAuthSession>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_BeginAuthSession"));
            gSteamGameServerEndAuthSession =
                reinterpret_cast<SteamGameServerEndAuthSession>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_EndAuthSession"));
            gSteamGameServerSetProduct =
                reinterpret_cast<SteamGameServerSetString>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_SetProduct"));
            gSteamGameServerSetGameDescription =
                reinterpret_cast<SteamGameServerSetString>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_SetGameDescription"));
            gSteamGameServerSetModDir =
                reinterpret_cast<SteamGameServerSetString>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_SetModDir"));
            gSteamGameServerSetDedicatedServer =
                reinterpret_cast<SteamGameServerSetBool>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_SetDedicatedServer"));
            gSteamGameServerSetMaxPlayerCount =
                reinterpret_cast<SteamGameServerSetInt>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_SetMaxPlayerCount"));
            gSteamGameServerLogOnAnonymous =
                reinterpret_cast<SteamGameServerLogOnAnonymous>(GetProcAddress(
                    steamApi, "SteamAPI_ISteamGameServer_LogOnAnonymous"));
            if (!gSteamInternalGameServerInit ||
                !gSteamGameServerRunCallbacks ||
                !gSteamGameServerGetHSteamPipe ||
                !gSteamGameServerShutdown ||
                !gSteamGameServerAccessor ||
                !gSteamGameServerBeginAuthSession ||
                !gSteamGameServerEndAuthSession ||
                !gSteamGameServerSetProduct ||
                !gSteamGameServerSetGameDescription ||
                !gSteamGameServerSetModDir ||
                !gSteamGameServerSetDedicatedServer ||
                !gSteamGameServerSetMaxPlayerCount ||
                !gSteamGameServerLogOnAnonymous)
            {
                return false;
            }

            const int existingPipe = gSteamGameServerGetHSteamPipe();
            // The owned helper establishes that GetHSteamPipe is the safe
            // existence probe.  Do not call the interface accessor before a
            // successful Init: on a client-only Steam context that accessor
            // can assert inside steam_api64.dll.
            if (existingPipe != 0)
            {
                // The game already owns this gameserver pipe.  We never
                // steal its callback pump or call Shutdown on its behalf;
                // without an independently owned pipe the strict authority
                // remains unavailable and startup must fail closed.
                return false;
            }
            if (existingPipe == 0)
            {
                const std::uint16_t gamePort = RequestedGamePort();
                const std::uint16_t queryPort = gamePort != 0U && gamePort < 65535U
                    ? static_cast<std::uint16_t>(gamePort + 1U) : 0U;
                // Verified against the owned Steamworks probe: the six
                // parameters are IP, Steam port, game port, query port,
                // EServerMode, and version string.  Passing the game/query
                // ports in the first two port positions makes the server
                // mode land in the fifth slot (the previous ordering made a
                // mode-0 server and could never establish authority).
                const bool initialized = gSteamInternalGameServerInit(
                    0U, 0U, gamePort, queryPort, 2, "1.0.0.0");
                gGameServerInitializationOwned = initialized;
                if (!initialized ||
                    !gSteamGameServerAccessor() ||
                    gSteamGameServerGetHSteamPipe() == 0)
                {
                    if (gGameServerInitializationOwned &&
                        gSteamGameServerShutdown)
                    {
                        gSteamGameServerShutdown();
                    }
                    gGameServerInitializationOwned = false;
                    return false;
                }
                gSteamGameServer = gSteamGameServerAccessor();
                gSteamGameServerSetProduct(gSteamGameServer, "Boundary");
                gSteamGameServerSetGameDescription(
                    gSteamGameServer, "Boundary");
                gSteamGameServerSetModDir(gSteamGameServer, "Boundary");
                gSteamGameServerSetDedicatedServer(gSteamGameServer, true);
                gSteamGameServerSetMaxPlayerCount(gSteamGameServer, 4);
                gSteamGameServerLogOnAnonymous(gSteamGameServer);
            }
        }

        gCallbackVTable = CallbackVTable{
            &OnCallback,
            &OnCallbackWithCallResult,
            &CallbackSize};
        gCallback.vtable = &gCallbackVTable;
        gCallback.callbackFlags = gameserver ? kCallbackFlagsGameServer : 0U;
        gCallback.reserved[0] = 0;
        gCallback.reserved[1] = 0;
        gCallback.reserved[2] = 0;
        gCallback.callbackId = static_cast<std::int32_t>(
            kValidateAuthTicketResponseCallback);
        gRegisterCallback(&gCallback, static_cast<int>(
            kValidateAuthTicketResponseCallback));
        gRegistered = true;
        if (gameserver)
        {
            gGameServerPumpStop.store(false, std::memory_order_release);
            try
            {
                gGameServerPump = std::thread(&GameServerCallbackPumpMain);
            }
            catch (...)
            {
                gUnregisterCallback(&gCallback);
                gRegistered = false;
                if (gGameServerInitializationOwned && gSteamGameServerShutdown)
                    gSteamGameServerShutdown();
                gGameServerInitializationOwned = false;
                gSteamGameServer = nullptr;
                return false;
            }
        }
        return true;
    }

    void Shutdown() noexcept
    {
        // Steam unregister/cancel/end calls can synchronously dispatch into
        // the callback objects.  Never hold gMutex across those external
        // calls: the callback handlers take the same mutex.
        UnregisterCallback unregisterCallback = nullptr;
        SteamUserCancelAuthTicket cancelTicket = nullptr;
        void* user = nullptr;
        std::uint32_t ticketHandle = 0;
        SteamGameServerEndAuthSession endAuthSession = nullptr;
        SteamGameServerShutdown shutdownGameServer = nullptr;
        void* gameServer = nullptr;
        std::unordered_map<std::string, std::uint64_t> sessions;
        std::thread callbackPump;
        bool unregisterClientTicket = false;
        bool unregisterServerCallback = false;
        bool shutdownOwnedGameServer = false;
        {
            std::lock_guard lock(gMutex);
            if (gShuttingDown)
                return;
            gShuttingDown = true;
            unregisterCallback = gUnregisterCallback;
            unregisterClientTicket = gClientTicketCallbackRegistered;
            unregisterServerCallback = gRegistered;
            gClientTicketCallbackRegistered = false;
            gRegistered = false;

            cancelTicket = gSteamUserCancelAuthTicket;
            user = gSteamUser;
            ticketHandle = gClientTicketHandle;
            if (gClientTicketRequestInFlight)
                gClientTicketCancellationRequested = true;
            gClientTicketRequested = false;
            gClientTicketReady = false;
            gClientTicketFailed = false;
            gClientTicketHandle = 0;
            gEarlyTicketCallbackCount = 0;
            SecureClearTicketBytes(gClientTicket);

            endAuthSession = gSteamGameServerEndAuthSession;
            shutdownGameServer = gSteamGameServerShutdown;
            shutdownOwnedGameServer = gGameServerInitializationOwned;
            gameServer = gSteamGameServer;
            sessions.swap(gServerAuthSessions);
            gExpectedGrants.clear();
            gBindings.clear();
            gRevokedConnectionNonces.clear();
            gProofChanged.notify_all();
            gGameServerPumpStop.store(true, std::memory_order_release);
            callbackPump = std::move(gGameServerPump);

            gRegisterCallback = nullptr;
            gUnregisterCallback = nullptr;
            gSteamGameServer = nullptr;
            gSteamGameServerAccessor = nullptr;
            gSteamGameServerBeginAuthSession = nullptr;
            gSteamGameServerEndAuthSession = nullptr;
            gSteamUser = nullptr;
            gSteamUserAccessor = nullptr;
            gSteamUserGetAuthTicket = nullptr;
            gSteamUserCancelAuthTicket = nullptr;
        }

        if (callbackPump.joinable() &&
            callbackPump.get_id() != std::this_thread::get_id())
        {
            callbackPump.join();
        }

        if (unregisterCallback)
        {
            if (unregisterClientTicket)
                unregisterCallback(&gClientTicketCallback);
            if (unregisterServerCallback)
                unregisterCallback(&gCallback);
        }
        if (cancelTicket && user && ticketHandle != 0U)
            cancelTicket(user, ticketHandle);
        if (endAuthSession && gameServer)
        {
            for (const auto& [nonce, steamId] : sessions)
            {
                (void)nonce;
                endAuthSession(gameServer, steamId);
            }
        }
        if (shutdownOwnedGameServer && shutdownGameServer)
            shutdownGameServer();
        {
            std::lock_guard lock(gMutex);
            gSteamGameServerRunCallbacks = nullptr;
            gSteamGameServerGetHSteamPipe = nullptr;
            gSteamGameServerShutdown = nullptr;
            gSteamGameServerSetProduct = nullptr;
            gSteamGameServerSetGameDescription = nullptr;
            gSteamGameServerSetModDir = nullptr;
            gSteamGameServerSetDedicatedServer = nullptr;
            gSteamGameServerSetMaxPlayerCount = nullptr;
            gSteamGameServerLogOnAnonymous = nullptr;
            gSteamInternalGameServerInit = nullptr;
            gGameServerInitializationOwned = false;
            gShuttingDown = false;
        }
    }

    bool ArmExpectedGrant(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce,
        const std::uint64_t nowMilliseconds) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId) ||
            !IsSafeGrantJti(grantJti) || nativeConnectionNonce.empty() ||
            nativeConnectionNonce.size() > 128U)
        {
            return false;
        }
        try
        {
            std::lock_guard lock(gMutex);
            PurgeExpiredLocked(nowMilliseconds);
            const auto existing = gExpectedGrants.find(steamId);
            if (existing != gExpectedGrants.end() &&
                (existing->second.grantJti != grantJti ||
                 existing->second.nativeConnectionNonce != nativeConnectionNonce))
            {
                // A second JTI cannot race and steal the pending native
                // handshake for this platform identity.
                return false;
            }
            if (existing == gExpectedGrants.end())
            {
                gExpectedGrants.emplace(
                    steamId,
                    ExpectedGrant{
                        std::string(grantJti),
                        std::string(nativeConnectionNonce),
                        AddLifetime(nowMilliseconds, kProofLifetimeMilliseconds),
                        false,
                        false});
            }
            else
            {
                existing->second.expiresAt = AddLifetime(
                    nowMilliseconds, kProofLifetimeMilliseconds);
            }
            return true;
        }
        catch (...)
        {
            return false;
        }
    }

    void ObserveValidateAuthTicketResponse(
        const void* payload,
        const std::size_t payloadBytes,
        const std::uint64_t nowMilliseconds) noexcept
    {
        if (!payload || payloadBytes < kValidateAuthTicketResponseBytes)
            return;
        std::uint64_t steamId = 0;
        std::int32_t response = -1;
        std::uint64_t ownerSteamId = 0;
        std::memcpy(&steamId, payload, sizeof(steamId));
        std::memcpy(
            &response,
            static_cast<const std::uint8_t*>(payload) + sizeof(steamId),
            sizeof(response));
        std::memcpy(
            &ownerSteamId,
            static_cast<const std::uint8_t*>(payload) + sizeof(steamId) +
                sizeof(response),
            sizeof(ownerSteamId));
        (void)ownerSteamId;
        if (steamId == 0)
            return;

        std::vector<std::uint64_t> sessionsToEnd;
        SteamGameServerEndAuthSession endAuth = nullptr;
        void* server = nullptr;
        bool proofStateChanged = false;
        {
            std::lock_guard lock(gMutex);
            PurgeExpiredLocked(nowMilliseconds);
            if (response != kAuthSessionResponseOk)
            {
                // Steam revocation applies immediately to every active native
                // connection for that platform identity and to its pending Grant.
                sessionsToEnd = RevokeSteamIdLocked(steamId);
                proofStateChanged = true;
            }
            else
            {
                const auto expected = gExpectedGrants.find(steamId);
                if (expected == gExpectedGrants.end() ||
                    !expected->second.authStarted)
                {
                    // A callback that was not preceded by this authority's
                    // BeginAuthSession is ignored; SteamID alone is not proof.
                    return;
                }
                expected->second.callbackOk = true;
                expected->second.expiresAt = AddLifetime(
                    nowMilliseconds, kProofLifetimeMilliseconds);
                proofStateChanged = true;
            }
            endAuth = gSteamGameServerEndAuthSession;
            server = gSteamGameServer;
        }
        if (proofStateChanged)
            gProofChanged.notify_all();
        // The callback is dispatched by Steam's normal gameserver pump. End
        // revoked sessions only after releasing gMutex to avoid re-entry.
        if (endAuth && server)
        {
            for (const auto sessionSteamId : sessionsToEnd)
                endAuth(server, sessionSteamId);
        }
    }

    bool ConsumeExpectedGrantProof(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce,
        const std::uint64_t nowMilliseconds) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId) ||
            !IsSafeGrantJti(grantJti) ||
            nativeConnectionNonce.empty() || nativeConnectionNonce.size() > 128U)
        {
            return false;
        }
        try
        {
            std::lock_guard lock(gMutex);
            PurgeExpiredLocked(nowMilliseconds);
            const auto existingBinding = gBindings.find(
                std::string(nativeConnectionNonce));
            if (existingBinding != gBindings.end())
            {
                return existingBinding->second.steamId == steamId &&
                    existingBinding->second.grantJti == grantJti &&
                    existingBinding->second.expiresAt > nowMilliseconds;
            }
            const auto expected = gExpectedGrants.find(steamId);
            if (expected == gExpectedGrants.end() ||
                expected->second.grantJti != grantJti ||
                expected->second.nativeConnectionNonce != nativeConnectionNonce ||
                !expected->second.authStarted ||
                !expected->second.callbackOk ||
                expected->second.expiresAt <= nowMilliseconds)
            {
                return false;
            }
            gBindings.emplace(
                std::string(nativeConnectionNonce),
                ConnectionBinding{
                    steamId,
                    std::string(grantJti),
                    AddLifetime(
                        nowMilliseconds,
                        kConnectionBindingLifetimeMilliseconds)});
            gExpectedGrants.erase(expected);
            return true;
        }
        catch (...)
        {
            return false;
        }
    }

    bool WaitForExpectedGrantProof(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce,
        const std::uint64_t nowMilliseconds,
        const std::uint64_t timeoutMilliseconds) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId) ||
            !IsSafeGrantJti(grantJti) || nativeConnectionNonce.empty() ||
            nativeConnectionNonce.size() > 128U || timeoutMilliseconds == 0U)
        {
            return false;
        }
        (void)nowMilliseconds;
        try
        {
            std::unique_lock lock(gMutex);
            const auto ready = [&]() noexcept {
                PurgeExpiredLocked(static_cast<std::uint64_t>(GetTickCount64()));
                const auto expected = gExpectedGrants.find(steamId);
                if (expected == gExpectedGrants.end())
                    return true;
                return expected->second.grantJti == grantJti &&
                    expected->second.nativeConnectionNonce == nativeConnectionNonce &&
                    expected->second.authStarted && expected->second.callbackOk;
            };
            if (!gGameServerPump.joinable() || !ready())
            {
                if (!gGameServerPump.joinable())
                    return false;
            }
            if (!ready())
            {
                (void)gProofChanged.wait_for(
                    lock, std::chrono::milliseconds(timeoutMilliseconds), ready);
            }
            const auto expected = gExpectedGrants.find(steamId);
            return expected != gExpectedGrants.end() &&
                expected->second.grantJti == grantJti &&
                expected->second.nativeConnectionNonce == nativeConnectionNonce &&
                expected->second.authStarted && expected->second.callbackOk &&
                expected->second.expiresAt >
                    static_cast<std::uint64_t>(GetTickCount64());
        }
        catch (...)
        {
            return false;
        }
    }

    bool IsConnectionBindingActive(
        const std::string_view platformId,
        const std::string_view nativeConnectionNonce,
        const std::uint64_t nowMilliseconds) noexcept
    {
        std::uint64_t steamId = 0;
        if (nativeConnectionNonce.empty() ||
            !ParseDecimalSteamId(platformId, steamId))
        {
            return false;
        }
        std::lock_guard lock(gMutex);
        PurgeExpiredLocked(nowMilliseconds);
        const auto it = gBindings.find(std::string(nativeConnectionNonce));
        return it != gBindings.end() && it->second.steamId == steamId &&
            it->second.expiresAt > nowMilliseconds;
    }

    void RevokeConnectionBinding(
        const std::string_view nativeConnectionNonce) noexcept
    {
        if (nativeConnectionNonce.empty())
            return;
        std::lock_guard lock(gMutex);
        gBindings.erase(std::string(nativeConnectionNonce));
    }

    bool TryConsumeRevokedConnectionNonce(
        std::string& nativeConnectionNonce) noexcept
    {
        nativeConnectionNonce.clear();
        std::lock_guard lock(gMutex);
        if (gRevokedConnectionNonces.empty())
            return false;
        nativeConnectionNonce = std::move(gRevokedConnectionNonces.front());
        gRevokedConnectionNonces.pop_front();
        return !nativeConnectionNonce.empty();
    }

    void ClearAllExpectedProofs() noexcept
    {
        std::unordered_map<std::string, std::uint64_t> sessions;
        SteamGameServerEndAuthSession endAuth = nullptr;
        void* server = nullptr;
        {
            std::lock_guard lock(gMutex);
            gExpectedGrants.clear();
            gBindings.clear();
            gRevokedConnectionNonces.clear();
            sessions.swap(gServerAuthSessions);
            endAuth = gSteamGameServerEndAuthSession;
            server = gSteamGameServer;
        }
        gProofChanged.notify_all();
        if (endAuth && server)
        {
            for (const auto& [nonce, steamId] : sessions)
            {
                (void)nonce;
                endAuth(server, steamId);
            }
        }
    }

    ClientTicketState InitializeClientTicket() noexcept
    {
        std::lock_guard lock(gMutex);
        if (gClientTicketCallbackRegistered && gSteamUser)
        {
            // A failed callback with no live request is terminal only for
            // that ticket handle.  Allow the next strictly scoped join to
            // request a fresh carrier; GetClientTicketState still reports
            // Unavailable until that new request has a matching callback.
            if (!gClientTicketRequested && !gClientTicketRequestInFlight)
                return ClientTicketState::Pending;
            return gClientTicketFailed
                ? ClientTicketState::Unavailable
                : (gClientTicketReady
                    ? ClientTicketState::Ready : ClientTicketState::Pending);
        }

        HMODULE steamApi = GetModuleHandleA("steam_api64.dll");
        if (!steamApi || !ResolveSteamCallbacksLocked(steamApi))
            return ClientTicketState::Unavailable;
        gSteamUserAccessor = reinterpret_cast<SteamUserAccessor>(
            GetProcAddress(steamApi, "SteamAPI_SteamUser_v023"));
        gSteamUserGetAuthTicket = reinterpret_cast<SteamUserGetAuthTicket>(
            GetProcAddress(steamApi, "SteamAPI_ISteamUser_GetAuthSessionTicket"));
        gSteamUserCancelAuthTicket = reinterpret_cast<SteamUserCancelAuthTicket>(
            GetProcAddress(steamApi, "SteamAPI_ISteamUser_CancelAuthTicket"));
        gSteamUser = gSteamUserAccessor ? gSteamUserAccessor() : nullptr;
        if (!gSteamUser || !gSteamUserGetAuthTicket ||
            !gSteamUserCancelAuthTicket)
        {
            gSteamUserAccessor = nullptr;
            gSteamUserGetAuthTicket = nullptr;
            gSteamUserCancelAuthTicket = nullptr;
            gSteamUser = nullptr;
            return ClientTicketState::Unavailable;
        }

        gClientTicketCallbackVTable = CallbackVTable{
            &OnTicketCallback,
            &OnTicketCallbackWithCallResult,
            &TicketCallbackSize};
        gClientTicketCallback.vtable = &gClientTicketCallbackVTable;
        gClientTicketCallback.callbackFlags = 0U;
        gClientTicketCallback.reserved[0] = 0;
        gClientTicketCallback.reserved[1] = 0;
        gClientTicketCallback.reserved[2] = 0;
        gClientTicketCallback.callbackId = static_cast<std::int32_t>(
            kGetAuthSessionTicketResponseCallback);
        gRegisterCallback(
            &gClientTicketCallback,
            static_cast<int>(kGetAuthSessionTicketResponseCallback));
        gClientTicketCallbackRegistered = true;
        return ClientTicketState::Pending;
    }

    ClientTicketState RequestClientAuthTicket() noexcept
    {
        const ClientTicketState initialized = InitializeClientTicket();
        if (initialized == ClientTicketState::Unavailable)
            return initialized;

        void* user = nullptr;
        SteamUserGetAuthTicket getTicket = nullptr;
        SteamUserCancelAuthTicket cancelTicket = nullptr;
        {
            std::lock_guard lock(gMutex);
            if (gClientTicketReady)
                return ClientTicketState::Ready;
            if (gClientTicketRequested || gClientTicketRequestInFlight)
                return gClientTicketFailed
                    ? ClientTicketState::Unavailable
                    : ClientTicketState::Pending;

            if (gShuttingDown || !gSteamUser ||
                !gSteamUserGetAuthTicket)
            {
                return ClientTicketState::Unavailable;
            }
            user = gSteamUser;
            getTicket = gSteamUserGetAuthTicket;
            cancelTicket = gSteamUserCancelAuthTicket;
            gClientTicketRequestInFlight = true;
            gClientTicketCancellationRequested = false;
            gEarlyTicketCallbackCount = 0;
        }

        std::array<std::uint8_t, NativeLoginGrantPolicy::MaxSteamTicketBytes>
            ticket{};
        std::uint32_t ticketBytes = 0;
        const std::uint32_t handle = getTicket(
            user,
            ticket.data(),
            static_cast<int>(ticket.size()),
            &ticketBytes,
            nullptr);
        if (handle == 0U || ticketBytes == 0U ||
            ticketBytes > ticket.size())
        {
            {
                std::lock_guard lock(gMutex);
                gClientTicketRequestInFlight = false;
                gClientTicketCancellationRequested = false;
                gEarlyTicketCallbackCount = 0;
                gClientTicketRequested = false;
                gClientTicketReady = false;
                gClientTicketFailed = true;
                gClientTicketHandle = 0;
                SecureClearTicketBytes(gClientTicket);
            }
            if (handle != 0U && cancelTicket)
                cancelTicket(user, handle);
            SecureZeroMemory(ticket.data(), ticket.size());
            return ClientTicketState::Unavailable;
        }

        bool cancelReturnedHandle = false;
        ClientTicketState resultState = ClientTicketState::Pending;
        try
        {
            std::lock_guard lock(gMutex);
            const bool cancellationRequested =
                gClientTicketCancellationRequested || gShuttingDown;
            if (cancellationRequested)
            {
                gClientTicketRequested = false;
                gClientTicketReady = false;
                gClientTicketFailed = false;
                gClientTicketHandle = 0;
                SecureClearTicketBytes(gClientTicket);
                cancelReturnedHandle = true;
                resultState = ClientTicketState::Unavailable;
            }
            else
            {
                gClientTicket.assign(ticket.begin(), ticket.begin() + ticketBytes);
                gClientTicketHandle = handle;
                gClientTicketRequested = true;
                gClientTicketReady = false;
                gClientTicketFailed = false;

                bool matchedEarlyCallback = false;
                std::int32_t earlyResult = -1;
                for (std::size_t index = 0;
                     index < gEarlyTicketCallbackCount;
                     ++index)
                {
                    if (gEarlyTicketCallbacks[index].handle == handle)
                    {
                        matchedEarlyCallback = true;
                        earlyResult = gEarlyTicketCallbacks[index].result;
                        break;
                    }
                }
                if (matchedEarlyCallback)
                {
                    if (earlyResult == kEResultOk &&
                        !gClientTicket.empty())
                    {
                        gClientTicketReady = true;
                        resultState = ClientTicketState::Ready;
                    }
                    else
                    {
                        gClientTicketRequested = false;
                        gClientTicketReady = false;
                        gClientTicketFailed = true;
                        gClientTicketHandle = 0;
                        SecureClearTicketBytes(gClientTicket);
                        cancelReturnedHandle = true;
                        resultState = ClientTicketState::Unavailable;
                    }
                }
            }
            gEarlyTicketCallbackCount = 0;
            gClientTicketRequestInFlight = false;
            gClientTicketCancellationRequested = false;
            SecureZeroMemory(ticket.data(), ticket.size());
        }
        catch (...)
        {
            {
                std::lock_guard lock(gMutex);
                SecureClearTicketBytes(gClientTicket);
                gEarlyTicketCallbackCount = 0;
                gClientTicketRequestInFlight = false;
                gClientTicketCancellationRequested = false;
                gClientTicketRequested = false;
                gClientTicketReady = false;
                gClientTicketFailed = true;
                gClientTicketHandle = 0;
            }
            if (handle != 0U && cancelTicket)
                cancelTicket(user, handle);
            SecureZeroMemory(ticket.data(), ticket.size());
            return ClientTicketState::Unavailable;
        }
        if (cancelReturnedHandle && cancelTicket)
            cancelTicket(user, handle);
        return resultState;
    }

    ClientTicketState GetClientTicketState() noexcept
    {
        std::lock_guard lock(gMutex);
        if (gClientTicketFailed || !gClientTicketRequested)
            return ClientTicketState::Unavailable;
        return gClientTicketReady
            ? ClientTicketState::Ready : ClientTicketState::Pending;
    }

    bool CopyClientAuthTicket(std::string& encodedTicket) noexcept
    {
        encodedTicket.clear();
        try
        {
            std::lock_guard lock(gMutex);
            if (!gClientTicketReady || gClientTicket.empty())
                return false;
            return NativeLoginGrantPolicy::EncodeSteamTicket(
                gClientTicket.data(), gClientTicket.size(), encodedTicket);
        }
        catch (...)
        {
            encodedTicket.clear();
            return false;
        }
    }

    void CancelClientAuthTicket() noexcept
    {
        SteamUserCancelAuthTicket cancel = nullptr;
        void* user = nullptr;
        std::uint32_t handle = 0;
        {
            std::lock_guard lock(gMutex);
            cancel = gSteamUserCancelAuthTicket;
            user = gSteamUser;
            handle = gClientTicketHandle;
            if (gClientTicketRequestInFlight)
                gClientTicketCancellationRequested = true;
            gClientTicketRequested = false;
            gClientTicketReady = false;
            gClientTicketFailed = false;
            gClientTicketHandle = 0;
            if (!gClientTicketRequestInFlight)
            {
                gClientTicketCancellationRequested = false;
                gEarlyTicketCallbackCount = 0;
            }
            SecureClearTicketBytes(gClientTicket);
        }
        if (cancel && user && handle != 0U)
            cancel(user, handle);
    }

    bool BeginServerAuthSession(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce,
        const std::uint8_t* ticketBytes,
        const std::size_t ticketByteCount,
        const std::uint64_t nowMilliseconds) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId) ||
            !IsSafeGrantJti(grantJti) || nativeConnectionNonce.empty() ||
            nativeConnectionNonce.size() > 128U || !ticketBytes ||
            ticketByteCount == 0U ||
            ticketByteCount > NativeLoginGrantPolicy::MaxSteamTicketBytes)
        {
            return false;
        }
        SteamGameServerBeginAuthSession beginAuth = nullptr;
        SteamGameServerEndAuthSession endAuth = nullptr;
        void* server = nullptr;
        bool endStartedSession = false;
        bool beginSucceeded = false;
        try
        {
            {
                std::lock_guard lock(gMutex);
                PurgeExpiredLocked(nowMilliseconds);
                const auto expected = gExpectedGrants.find(steamId);
                if (expected == gExpectedGrants.end() ||
                    expected->second.grantJti != grantJti ||
                    expected->second.nativeConnectionNonce != nativeConnectionNonce ||
                    expected->second.authStarted || !gSteamGameServer ||
                    !gSteamGameServerBeginAuthSession)
                {
                    return false;
                }
                expected->second.authStarted = true;
                beginAuth = gSteamGameServerBeginAuthSession;
                endAuth = gSteamGameServerEndAuthSession;
                server = gSteamGameServer;
            }
            const int result = beginAuth(
                server,
                ticketBytes,
                static_cast<int>(ticketByteCount),
                steamId);
            if (result != 0)
            {
                std::lock_guard lock(gMutex);
                const auto expected = gExpectedGrants.find(steamId);
                if (expected != gExpectedGrants.end() &&
                    expected->second.grantJti == grantJti &&
                    expected->second.nativeConnectionNonce == nativeConnectionNonce)
                {
                    gExpectedGrants.erase(expected);
                }
                return false;
            }
            beginSucceeded = true;
            std::lock_guard lock(gMutex);
            const auto expected = gExpectedGrants.find(steamId);
            if (expected == gExpectedGrants.end() ||
                expected->second.grantJti != grantJti ||
                expected->second.nativeConnectionNonce != nativeConnectionNonce)
            {
                endStartedSession = true;
            }
            else
            {
                gServerAuthSessions[std::string(nativeConnectionNonce)] = steamId;
                return true;
            }
        }
        catch (...)
        {
            if (beginSucceeded && endAuth && server)
                endAuth(server, steamId);
            return false;
        }
        if (endStartedSession && endAuth && server)
            endAuth(server, steamId);
        return false;
    }

    void EndServerAuthSession(
        const std::string_view nativeConnectionNonce) noexcept
    {
        if (nativeConnectionNonce.empty())
            return;
        SteamGameServerEndAuthSession endAuth = nullptr;
        void* server = nullptr;
        std::uint64_t steamId = 0;
        {
            std::lock_guard lock(gMutex);
            const auto session = gServerAuthSessions.find(
                std::string(nativeConnectionNonce));
            if (session == gServerAuthSessions.end())
                return;
            steamId = session->second;
            gServerAuthSessions.erase(session);
            endAuth = gSteamGameServerEndAuthSession;
            server = gSteamGameServer;
        }
        if (endAuth && server)
            endAuth(server, steamId);
    }

    void CancelExpectedGrant(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId))
            return;
        std::lock_guard lock(gMutex);
        const auto expected = gExpectedGrants.find(steamId);
        if (expected != gExpectedGrants.end() &&
            expected->second.grantJti == grantJti &&
            expected->second.nativeConnectionNonce == nativeConnectionNonce)
        {
            gExpectedGrants.erase(expected);
            gProofChanged.notify_all();
        }
    }

#if defined(STRICT_ROSTER_STEAM_AUTH_TEST)
    bool MarkExpectedGrantAuthStartedForTest(
        const std::string_view platformId,
        const std::string_view grantJti,
        const std::string_view nativeConnectionNonce) noexcept
    {
        std::uint64_t steamId = 0;
        if (!ParseDecimalSteamId(platformId, steamId))
            return false;
        std::lock_guard lock(gMutex);
        const auto expected = gExpectedGrants.find(steamId);
        if (expected == gExpectedGrants.end() ||
            expected->second.grantJti != grantJti ||
            expected->second.nativeConnectionNonce != nativeConnectionNonce)
        {
            return false;
        }
        expected->second.authStarted = true;
        return true;
    }
#endif

    bool CallbackRegistered() noexcept
    {
        std::lock_guard lock(gMutex);
        return gRegistered;
    }
}
