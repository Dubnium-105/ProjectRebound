// ======================================================
//  LateJoinManager — 中途加入（Mid-Game Join）实现
// ======================================================
//
//  中途加入流程概述：
//    1. PostLogin 检测 → 判定为中途加入 → 注册到 LateJoinPlayers
//    2. 延迟 1s 发送 ClientStart 序列，让客户端"追上"比赛状态
//    3. 客户端选择角色 → 拦截 ServerConfirmRoleSelection → 状态推进
//    4. 每隔 2s 尝试生成 Pawn（最多 3 次，逐步回退）
//    5. 检测到非旁观者 Pawn → 强制 Possess + 通知客户端进入 Playing
//
//  与其他系统的关系：
//    - PlayerRespawnAllowedMap：与 Respawn/Death 系统共享，
//      LateJoin 设置为 true 允许重生，ClientBeKilled 设置为 false 阻止重生
//    - DidProcStartMatch：由匹配流程设置，LateJoin 只读取

#include "LateJoinManager.h"
#include "ServerLogic.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include <iostream>
#include <vector>

using namespace SDK;

namespace
{
    std::string DescribePlayerRoleIdentity(APBPlayerController* PC)
    {
        if (!PC || !PC->PBPlayerState)
            return "selected=<missing> possessed=<missing>";
        try
        {
            return "selected=" + PC->PBPlayerState->SelectedCharacterID.ToString() +
                " possessed=" + PC->PBPlayerState->PossessedCharacterId.ToString();
        }
        catch (...)
        {
            return "selected=<unreadable> possessed=<unreadable>";
        }
    }

    bool IsCurrentServerConnection(APBPlayerController* PC)
    {
        return PC && ConnectedPlayerControllers.contains(PC) &&
            !DisconnectedPlayerControllers.contains(PC) &&
            !PC->bActorIsBeingDestroyed;
    }

    class ScopedRestartPermit
    {
    public:
        ScopedRestartPermit(
            APBPlayerController*& slot,
            int& depth,
            APBPlayerController* player)
            : Slot(slot), Depth(depth), Previous(slot)
        {
            Slot = player;
            ++Depth;
        }

        ~ScopedRestartPermit()
        {
            if (Depth > 0) --Depth;
            Slot = Previous;
        }

    private:
        APBPlayerController*& Slot;
        int& Depth;
        APBPlayerController* Previous;
    };

    class ScopedSpawnDispatchCompletion
    {
    public:
        ScopedSpawnDispatchCompletion(
            const std::function<void(APBPlayerController*)>& callback,
            APBPlayerController* playerController)
            : Callback(callback), PlayerController(playerController)
        {
        }

        ~ScopedSpawnDispatchCompletion()
        {
            if (Callback) Callback(PlayerController);
        }

    private:
        const std::function<void(APBPlayerController*)>& Callback;
        APBPlayerController* PlayerController = nullptr;
    };
}

// =====================================================================
//  构造函数
// =====================================================================

LateJoinManager::LateJoinManager(
    const bool& InDidProcStartMatch,
    const bool& InDidBroadcastRoleSelection,
    std::unordered_map<APBPlayerController*, bool>& InPlayerRespawnAllowedMap,
    FReportRoomStarted InReportRoomStarted,
    FCanReleasePlayerSpawn InCanReleasePlayerSpawn,
    FSpawnDispatchNotification InBeginSpawnDispatch,
    FSpawnDispatchNotification InCompleteSpawnDispatch,
    FSpawnDispatchNotification InFinalizeSpawnRequest,
    FSpawnDispatchNotification InAbandonSpawnRequest
)
    : DidProcStartMatch(InDidProcStartMatch)
    , DidBroadcastRoleSelection(InDidBroadcastRoleSelection)
    , PlayerRespawnAllowedMap(InPlayerRespawnAllowedMap)
    , ReportRoomStarted(std::move(InReportRoomStarted))
    , CanReleasePlayerSpawn(std::move(InCanReleasePlayerSpawn))
    , BeginSpawnDispatch(std::move(InBeginSpawnDispatch))
    , CompleteSpawnDispatch(std::move(InCompleteSpawnDispatch))
    , FinalizeSpawnRequest(std::move(InFinalizeSpawnRequest))
    , AbandonSpawnRequest(std::move(InAbandonSpawnRequest))
{
}

// =====================================================================
//  公有接口 — Hook 层调用入口
// =====================================================================

// @brief PostLogin Hook 入口。
//  检测新连接玩家是否需要中途加入流程：
//  - 若比赛已开始或回合进行中 → 注册为中途加入玩家并返回 true
//  - 否则返回 false，由调用方执行正常首生逻辑
bool LateJoinManager::OnPostLogin(AGameMode* GameMode, APBPlayerController* PC)
{
    if (PC && IsLateJoinWindowOpen())
    {
        QueueLateJoinPlayer(PC);

        // 通知后端房间已启动（如中途加入玩家触发了首次上报）
        if (ReportRoomStarted)
            ReportRoomStarted();

        return true;
    }

    return false;
}

// @brief ProcessEvent Hook 入口。
//  拦截以下 RPC 并处理：
//    - CanPlayerSelectRole     → 对中途加入玩家强制返回 true
//    - CanSelectRole           → 对中途加入玩家强制返回 true
//    - ServerConfirmRoleSelection → 执行原函数 + 装备覆盖 + 推进状态机
//  返回 true 表示已完全拦截，调用方应 return 跳过原始 ProcessEvent。
bool LateJoinManager::OnProcessEvent(UObject* Object, const std::string& functionName, void* Parms)
{
    // ---- CanPlayerSelectRole（PBGameMode 级别）----
    // 中途加入玩家在回合进行中原本无法选择角色，此处强制放行
    if (functionName.contains("CanPlayerSelectRole"))
    {
        auto* RoleParms = (Params::PBGameMode_CanPlayerSelectRole*)Parms;
        const auto tracked = RoleParms
            ? LateJoinPlayers.find(RoleParms->Player)
            : LateJoinPlayers.end();
        if (RoleParms && tracked != LateJoinPlayers.end() &&
            (tracked->second.State == ELateJoinState::PendingRoleSelection ||
             tracked->second.State == ELateJoinState::AwaitingRespawnInput))
        {
            RoleParms->ReturnValue = true;
            return true;    // 已拦截，跳过原始调用
        }
    }

    // ---- CanSelectRole（PBPlayerController 级别）----
    // 同上，对中途加入玩家强制允许
    if (functionName.contains("CanSelectRole"))
    {
        APBPlayerController* PBPlayerController = Object && Object->IsA(APBPlayerController::StaticClass())
            ? (APBPlayerController*)Object
            : nullptr;

        const auto tracked = LateJoinPlayers.find(PBPlayerController);
        if (tracked != LateJoinPlayers.end() &&
            (tracked->second.State == ELateJoinState::PendingRoleSelection ||
             tracked->second.State == ELateJoinState::AwaitingRespawnInput))
        {
            auto* RoleParms = (Params::PBPlayerController_CanSelectRole*)Parms;
            if (RoleParms)
            {
                RoleParms->ReturnValue = true;
                return true;    // 已拦截
            }
        }
    }

    // 注意：ServerConfirmRoleSelection 不在此处处理。
    // 该 RPC 需要调用方先执行原始 ProcessEvent.call 再推进状态，
    // 因此由调用方在 Hook 体内使用 IsLateJoinPlayer() + OnRoleConfirmed() 处理。

    return false;   // 未拦截，调用方继续正常流程
}

// @brief ServerConfirmRoleSelection 后的状态推进
//  将中途加入玩家的状态从 PendingRoleSelection 推进到 RoleConfirmed，
//  并重置生成计时器。
void LateJoinManager::OnRoleConfirmed(
    APBPlayerController* PC,
    const std::string& roleId)
{
    auto it = LateJoinPlayers.find(PC);
    if (it == LateJoinPlayers.end() || roleId.empty() || roleId == "None")
        return;

    // ESC -> role selection is the explicit alternate to the native F respawn
    // prompt. A dead controller can still retain its old Pawn briefly, so do
    // not run the playable-Pawn/role-transition test in this state: the death
    // that would release that transition has already happened.
    if (it->second.State == ELateJoinState::AwaitingRespawnInput)
    {
        it->second.DesiredRoleId = roleId;
        it->second.State = ELateJoinState::RoleConfirmed;
        it->second.ElapsedSeconds = 0.0f;
        it->second.SpawnAttempts = 0;
        it->second.AwaitingRoleTransitionDeath = false;
        PlayerRespawnAllowedMap[PC] = false;
        std::cout << "[LATEJOIN] Post-death role selection confirmed; scheduling spawn."
            << std::endl;
        return;
    }

    it->second.DesiredRoleId = roleId;
    const auto respawn = PlayerRespawnAllowedMap.find(PC);
    const bool respawnWasBlocked =
        respawn != PlayerRespawnAllowedMap.end() && !respawn->second;
    const bool anyCurrentPawnPlayable = HasPlayableLateJoinPawn(PC);
    it = LateJoinPlayers.find(PC);
    if (it == LateJoinPlayers.end())
        return;
    const bool desiredPawnPlayable = anyCurrentPawnPlayable &&
        HasPlayableLateJoinPawn(PC, roleId);
    // The alive check may synchronously tear down the controller through gameplay
    // callbacks. Never retain an unordered_map iterator across an SDK call.
    it = LateJoinPlayers.find(PC);
    if (it == LateJoinPlayers.end())
        return;

    if (desiredPawnPlayable && !respawnWasBlocked)
    {
        // Covers both an already-Spawned connection and the one-frame window
        // where RestartPlayers produced a Pawn but Tick has not marked it yet.
        std::cout << "[LATEJOIN] Ignored role confirmation while current pawn is playable."
            << std::endl;
        return;
    }
    const bool bInitialJoin = it->second.bIsInitialJoin;
    it->second.State = ELateJoinState::RoleConfirmed;
    it->second.ElapsedSeconds = 0.0f;
    it->second.SpawnAttempts = 0;
    it->second.AwaitingRoleTransitionDeath =
        anyCurrentPawnPlayable && !desiredPawnPlayable;
    if (it->second.AwaitingRoleTransitionDeath)
        PlayerRespawnAllowedMap[PC] = false;
    std::cout << "[LATEJOIN] Role confirmed; scheduling "
        << (bInitialJoin ? "initial-join" : "single-player")
        << " spawn." << std::endl;
}

void LateJoinManager::OnPlayerKilled(APBPlayerController* PC)
{
    auto it = LateJoinPlayers.find(PC);
    if (!IsCurrentServerConnection(PC) || it == LateJoinPlayers.end())
        return;

    if (it->second.AwaitingRoleTransitionDeath)
    {
        it->second.AwaitingRoleTransitionDeath = false;
        it->second.State = ELateJoinState::RoleConfirmed;
        it->second.ElapsedSeconds = 0.0f;
        it->second.SpawnAttempts = 0;
        return;
    }

    if (!it->second.HasCompletedSpawn)
        return;

    if (it->second.State == ELateJoinState::AwaitingRespawnInput)
        return;

    // Preserve the current role and loadout while the native death UI waits
    // for player intent. F emits the normal restart RPC and is serialized by
    // QueueManagedRespawn; ESC opens the game's own role-selection screen and
    // eventually reaches OnRoleConfirmed. Do not synthesize ClientSelectRole
    // here, because that bypasses the native F/ESC choice.
    it->second.State = ELateJoinState::AwaitingRespawnInput;
    it->second.ElapsedSeconds = 0.0f;
    it->second.SpawnAttempts = 0;
    it->second.AwaitingRoleTransitionDeath = false;
    // Keep GameMode wave/automatic restart entry points closed. The explicit
    // client ServerRestartPlayer/ServerQuickRespawn RPC is allowed to convert
    // this state into a managed spawn by the hook layer.
    PlayerRespawnAllowedMap[PC] = false;
    std::cout << "[LATEJOIN] Player death is awaiting native F/ESC respawn input."
        << std::endl;
}

bool LateJoinManager::CanQueueManagedRespawn(APBPlayerController* PC) const
{
    auto it = LateJoinPlayers.find(PC);
    if (!IsCurrentServerConnection(PC) || it == LateJoinPlayers.end())
        return false;

    // HasCompletedSpawn is monotonic for a connection. It is the authority
    // that distinguishes a real death/round restart from an engine callback
    // occurring during initial role selection or the first spawn attempt.
    // Keep the explicit state check as a defence against a future transition
    // accidentally carrying the flag into a fresh PendingRoleSelection state.
    return it->second.HasCompletedSpawn &&
        it->second.State != ELateJoinState::PendingRoleSelection;
}

bool LateJoinManager::QueueManagedRespawn(APBPlayerController* PC)
{
    if (!CanQueueManagedRespawn(PC))
        return false;

    auto it = LateJoinPlayers.find(PC);
    if (it == LateJoinPlayers.end())
        return false;

    if (!it->second.AwaitingRoleTransitionDeath)
    {
        it->second.State = ELateJoinState::RoleConfirmed;
        it->second.ElapsedSeconds = 0.0f;
        it->second.SpawnAttempts = 0;
    }
    // Close every native restart path until Tick has acquired the per-role
    // loadout lease and installed the matching inventory seed.
    PlayerRespawnAllowedMap[PC] = false;
    return true;
}

bool LateJoinManager::IsManagedPlayer(APBPlayerController* PC) const
{
    return PC && LateJoinPlayers.contains(PC);
}

bool LateJoinManager::IsAwaitingRespawnInput(APBPlayerController* PC) const
{
    const auto it = LateJoinPlayers.find(PC);
    return PC && it != LateJoinPlayers.end() &&
        it->second.State == ELateJoinState::AwaitingRespawnInput;
}

bool LateJoinManager::HasManagedRestartPermit(APBPlayerController* PC) const
{
    return PC && ManagedRestartPermitDepth > 0 && ManagedRestartPermit == PC;
}

// @brief 每帧驱动中途加入状态机。
//  由 TickFlush Hook 调用，遍历所有已注册的中途加入玩家，
//  根据当前阶段推进状态或执行超时清理。
void LateJoinManager::Tick(float DeltaTime)
{
    // Every SDK call below may synchronously invoke Logout/Destroy and erase a
    // player from LateJoinPlayers. Iterate a key snapshot and re-find the
    // entry after each such call; never retain an iterator or reference across
    // an engine boundary.
    std::vector<APBPlayerController*> players;
    players.reserve(LateJoinPlayers.size());
    for (const auto& entry : LateJoinPlayers)
        players.push_back(entry.first);

    for (APBPlayerController* PC : players)
    {
        if (!PC)
        {
            LateJoinPlayers.erase(nullptr);
            continue;
        }

        auto it = LateJoinPlayers.find(PC);
        if (it == LateJoinPlayers.end())
            continue;
        it->second.ElapsedSeconds += DeltaTime;

        // ---- 检测生成成功 ----
        // 如果角色已确认且已尝试过生成，且现在拥有非旁观者 Pawn，则完成
        if (it->second.State == ELateJoinState::RoleConfirmed &&
            it->second.SpawnAttempts > 0)
        {
            const std::string desiredRoleId = it->second.DesiredRoleId;
            const bool hasPlayablePawn =
                HasPlayableLateJoinPawn(PC, desiredRoleId);
            it = LateJoinPlayers.find(PC);
            if (it == LateJoinPlayers.end())
                continue;

            if (hasPlayablePawn)
            {
                const FLateJoinInfo snapshot = it->second;
                FinalizeLateJoinSpawn(PC, snapshot);
                it = LateJoinPlayers.find(PC);
                if (it == LateJoinPlayers.end())
                    continue;

                it->second.State = ELateJoinState::Spawned;
                it->second.HasCompletedSpawn = true;
                if (FinalizeSpawnRequest) FinalizeSpawnRequest(PC);
                if (!IsCurrentServerConnection(PC) ||
                    !LateJoinPlayers.contains(PC))
                {
                    continue;
                }
                std::cout << "[LATEJOIN] Spawn complete for late join player." << std::endl;
                continue;
            }
        }

        it = LateJoinPlayers.find(PC);
        if (it == LateJoinPlayers.end())
            continue;

        // ---- Phase 1: 等待角色选择 ----
        if (it->second.State == ELateJoinState::PendingRoleSelection)
        {
            // Initial players remain in the native pre-match role-selection
            // lifecycle. Sending the mid-game sequence here would announce a
            // match/round before StartMatch and can suppress native role
            // confirmation. K2_OnLogout bounds this wait instead of the
            // mid-game role-selection timeout.
            if (it->second.bIsInitialJoin)
            {
                // ClientSelectRole is normally a one-shot server-wide
                // broadcast. Prompt players that connected after it, and
                // retry controllers that were not ready during the scan.
                const bool shouldPrompt = DidBroadcastRoleSelection &&
                    !it->second.InitialRoleSelectionSent &&
                    it->second.ElapsedSeconds >= CLIENT_START_DELAY_SEC;
                bool canSelectRole = false;
                if (shouldPrompt)
                    canSelectRole = PC->CanSelectRole();

                it = LateJoinPlayers.find(PC);
                if (it == LateJoinPlayers.end())
                    continue;
                if (shouldPrompt && canSelectRole &&
                    it->second.State == ELateJoinState::PendingRoleSelection &&
                    it->second.bIsInitialJoin &&
                    !it->second.InitialRoleSelectionSent)
                {
                    PC->ClientSelectRole();
                    it = LateJoinPlayers.find(PC);
                    if (it == LateJoinPlayers.end())
                        continue;
                    it->second.InitialRoleSelectionSent = true;
                    std::cout << "[LATEJOIN] Sent missed initial role-selection prompt." << std::endl;
                }
                continue;
            }

            // 延迟 1s 后发送 ClientStart 序列（等待连接稳定）
            if (!it->second.ClientStartSent &&
                it->second.ElapsedSeconds >= CLIENT_START_DELAY_SEC)
            {
                SendLateJoinClientStart(PC);
                it = LateJoinPlayers.find(PC);
                if (it == LateJoinPlayers.end())
                    continue;
                it->second.ClientStartSent = true;
                it->second.ElapsedSeconds = 0.0f;
            }
            // 超时 30s 未选择角色 → 放弃
            else if (it->second.ClientStartSent &&
                it->second.ElapsedSeconds >= ROLE_SELECTION_TIMEOUT)
            {
                it->second.State = ELateJoinState::TimedOut;
                if (AbandonSpawnRequest) AbandonSpawnRequest(PC);
                std::cout << "[LATEJOIN] Timed out waiting for role selection." << std::endl;
            }
        }
        // ---- Phase 2: 角色已确认，尝试生成 Pawn ----
        else if (it->second.State == ELateJoinState::RoleConfirmed)
        {
            if (it->second.AwaitingRoleTransitionDeath)
            {
                const std::string desiredRoleId = it->second.DesiredRoleId;
                const bool desiredPawnPlayable =
                    HasPlayableLateJoinPawn(PC, desiredRoleId);
                it = LateJoinPlayers.find(PC);
                if (it == LateJoinPlayers.end())
                    continue;
                if (desiredPawnPlayable)
                {
                    it->second.AwaitingRoleTransitionDeath = false;
                    it->second.State = ELateJoinState::Spawned;
                    it->second.HasCompletedSpawn = true;
                    if (FinalizeSpawnRequest) FinalizeSpawnRequest(PC);
                    if (!IsCurrentServerConnection(PC) ||
                        !LateJoinPlayers.contains(PC))
                    {
                        continue;
                    }
                    continue;
                }

                const bool oldPawnStillPlayable =
                    HasPlayableLateJoinPawn(PC);
                it = LateJoinPlayers.find(PC);
                if (it == LateJoinPlayers.end())
                    continue;
                if (oldPawnStillPlayable)
                    continue;
                it->second.AwaitingRoleTransitionDeath = false;
                it->second.ElapsedSeconds = 0.0f;
            }

            // 首次立即尝试，之后每 2s 重试
            if (it->second.SpawnAttempts == 0 ||
                it->second.ElapsedSeconds >= SPAWN_RETRY_INTERVAL)
            {
                if (it->second.SpawnAttempts < MAX_SPAWN_ATTEMPTS)
                {
                    // Reassert the per-player inventory immediately before
                    // this concrete spawn attempt. The FieldMod cache is
                    // world+role scoped, so LoadoutManager also serializes two
                    // players choosing the same role until InventorySpawned.
                    const bool canRelease =
                        !CanReleasePlayerSpawn || CanReleasePlayerSpawn(PC);
                    it = LateJoinPlayers.find(PC);
                    if (it == LateJoinPlayers.end())
                        continue;
                    if (canRelease &&
                        it->second.State == ELateJoinState::RoleConfirmed)
                        RequestLateJoinSpawn(PC);
                }
                else
                {
                    it->second.State = ELateJoinState::TimedOut;
                    if (AbandonSpawnRequest) AbandonSpawnRequest(PC);
                    std::cout << "[LATEJOIN] Timed out spawning late join player." << std::endl;
                }
            }
        }

        // ---- 终态清理 ----
        it = LateJoinPlayers.find(PC);
        if (it != LateJoinPlayers.end() &&
            it->second.State == ELateJoinState::TimedOut)
        {
            // An initial participant must remain visible to the StartMatch
            // readiness gate. Erasing it would silently count a failed spawn
            // as ready; a later role confirmation can reset and retry it.
            if (!it->second.bIsInitialJoin)
                LateJoinPlayers.erase(it);
        }
    }
}

// @brief 查询指定 PC 是否为中途加入玩家（排除初始加入）
bool LateJoinManager::IsLateJoinPlayer(APBPlayerController* PC) const
{
    if (!PC)
        return false;
    auto it = LateJoinPlayers.find(PC);
    return it != LateJoinPlayers.end() && !it->second.bIsInitialJoin;
}

// @brief 查询指定 PC 是否为初始加入玩家（注册到延迟生成流程但比赛未开始时连接）
bool LateJoinManager::IsInitialJoinPlayer(APBPlayerController* PC) const
{
    if (!PC)
        return false;
    auto it = LateJoinPlayers.find(PC);
    return it != LateJoinPlayers.end() && it->second.bIsInitialJoin;
}

void LateJoinManager::OnPlayerDisconnected(APBPlayerController* PC)
{
    if (!PC)
        return;

    LateJoinPlayers.erase(PC);
    PlayerRespawnAllowedMap.erase(PC);
}

void LateJoinManager::OnRoleSelectionPromptSent(APBPlayerController* PC)
{
    auto it = LateJoinPlayers.find(PC);
    if (it != LateJoinPlayers.end() && it->second.bIsInitialJoin)
        it->second.InitialRoleSelectionSent = true;
}

void LateJoinManager::ResetForWorldChange()
{
    LateJoinPlayers.clear();
    ManagedRestartPermit = nullptr;
    ManagedRestartPermitDepth = 0;
}

// @brief 查询中途加入窗口是否开放
//  条件：比赛已调用 StartMatch（DidProcStartMatch）或回合正在进行中
bool LateJoinManager::IsLateJoinWindowOpen() const
{
    return DidProcStartMatch || IsRoundCurrentlyInProgress();
}

bool LateJoinManager::CanRestartBeforeMatch(APBPlayerController* PC) const
{
    auto it = LateJoinPlayers.find(PC);
    return it != LateJoinPlayers.end() && it->second.bIsInitialJoin &&
        it->second.State == ELateJoinState::RoleConfirmed;
}

bool LateJoinManager::AreInitialPlayersReadyForStart() const
{
    for (const auto& entry : LateJoinPlayers)
    {
        const FLateJoinInfo& info = entry.second;
        if (info.bIsInitialJoin && info.State != ELateJoinState::Spawned)
            return false;
    }
    return true;
}

// =====================================================================
//  私有方法 — 状态查询
// =====================================================================

APBGameState* LateJoinManager::GetPBGameState() const
{
    UWorld* World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode || !World->AuthorityGameMode->GameState)
        return nullptr;

    return (APBGameState*)World->AuthorityGameMode->GameState;
}

APBGameMode* LateJoinManager::GetPBGameMode() const
{
    UWorld* World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return nullptr;

    return (APBGameMode*)World->AuthorityGameMode;
}

bool LateJoinManager::IsRoundCurrentlyInProgress() const
{
    APBGameState* GameState = GetPBGameState();
    return GameState && GameState->IsRoundInProgress();
}

// @brief 判断 Pawn 是否为旁观者（SpectatorPawn）
bool LateJoinManager::IsSpectatorPawn(APawn* Pawn)
{
    return Pawn && Pawn->IsA(ASpectatorPawn::StaticClass());
}

// @brief 判断玩家是否拥有可玩的（非旁观者）Pawn
bool LateJoinManager::HasPlayableLateJoinPawn(
    APBPlayerController* PC,
    const std::string& desiredRoleId)
{
    if (!PC || !PC->Pawn || IsSpectatorPawn(PC->Pawn))
        return false;
    if (!PC->Pawn->IsA(APBCharacter::StaticClass()))
        return desiredRoleId.empty();
    auto* character = static_cast<APBCharacter*>(PC->Pawn);
    if (!desiredRoleId.empty() &&
        character->CharacterID.ToString() != desiredRoleId)
    {
        return false;
    }
    try
    {
        return character->IsAlive();
    }
    catch (...)
    {
        return false;
    }
}

// =====================================================================
//  私有方法 — 状态机动作
// =====================================================================

// @brief 将玩家注册为中途加入，初始化跟踪信息
void LateJoinManager::QueueLateJoinPlayer(APBPlayerController* PC)
{
    if (!IsCurrentServerConnection(PC))
        return;

    LateJoinPlayers[PC] = FLateJoinInfo{};
    // Mid-game joins must confirm a role before any native restart query can
    // produce a default pawn.
    PlayerRespawnAllowedMap[PC] = false;
    std::cout << "[LATEJOIN] Queued player for in-progress join: " << PC->GetFullName() << std::endl;
}

// @brief 将初始加入玩家注册到与中途加入一致的延迟生成流程。
//  初始加入同样走 ClientStart 序列 + 角色确认后生成，
//  统一客户端状态推进并确保武器在角色确认后创建。
void LateJoinManager::QueueInitialJoinPlayer(AGameMode* GameMode, APBPlayerController* PC)
{
    (void)GameMode;
    if (!IsCurrentServerConnection(PC))
        return;

    // 如果引擎已为该玩家创建了默认 Pawn，需要先清理掉，
    // 否则 LateJoinManager 会误判为"已生成"而不再创建新 Pawn，
    // 导致武器永远是默认配置。
    if (PC->Pawn)
    {
        std::cout << "[LATEJOIN] Clearing pre-existing pawn for initial join player: "
            << PC->Pawn->GetFullName() << std::endl;
        if (IsSpectatorPawn(PC->Pawn))
        {
            PC->ExitObserverState();
            if (!IsCurrentServerConnection(PC))
                return;
        }
        PC->UnPossess();
        if (!IsCurrentServerConnection(PC))
            return;
    }

    FLateJoinInfo info{};
    info.bIsInitialJoin = true;
    // No synthetic ClientStart is pending. The existing server-wide native
    // flow calls ClientSelectRole at the normal point in the countdown.
    info.ClientStartSent = true;
    LateJoinPlayers[PC] = info;
    // 阻止角色确认前的自动重生（ServerRestartPlayer 拦截会检查此表）
    // PrepareLateJoinRespawn 会在生成时将其设为 true
    PlayerRespawnAllowedMap[PC] = false;
    std::cout << "[LATEJOIN] Queued player for initial join (deferred spawn): "
        << PC->GetFullName() << std::endl;
}

// @brief 统一的客户端状态同步入口。
//  通过参数控制发送哪些 RPC，避免初始加入/中途加入逻辑漂移。
void LateJoinManager::SyncClientJoinState(APBPlayerController* PC, const FClientSyncOptions& Options)
{
    const auto isTracked = [this, PC]() {
        return IsCurrentServerConnection(PC) && LateJoinPlayers.contains(PC);
    };
    if (!isTracked())
        return;

    if (Options.SendStartOnlineGame)
    {
        PC->ClientStartOnlineGame();
        if (!isTracked()) return;
    }

    if (Options.SendMatchHasStarted)
    {
        PC->ClientMatchHasStarted();
        if (!isTracked()) return;
    }

    if (Options.SendRoundHasStarted)
    {
        PC->ClientRoundHasStarted();
        if (!isTracked()) return;
    }

    if (Options.SendNotifyGameStarted)
    {
        PC->NotifyGameStarted();
        if (!isTracked()) return;
    }

    if (Options.SendClientSelectRole)
    {
        PC->ClientSelectRole();
        if (!isTracked()) return;
    }

    if (Options.SendReadyAtStartSpot)
    {
        PC->ClientReadyAtStartSpot();
        if (!isTracked()) return;
    }

    if (Options.SendGotoPlaying)
    {
        PC->ClientGotoState(UKismetStringLibrary::Conv_StringToName(L"Playing"));
        if (!isTracked()) return;
    }

    if (Options.SendRestartAndAcknowledge)
    {
        PC->ClientRestart(PC->Pawn);
        if (!isTracked()) return;
        PC->ClientRetryClientRestart(PC->Pawn);
        if (!isTracked()) return;
        PC->ServerAcknowledgePossession(PC->Pawn);
    }
}

// @brief 向中途加入客户端发送"比赛已开始"的完整通知序列
//  模拟正常比赛启动时的 RPC 序列，让客户端 UI 状态追上
void LateJoinManager::SendLateJoinClientStart(APBPlayerController* PC)
{
    if (!PC)
        return;

    std::cout << "[LATEJOIN] Sending in-progress match state and role selection." << std::endl;
    FClientSyncOptions options{};
    options.SendStartOnlineGame = true;
    options.SendMatchHasStarted = true;
    options.SendRoundHasStarted = true;
    options.SendNotifyGameStarted = true;
    options.SendClientSelectRole = true;
    SyncClientJoinState(PC, options);
}

// @brief 准备重生前的清理工作
//  清除旁观者状态、解锁输入、释放旁观者 Pawn
void LateJoinManager::PrepareLateJoinRespawn(APBPlayerController* PC)
{
    const auto isTracked = [this, PC]() {
        return IsCurrentServerConnection(PC) && LateJoinPlayers.contains(PC);
    };
    if (!isTracked())
        return;

    // Do not clear spectator-waiting or input state before the controlled
    // restart. On this build ServerSetSpectatorWaiting(false) synchronously
    // enters the native default-respawn path: it publishes FIXER pre-ordering
    // and can possess a FIXER pawn before the requested role's RestartPlayers
    // dispatch begins. FinalizeLateJoinSpawn clears those flags only after a
    // live pawn with DesiredRoleId has been observed.
    PlayerRespawnAllowedMap[PC] = true;

    // 如果当前是旁观者 Pawn → 退出观察模式并释放
    if (PC->Pawn && IsSpectatorPawn(PC->Pawn))
    {
        std::cout << "[LATEJOIN] Clearing spectator pawn before playable spawn: "
            << PC->Pawn->GetFullName() << std::endl;
        // ExitObserverState can also advance the native respawn state machine.
        // Releasing possession is sufficient; the managed RestartPlayers call
        // below owns the only pre-spawn transition.
        PC->UnPossess();
    }
}

// @brief 生成成功后的最终化操作
//  强制 Possess、通知客户端进入 Playing 状态、确认占有
void LateJoinManager::FinalizeLateJoinSpawn(APBPlayerController* PC, FLateJoinInfo Info)
{
    const auto isTracked = [this, PC]() {
        return IsCurrentServerConnection(PC) && LateJoinPlayers.contains(PC);
    };
    if (!isTracked())
        return;
    const bool hasPlayablePawn =
        HasPlayableLateJoinPawn(PC, Info.DesiredRoleId);
    if (!hasPlayablePawn || !isTracked())
        return;

    // 确保重生许可和输入解锁（防御性重置）
    PlayerRespawnAllowedMap[PC] = true;
    PC->ServerSetSpectatorWaiting(false);
    if (!isTracked()) return;
    PC->ClientSetSpectatorWaiting(false);
    if (!isTracked()) return;
    PC->SetIgnoreMoveInput(false);
    if (!isTracked()) return;
    PC->SetIgnoreLookInput(false);
    if (!isTracked()) return;
    PC->ClientIgnoreMoveInput(false);
    if (!isTracked()) return;
    PC->ClientIgnoreLookInput(false);
    if (!isTracked()) return;
    PC->ExitObserverState();
    if (!isTracked()) return;

    // 强制 Possess（如果 Pawn 的 Controller 不是当前 PC）
    if (PC->Pawn)
    {
        if (PC->Pawn->Controller != (AController*)PC)
        {
            std::cout << "[LATEJOIN] Forcing possess on spawned pawn: "
                << PC->Pawn->GetFullName() << std::endl;
            PC->Possess(PC->Pawn);
            if (!isTracked()) return;
        }

        PC->Pawn->ForceNetUpdate();
        if (!isTracked()) return;
    }

    // Mid-game clients missed the original match lifecycle and need the full
    // synchronization sequence. Initial clients already participate in that
    // lifecycle; RestartPlayers owns their normal client restart and no
    // match/round/playing notification may be synthesized here.
    PC->ForceNetUpdate();
    if (!isTracked()) return;
    if (!Info.bIsInitialJoin && !Info.HasCompletedSpawn)
    {
        FClientSyncOptions options{};
        options.SendMatchHasStarted = true;
        options.SendRoundHasStarted = true;
        options.SendNotifyGameStarted = true;
        options.SendReadyAtStartSpot = true;
        options.SendGotoPlaying = true;
        options.SendRestartAndAcknowledge = true;
        SyncClientJoinState(PC, options);
        if (!isTracked()) return;
    }

    if (PC->Pawn)
        std::cout << "[LATEJOIN] Finalized playable possession: "
            << PC->Pawn->GetFullName() << std::endl;
}

// @brief 执行生成尝试（3 级回退策略）
//  Attempt 0: RestartPlayers — 标准引擎生成路径
//  Attempt 1: ServerQuickRespawn — 快速重生
//  Attempt 2: ServerSuicide — 自杀触发重生（最后手段）
void LateJoinManager::RequestLateJoinSpawn(APBPlayerController* PC)
{
    auto it = LateJoinPlayers.find(PC);
    if (!IsCurrentServerConnection(PC) || it == LateJoinPlayers.end())
        return;

    // Mutate the tracked state before crossing into the engine. RestartPlayers
    // and the fallback RPCs may synchronously disconnect and erase this entry.
    const int spawnAttempt = it->second.SpawnAttempts;
    ++it->second.SpawnAttempts;
    it->second.ElapsedSeconds = 0.0f;

    PrepareLateJoinRespawn(PC);
    if (!IsCurrentServerConnection(PC) || !LateJoinPlayers.contains(PC))
        return;
    PlayerRespawnAllowedMap[PC] = true;

    APBGameMode* GameMode = GetPBGameMode();
    if (!IsCurrentServerConnection(PC) || !LateJoinPlayers.contains(PC))
        return;

    if (BeginSpawnDispatch) BeginSpawnDispatch(PC);
    ScopedSpawnDispatchCompletion dispatchCompletion(
        CompleteSpawnDispatch, PC);
    if (!IsCurrentServerConnection(PC) || !LateJoinPlayers.contains(PC))
        return;

    ScopedRestartPermit permit(ManagedRestartPermit, ManagedRestartPermitDepth, PC);
    std::cout << "[LATEJOIN] Pre-dispatch role identity: "
        << DescribePlayerRoleIdentity(PC) << std::endl;
    // RestartPlayers requires the controller to have left Observer state, but
    // ServerSetSpectatorWaiting(false) is not safe here: it synchronously
    // enters the default FIXER respawn path on this build. Run only the native
    // state transition while the matching managed restart permit is active.
    PC->ExitObserverState();
    if (!IsCurrentServerConnection(PC) || !LateJoinPlayers.contains(PC))
        return;
    std::cout << "[LATEJOIN] Post-observer-exit role identity: "
        << DescribePlayerRoleIdentity(PC) << std::endl;
    if (spawnAttempt == 0 && GameMode)
    {
        // 第 1 次：通过 GameMode 的标准 RestartPlayers 生成
        TArray<AController*> Controllers{};
        Controllers.Add((AController*)PC);
        std::cout << "[LATEJOIN] RestartPlayers for late join player." << std::endl;
        GameMode->RestartPlayers(Controllers);
    }
    else if (spawnAttempt == 1)
    {
        // 第 2 次：RestartPlayers 未生效，尝试 ServerQuickRespawn
        std::cout << "[LATEJOIN] RestartPlayers did not produce a pawn; trying ServerQuickRespawn." << std::endl;
        PC->ServerQuickRespawn();
    }
    else if (spawnAttempt == 2)
    {
        // 第 3 次：QuickRespawn 也未生效，用 ServerSuicide 触发重生链
        std::cout << "[LATEJOIN] Quick respawn did not produce a pawn; trying ServerSuicide fallback." << std::endl;
        PC->ServerSuicide(0);
    }

}
