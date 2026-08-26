#pragma once

// ======================================================
//  LateJoinManager — 中途加入（Mid-Game Join）状态机
// ======================================================
//
//  职责：
//    管理比赛进行中新连接玩家从"进入"到"可玩"的完整生命周期，
//    包括角色选择、Pawn 生成、客户端状态同步。
//
//  使用方式：
//    1. 构造实例，注入外部依赖（见构造函数参数）
//    2. 在 PostLogin Hook 中调用 OnPostLogin()
//    3. 在 ProcessEvent Hook 中调用 OnProcessEvent()
//    4. 在 TickFlush Hook 中调用 Tick()
//
//  设计原则：
//    - 所有 LateJoin 内部状态完全私有封装
//    - 外部共享状态（PlayerRespawnAllowedMap、DidProcStartMatch）
//      通过引用注入，不获取所有权
//    - Hook 层仅需一行调用，拦截逻辑由本类内部处理

#include <unordered_map>
#include <functional>
#include <cstdint>
#include <string>

// Forward declarations — 与 SDK 命名空间保持一致
namespace SDK
{
    class APBPlayerController;
    class APawn;
    class APBGameState;
    class APBGameMode;
    class AGameMode;
    class UObject;
}

class LateJoinManager
{
public:
    // ------------------------------------------------------------------
    //  嵌套类型 — 中途加入状态机
    // ------------------------------------------------------------------

    // @brief 中途加入玩家所处的阶段
    enum class ELateJoinState
    {
        AwaitingRespawnInput,   // Dead; preserve role/loadout until native F/ESC input.
        PendingRoleSelection,   // 等待客户端选择角色
        RoleConfirmed,          // 角色已确认，准备生成可玩 Pawn
        Spawned,                // 可玩 Pawn 已生成；保留至断线以统一后续复活
        TimedOut                // 超时放弃（终态，将移除）
    };

    // @brief 单个中途加入玩家的跟踪信息
    struct FLateJoinInfo
    {
        ELateJoinState State = ELateJoinState::PendingRoleSelection;
        float ElapsedSeconds = 0.0f;    // 当前阶段已持续时间（秒）
        int   SpawnAttempts   = 0;      // 已尝试生成的次数（最多 3 次）
        bool  ClientStartSent = false;   // Whether the client received the in-match UI/lifecycle sequence.
        bool  bIsInitialJoin  = false;   // Connected before the match/round entered progress.
        bool  InitialRoleSelectionSent = false; // Native pre-match prompt reached this connection.
        bool  HasCompletedSpawn = false; // Suppress repeated mid-game lifecycle synchronization.
        bool  AwaitingRoleTransitionDeath = false; // A->B accepted while the old Pawn remains alive.
        bool  ExplicitNativeRespawnDispatched = false; // Attempt 0 was the exact F RPC, not RestartPlayers.
        bool  bIsSeamlessRebound = false; // Controller/role survived an owned match transition.
        std::uint64_t RespawnLifecycleId = 0; // Monotonic within the current server world.
        SDK::APawn* LastClientSyncedPawn = nullptr; // Idempotence key for the current Pawn generation.
        std::uint64_t LastClientSyncedRespawnLifecycleId = 0;
        std::string DesiredRoleId; // Last role accepted by the authoritative PlayerState.
    };

    // ------------------------------------------------------------------
    //  回调类型定义
    // ------------------------------------------------------------------

    // @brief 用于通知外部系统房间已启动（如后端心跳上报）
    using FReportRoomStarted = std::function<void()>;
    using FCanReleasePlayerSpawn = std::function<bool(SDK::APBPlayerController*)>;
    using FSpawnDispatchNotification =
        std::function<void(SDK::APBPlayerController*)>;
    using FExplicitRespawnDispatch = std::function<void()>;

    // ------------------------------------------------------------------
    //  构造 / 初始化
    // ------------------------------------------------------------------

    // @param InDidProcStartMatch        引用 — 比赛是否已调用过 StartMatch
    // @param InPlayerRespawnAllowedMap  引用 — 玩家重生许可表（与 Respawn 系统共享）
    // @param InReportRoomStarted        回调 — 通知后端房间已启动
    LateJoinManager(
        const bool& InDidProcStartMatch,
        const bool& InDidBroadcastRoleSelection,
        std::unordered_map<SDK::APBPlayerController*, bool>& InPlayerRespawnAllowedMap,
        FReportRoomStarted InReportRoomStarted = nullptr,
        FCanReleasePlayerSpawn InCanReleasePlayerSpawn = nullptr,
        FSpawnDispatchNotification InBeginSpawnDispatch = nullptr,
        FSpawnDispatchNotification InCompleteSpawnDispatch = nullptr,
        FSpawnDispatchNotification InFinalizeSpawnRequest = nullptr,
        FSpawnDispatchNotification InAbandonSpawnRequest = nullptr
    );

    // ------------------------------------------------------------------
    //  公有接口 — Hook 层调用入口
    // ------------------------------------------------------------------

    // @brief PostLogin Hook 中调用。检测是否为中途加入并注册玩家。
    // @param GameMode  当前 GameMode
    // @param PC        新连接的 PlayerController
    // @return true 表示已作为中途加入处理，调用方应跳过正常首生逻辑
    bool OnPostLogin(SDK::AGameMode* GameMode, SDK::APBPlayerController* PC);

    // @brief PostLogin Hook 中调用。将初始加入玩家注册到与中途加入一致的流程。
    //        初始加入也会经过 ClientStart 序列与延迟 Pawn 生成路径，
    //        以统一客户端状态推进与 UI 生命周期。
    // @param GameMode  当前 GameMode
    // @param PC        新连接的 PlayerController
    void QueueInitialJoinPlayer(
        SDK::AGameMode* GameMode,
        SDK::APBPlayerController* PC,
        bool IsFreshSeamlessDestination = false);

    // Register a controller whose exact connection and playable Pawn were
    // carried through an owned dedicated seamless transition. Returns false
    // when the runtime shape is incomplete so the caller can use the ordinary
    // initial-join/role-selection recovery path.
    bool RegisterSeamlessReboundPlayer(SDK::APBPlayerController* PC);

    // @brief ProcessEvent Hook 中调用。拦截角色选择相关的 RPC。
    //        仅处理 CanPlayerSelectRole / CanSelectRole（强制返回 true）。
    //        ServerConfirmRoleSelection 需要调用方先执行原函数再用 OnRoleConfirmed 推进状态，
    //        因为此处无法访问 SafetyHook 的原始调用。
    // @param Object       ProcessEvent 的目标对象
    // @param functionName 函数全名（用于匹配）
    // @param Parms        参数缓冲区（可能被修改）
    // @return true 表示已拦截并处理，调用方应 return 跳过原始 ProcessEvent
    bool OnProcessEvent(SDK::UObject* Object, const std::string& functionName, void* Parms);

    // @brief ServerConfirmRoleSelection 拦截后的状态推进。
    //        调用方应先检查 IsLateJoinPlayer()，若为 true 则：
    //          1. 执行原始 ProcessEvent.call(Object, Function, Parms)
    //          2. 调用本方法
    //          3. return 跳过正常计数逻辑
    // @param PC 角色确认的 PlayerController
    void OnRoleConfirmed(
        SDK::APBPlayerController* PC,
        const std::string& roleId,
        bool wasAwaitingRespawnInputBeforeConfirmation,
        bool confirmationRestartWasSuppressed);
    void OnPlayerKilled(SDK::APBPlayerController* PC);

    // Converts an engine-initiated restart into the same serialized spawn
    // state machine. Only a current connection that has completed at least one
    // playable spawn is eligible; in particular, PendingRoleSelection and the
    // first RoleConfirmed -> Spawned transition can never be bypassed through
    // this API. The caller must suppress the original restart unless the
    // matching managed permit is active.
    bool CanQueueManagedRespawn(SDK::APBPlayerController* PC) const;
    bool QueueManagedRespawn(SDK::APBPlayerController* PC);
    bool DispatchManagedExplicitRespawn(
        SDK::APBPlayerController* PC,
        const char* requestKind,
        const FExplicitRespawnDispatch& dispatch);
    // A Deploy click from the post-death role screen has already committed the
    // requested role. Try the game's PB-specific respawn RPC exactly once.
    // If native cooldown rejects it, remain in AwaitingRespawnInput without
    // entering the generic RestartPlayers/QuickRespawn/Suicide fallback chain.
    bool DispatchPostDeathRoleDeployRespawn(
        SDK::APBPlayerController* PC,
        const FExplicitRespawnDispatch& dispatch);
    bool IsManagedPlayer(SDK::APBPlayerController* PC) const;
    // A live, already-spawned managed player may use the in-match role screen
    // to change the role/loadout for the next life. The native confirmation
    // must still commit its role and pre-ordering cache, but its synchronous
    // RestartPlayer call must be suppressed by the hook layer.
    bool ShouldStageLiveRoleConfirmation(
        SDK::APBPlayerController* PC,
        const std::string& requestedRoleId) const;
    bool IsRedundantSeamlessRoleConfirmation(
        SDK::APBPlayerController* PC,
        const std::string& requestedRoleId) const;
    bool HasManagedRestartPermit(SDK::APBPlayerController* PC) const;
    bool IsAwaitingRespawnInput(SDK::APBPlayerController* PC) const;

    // Remove all deferred-spawn state for a disconnected controller. This is
    // called from the authoritative K2_OnLogout hook before the UObject can be
    // recycled for a later connection.
    void OnPlayerDisconnected(SDK::APBPlayerController* PC);

    // The native pre-match role prompt is broadcast once. Track recipients so
    // late initial connections (and controllers not ready at broadcast time)
    // can be prompted by Tick without duplicating successful deliveries.
    void OnRoleSelectionPromptSent(SDK::APBPlayerController* PC);

    // Drop pointer-keyed state when the server enters a different UWorld.
    void ResetForWorldChange();

    // @brief TickFlush Hook 中调用。驱动状态机每帧更新。
    // @param DeltaTime 帧间隔时间（秒）
    void Tick(float DeltaTime);

    // @brief 查询指定 PC 是否为中途加入玩家（供其他系统判断）
    bool IsLateJoinPlayer(SDK::APBPlayerController* PC) const;

    // @brief 查询指定 PC 是否为初始加入玩家（注册到延迟生成流程但比赛未开始时连接）
    bool IsInitialJoinPlayer(SDK::APBPlayerController* PC) const;

    // Keep the native role prompt behind the direct-connect match-state sync.
    // This preserves a single visible ClientSelectRole delivery.
    bool ShouldDeferInitialRoleSelectionPrompt(SDK::APBPlayerController* PC) const;

    // @brief 查询中途加入窗口是否开放（比赛已开始或回合进行中）
    bool IsLateJoinWindowOpen() const;

    // Initial players are spawned one at a time before StartMatch so a
    // world+role FieldMod cache can never feed two same-role players.
    bool CanRestartBeforeMatch(SDK::APBPlayerController* PC) const;
    bool AreInitialPlayersReadyForStart() const;
    bool HasFreshSeamlessInitialPlayer() const;

private:
    // ------------------------------------------------------------------
    //  私有类型 — 客户端状态同步配置
    // ------------------------------------------------------------------

    struct FClientSyncOptions
    {
        bool SendStartOnlineGame = false;
        bool SendMatchHasStarted = false;
        bool SendRoundHasStarted = false;
        bool SendNotifyGameStarted = false;
        bool SendClientSelectRole = false;
        bool SendReadyAtStartSpot = false;
        bool SendGotoPlaying = false;
        bool SendRestartAndAcknowledge = false;
    };

    // ------------------------------------------------------------------
    //  外部依赖（引用，不拥有）
    // ------------------------------------------------------------------

    const bool& DidProcStartMatch;                                                      // 比赛是否已启动
    const bool& DidBroadcastRoleSelection;                                              // native prompt was broadcast
    std::unordered_map<SDK::APBPlayerController*, bool>& PlayerRespawnAllowedMap;       // 重生许可表
    FReportRoomStarted        ReportRoomStarted;                                        // 后端上报回调
    FCanReleasePlayerSpawn    CanReleasePlayerSpawn;                                    // authoritative loadout gate
    FSpawnDispatchNotification BeginSpawnDispatch;
    FSpawnDispatchNotification CompleteSpawnDispatch;
    FSpawnDispatchNotification FinalizeSpawnRequest;
    FSpawnDispatchNotification AbandonSpawnRequest;

    // ------------------------------------------------------------------
    //  内部状态
    // ------------------------------------------------------------------

    std::unordered_map<SDK::APBPlayerController*, FLateJoinInfo> LateJoinPlayers;
    SDK::APBPlayerController* ManagedRestartPermit = nullptr;
    int ManagedRestartPermitDepth = 0;
    std::uint64_t NextRespawnLifecycleId = 1;

    // ------------------------------------------------------------------
    //  可配置常量 — 未来可提取为配置项
    // ------------------------------------------------------------------

    static constexpr float CLIENT_START_DELAY_SEC   = 1.0f;   // 连接后延迟多久发送 ClientStart
    static constexpr float ROLE_SELECTION_TIMEOUT   = 30.0f;  // 等待角色选择超时
    static constexpr float SPAWN_RETRY_INTERVAL     = 2.0f;   // 生成重试间隔
    static constexpr int   MAX_SPAWN_ATTEMPTS       = 3;       // 最大生成尝试次数

    // ------------------------------------------------------------------
    //  私有方法 — 状态查询
    // ------------------------------------------------------------------

    SDK::APBGameState* GetPBGameState() const;
    SDK::APBGameMode*  GetPBGameMode() const;
    bool          IsRoundCurrentlyInProgress() const;
    static bool   IsSpectatorPawn(SDK::APawn* Pawn);
    static bool   HasPlayableLateJoinPawn(
        SDK::APBPlayerController* PC,
        const std::string& desiredRoleId = {});

    // ------------------------------------------------------------------
    //  私有方法 — 状态机动作
    // ------------------------------------------------------------------

    void QueueLateJoinPlayer(SDK::APBPlayerController* PC);
    void SyncClientJoinState(SDK::APBPlayerController* PC, const FClientSyncOptions& Options);
    void SendInitialJoinClientStart(SDK::APBPlayerController* PC);
    void SendLateJoinClientStart(SDK::APBPlayerController* PC);
    void PrepareLateJoinRespawn(SDK::APBPlayerController* PC);
    void FinalizeLateJoinSpawn(SDK::APBPlayerController* PC, FLateJoinInfo Info);
    void RequestLateJoinSpawn(SDK::APBPlayerController* PC);
};
