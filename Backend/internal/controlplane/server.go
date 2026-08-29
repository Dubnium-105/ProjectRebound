package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/cache"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/connection"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/diagnostic"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/download"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/entitlement"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/health"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/integrity"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/invite"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/matchlobby"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/observability"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/relayregistry"
	updateservice "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg                *config.Config
	logger             *slog.Logger
	database           *database.Pool
	cache              *cache.Client
	httpServer         *http.Server
	backgroundServices []backgroundService
	background         sync.WaitGroup
}

type backgroundService interface {
	Run(context.Context)
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if err := cfg.ValidateControlPlane(); err != nil {
		return nil, err
	}

	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	dbPool, err := database.Open(startupCtx, cfg.Database)
	if err != nil {
		return nil, err
	}
	if err := database.NewMigrator(dbPool.Pool).Up(startupCtx); err != nil {
		dbPool.Close()
		return nil, fmt.Errorf("apply database migrations: %w", err)
	}

	redisClient, err := cache.Open(startupCtx, cfg.Redis)
	if err != nil {
		dbPool.Close()
		return nil, err
	}

	limiter := appmiddleware.NewIPRateLimiter(
		cfg.RateLimit.RequestsPerSecond,
		cfg.RateLimit.Burst,
		cfg.HTTP.TrustProxyHeaders,
	)
	handler, backgroundServices, err := buildHandler(startupCtx, cfg, logger, dbPool, redisClient, limiter)
	if err != nil {
		_ = redisClient.Close()
		dbPool.Close()
		return nil, err
	}

	return &Server{
		cfg:                cfg,
		logger:             logger,
		database:           dbPool,
		cache:              redisClient,
		backgroundServices: backgroundServices,
		httpServer: &http.Server{
			Addr:              cfg.HTTP.Addr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout(),
			ReadTimeout:       cfg.HTTP.ReadTimeout(),
			WriteTimeout:      cfg.HTTP.WriteTimeout(),
			IdleTimeout:       cfg.HTTP.IdleTimeout(),
		},
	}, nil
}

func buildHandler(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	dbPool *database.Pool,
	redisClient *cache.Client,
	limiter *appmiddleware.IPRateLimiter,
) (http.Handler, []backgroundService, error) {
	router := chi.NewRouter()
	metrics := observability.NewMetrics(dbPool.Pool)
	metrics.SetRedisProbe(redisClient.Check)
	healthHandler := health.NewHandler([]health.Dependency{
		{Name: "postgres", Checker: dbPool},
		{Name: "redis", Checker: redisClient},
	}, cfg.Database.HealthTimeout(), logger)
	router.Get("/health/live", healthHandler.Live)
	router.Get("/health/ready", healthHandler.Ready)

	tokenManager, ephemeralKey, err := auth.NewTokenManager(cfg.Auth, cfg.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize access token signer: %w", err)
	}
	if ephemeralKey {
		logger.Warn("using ephemeral development access-token key; tokens will not survive a restart")
	}
	deviceFingerprinter, ephemeralDeviceFingerprintKey, err := auth.NewDeviceFingerprinter(cfg.Auth, cfg.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize device fingerprint protection: %w", err)
	}
	if ephemeralDeviceFingerprintKey {
		logger.Warn("using ephemeral development device-fingerprint key; device correlation will not survive a restart")
	}
	authRepository := auth.NewRepository()
	playerRepository := player.NewRepository()
	adminRepository := admin.NewRepository()
	entitlementRepository := entitlement.NewRepository(dbPool.Pool)
	inviteService := invite.NewService(dbPool.Pool, invite.NewRepository(dbPool.Pool), adminRepository)
	authService := auth.NewService(
		dbPool.Pool,
		authRepository,
		playerRepository,
		tokenManager,
		deviceFingerprinter,
		cfg.Auth,
		logger,
	)
	authService.SetTicketVerifier(auth.NewExecTicketVerifier(cfg.Auth, logger))
	authService.SetInviteConsumer(inviteService)
	authService.SetEntitlements(entitlementRepository)
	authService.SetBindLimiter(auth.NewBindLimiter(redisClient, cfg.Auth, logger))
	authService.SetMetrics(metrics)
	if len(cfg.Auth.DevelopmentTrustedSteamIDs) > 0 {
		logger.Warn(
			"development Steam ticket bypass is enabled for an explicit lab allowlist",
			"allowlisted_players", len(cfg.Auth.DevelopmentTrustedSteamIDs),
		)
	}
	integrityService, err := integrity.NewService(cfg.Auth, authService, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize integrity challenge service: %w", err)
	}
	authService.SetIntegritySessionManager(integrityService)
	authHandler := auth.NewHTTPHandler(authService, logger, cfg.HTTP.TrustProxyHeaders)
	vntRepository := vnt.NewRepository(dbPool.Pool)
	vntVersionPolicy := vnt.NewVersionPolicy(nil, nil)
	metrics.SetVNTPolicy(cfg.Update.VNTRoomsEnabled, vntVersionPolicy)
	vntService := vnt.NewService(vntRepository, entitlementRepository)
	vntService.SetVersionPolicy(vntVersionPolicy)
	vntService.SetCredentialRotationGrace(cfg.VNT.CredentialRotationGrace())
	vntService.SetMaxNodesPerPlayer(cfg.VNT.MaxNodesPerPlayer)
	vntLimiter := vnt.NewLimiter(redisClient, cfg.VNT, logger)
	vntLimiter.SetMetrics(metrics)
	vntService.SetLimiter(vntLimiter)
	vntHandler := vnt.NewHTTPHandler(vntService, logger)
	vntHandler.SetAccessAuthenticator(authService)
	vntHandler.SetTrustProxyHeaders(cfg.HTTP.TrustProxyHeaders)
	integrityHandler := integrity.NewHTTPHandler(
		integrityService,
		logger,
		cfg.HTTP.TrustProxyHeaders,
	)
	diagnosticHandler := diagnostic.NewHTTPHandler(diagnostic.NewRepository(dbPool.Pool), logger)
	router.Route("/v1", func(router chi.Router) {
		router.Post("/auth/bind", authHandler.Bind)
		router.Post("/auth/refresh", authHandler.Refresh)
		router.With(auth.RequireAccess(authService, logger)).Post("/auth/logout", authHandler.Logout)
		router.With(auth.RequireAccess(authService, logger)).Get("/users/me", authHandler.Me)
		router.With(auth.RequireAccess(authService, logger)).Get("/users/me/sessions", authHandler.ListSessions)
		router.With(auth.RequireAccess(authService, logger), auth.RequireActive, auth.RequireVerified).Get("/users/me/vnt-nodes", vntHandler.ListOwned)
		router.With(auth.RequireAccess(authService, logger)).Delete("/users/me/sessions/{session_id}", authHandler.RevokeSession)
		router.With(auth.RequireAccess(authService, logger)).Post("/users/me/sessions/revoke-others", authHandler.RevokeOtherSessions)
		router.With(auth.RequireAccess(authService, logger)).Post("/integrity/challenge", integrityHandler.Challenge)
		router.With(auth.RequireAccess(authService, logger)).Post("/integrity/proof", integrityHandler.Proof)
		router.With(auth.RequireAccess(authService, logger)).Post("/integrity/verify", integrityHandler.Verify)
		router.With(auth.RequireAccess(authService, logger)).Post("/diagnostic/report", diagnosticHandler.Submit)
		router.With(auth.RequireAccess(authService, logger)).Post("/vnt/node-enrollments", vntHandler.CreateEnrollment)
		router.Post("/vnt/nodes", vntHandler.Register)
		router.Post("/vnt/nodes/{node_id}/recover", vntHandler.Recover)
		router.Get("/vnt/nodes", vntHandler.List)
		router.Post("/vnt/nodes/{node_id}/heartbeat", vntHandler.Heartbeat)
		router.Post("/vnt/nodes/{node_id}/credential/rotate", vntHandler.RotateCredential)
		router.Delete("/vnt/nodes/{node_id}", vntHandler.Retire)
	})

	adminAuthenticator, err := admin.NewAuthenticator(cfg.Admin)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize administrator authentication: %w", err)
	}
	if !adminAuthenticator.Configured() {
		logger.Warn("no machine administrator tokens configured; internal operational routes will reject requests")
	}
	adminNetworkGuard, err := admin.NewNetworkGuard(cfg.Admin.TrustedCIDRs, cfg.HTTP.TrustProxyHeaders)
	if err != nil {
		return nil, nil, err
	}
	adminTokenManager, ephemeralAdminTokenKey, err := auth.NewTokenManager(
		cfg.Admin.AccessTokenConfig(),
		cfg.Environment,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize administrator access token signer: %w", err)
	}
	if ephemeralAdminTokenKey {
		logger.Warn("using ephemeral development administrator access-token key; sessions will not survive a restart")
	}
	adminSecretBox, ephemeralMFAKey, err := admin.NewSecretBox(
		cfg.Admin.MFAEncryptionKeyBase64,
		cfg.Environment,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize administrator MFA encryption: %w", err)
	}
	if ephemeralMFAKey {
		logger.Warn("using ephemeral development administrator MFA key; configure ADMIN_MFA_ENCRYPTION_KEY_BASE64 before creating accounts")
	}
	turnstileVerifier := admin.NewCloudflareTurnstileVerifier(cfg.Admin)
	if !turnstileVerifier.Configured() {
		logger.Warn("Cloudflare Turnstile is not configured; human administrator login will remain unavailable")
	}
	adminAuthRepository := admin.NewAuthRepository(dbPool.Pool)
	adminAuthService, err := admin.NewAdminAuthService(
		dbPool.Pool,
		adminAuthRepository,
		turnstileVerifier,
		admin.NewLoginLimiter(redisClient, cfg.Admin, logger),
		adminTokenManager,
		adminSecretBox,
		cfg.Admin,
		logger,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize administrator authentication service: %w", err)
	}
	adminSessionAuthenticator := admin.NewSessionAuthenticator(adminAuthService)
	adminAuthHandler := admin.NewAdminAuthHTTPHandler(
		adminAuthService,
		logger,
		cfg.Admin,
		cfg.Environment,
		cfg.HTTP.TrustProxyHeaders,
	)
	adminService := admin.NewService(dbPool.Pool, playerRepository, authRepository, adminRepository)
	adminHandler := admin.NewHTTPHandler(adminService, logger, cfg.HTTP.TrustProxyHeaders)
	adminSecurityService := admin.NewSecurityService(
		dbPool.Pool,
		admin.NewSecurityRepository(dbPool.Pool),
		adminRepository,
	)
	adminSecurityHandler := admin.NewSecurityHTTPHandler(
		adminSecurityService,
		logger,
		cfg.HTTP.TrustProxyHeaders,
	)
	adminGovernanceService := admin.NewGovernanceService(
		dbPool.Pool,
		adminRepository,
		adminSecretBox,
	)
	adminGovernanceHandler := admin.NewGovernanceHTTPHandler(
		adminGovernanceService,
		logger,
		cfg.HTTP.TrustProxyHeaders,
	)
	adminSettingsService := admin.NewSettingsService(dbPool.Pool, adminRepository)
	adminSettingsHandler := admin.NewSettingsHTTPHandler(
		adminSettingsService,
		logger,
		cfg.HTTP.TrustProxyHeaders,
	)
	inviteHandler := invite.NewHTTPHandler(inviteService, logger, cfg.HTTP.TrustProxyHeaders)
	adminRouter := chi.NewRouter()
	adminRouter.Use(adminNetworkGuard.Middleware)
	adminRouter.Get("/auth/config", adminAuthHandler.Config)
	adminRouter.Post("/auth/login", adminAuthHandler.Login)
	adminRouter.Post("/auth/mfa/verify", adminAuthHandler.VerifyMFA)
	adminRouter.Post("/auth/refresh", adminAuthHandler.Refresh)
	adminRouter.Group(func(router chi.Router) {
		router.Use(adminSessionAuthenticator.Middleware)
		router.Post("/auth/logout", adminAuthHandler.Logout)
		router.Post("/auth/step-up", adminAuthHandler.StepUp)
		router.Get("/auth/me", adminAuthHandler.Me)
		router.Get("/auth/sessions", adminAuthHandler.Sessions)
		router.Delete("/auth/sessions/{session_id}", adminAuthHandler.RevokeSession)
		router.With(admin.RequirePermission("dashboard.read")).Get("/dashboard/summary", adminSecurityHandler.Summary)
		router.With(admin.RequirePermission("dashboard.read")).Get("/dashboard/timeseries", adminSecurityHandler.Timeseries)
		router.With(admin.RequirePermission("dashboard.read")).Get("/dashboard/alerts", adminSecurityHandler.Alerts)
		router.With(admin.RequirePermission("players.read")).Get("/players", adminHandler.ListPlayers)
		router.With(admin.RequirePermission("players.read")).Get("/players/{player_id}", adminHandler.GetPlayer)
		router.With(admin.RequirePermission("players.read")).Get("/players/{player_id}/sessions", adminSecurityHandler.PlayerSessions)
		router.With(admin.RequirePermission("players.read")).Get("/players/{player_id}/risk-events", adminSecurityHandler.PlayerRiskEvents)
		router.With(admin.RequirePermission("players.read")).Get("/players/{player_id}/login-events", adminSecurityHandler.PlayerLoginEvents)
		router.Patch("/players/{player_id}", adminHandler.PatchPlayer)
		router.Post("/players/{player_id}/revoke-sessions", adminHandler.RevokeSessions)
		router.With(admin.RequirePermission("invite_codes.create")).Post("/invite-codes", inviteHandler.Create)
		router.With(admin.RequirePermission("invite_codes.read")).Get("/invite-codes", inviteHandler.List)
		router.With(admin.RequirePermission("invite_codes.read")).Get("/invite-codes/{id}", inviteHandler.Get)
		router.With(admin.RequirePermission("invite_codes.read")).Get("/invite-codes/{id}/uses", inviteHandler.ListUses)
		router.With(admin.RequirePermission("invite_codes.update")).Patch("/invite-codes/{id}", inviteHandler.Patch)
		router.With(admin.RequirePermission("invite_codes.revoke")).Post("/invite-codes/{id}/revoke", inviteHandler.Revoke)
		router.With(admin.RequirePermission("risk_events.read")).Get("/risk-events", adminSecurityHandler.ListRiskEvents)
		router.With(admin.RequirePermission("risk_events.read")).Get("/risk-events/{event_id}", adminSecurityHandler.GetRiskEvent)
		router.With(admin.RequirePermission("risk_events.resolve")).Post("/risk-events/{event_id}/resolve", adminSecurityHandler.ResolveRiskEvent)
		router.With(admin.RequirePermission("audit_logs.read")).Get("/audit-logs", adminSecurityHandler.ListAudit)
		router.With(admin.RequirePermission("vnt_nodes.read")).Get("/vnt-security-events", adminSecurityHandler.ListVNTSecurityAudit)
		router.With(admin.RequirePermission("audit_logs.read")).Get("/audit-logs/{audit_id}", adminSecurityHandler.GetAudit)
		router.With(admin.RequirePermission("audit_logs.read")).Get("/login-audit", adminSecurityHandler.ListLoginAudit)
		router.With(admin.RequirePermission("admins.read")).Get("/admins", adminGovernanceHandler.ListAdmins)
		router.With(
			admin.RequirePermission("admins.create"),
			admin.RequireStepUp(adminAuthService),
		).Post("/admins", adminGovernanceHandler.CreateAdmin)
		router.With(
			admin.RequirePermission("admins.update"),
			admin.RequireStepUp(adminAuthService),
		).Patch("/admins/{admin_id}", adminGovernanceHandler.UpdateAdmin)
		router.With(
			admin.RequirePermission("admins.update"),
			admin.RequireStepUp(adminAuthService),
		).Post("/admins/{admin_id}/reset-mfa", adminGovernanceHandler.ResetMFA)
		router.With(admin.RequirePermission("admins.read")).Get("/roles", adminGovernanceHandler.ListRoles)
		router.With(
			admin.RequirePermission("roles.manage"),
			admin.RequireStepUp(adminAuthService),
		).Patch("/roles/{role_id}", adminGovernanceHandler.UpdateRole)
		router.Get("/features", adminSettingsHandler.Features)
		router.Get("/capabilities", adminSettingsHandler.Capabilities)
		router.With(admin.RequirePermission("settings.read")).Get("/settings", adminSettingsHandler.List)
		router.With(
			admin.RequirePermission("settings.update"),
			admin.RequireStepUp(adminAuthService),
		).Patch("/settings", adminSettingsHandler.Update)
	})
	router.With(adminNetworkGuard.Middleware).Get("/internal/metrics", metrics.Handler().ServeHTTP)

	gameServerRepository := gameserver.NewRepository(dbPool.Pool)
	gameServerRegistrationRepository := gameserverregistration.NewRepository()
	gameServerAuthority, err := gameserver.NewAuthority(cfg.GameServer, cfg.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize game server certificate authority: %w", err)
	}
	if gameServerAuthority.Ephemeral() {
		logger.Warn("using ephemeral development game server CA; node certificates will not survive a restart")
	}
	gameServerService := gameserver.NewService(
		gameServerRepository, gameServerRegistrationRepository, cfg.GameServer, gameServerAuthority,
	)
	gameServerHandler := gameserver.NewHTTPHandler(gameServerService, logger)
	router.Group(func(router chi.Router) {
		router.Use(auth.RequireAccess(authService, logger))
		router.Use(auth.RequireActive)
		router.Use(auth.RequireVerified)
		router.Post("/v1/game-server-registration-tokens", gameServerHandler.IssueRegistrationCredential)
	})
	router.Post("/v1/game-servers", gameServerHandler.Register)
	router.Get("/v1/game-servers", gameServerHandler.List)
	router.Get("/v1/game-servers/{server_id}", gameServerHandler.Get)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/heartbeat", gameServerHandler.Heartbeat)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/credential/rotate", gameServerHandler.RotateCredential)
	router.With(gameServerHandler.RequireCredentialProof).
		Delete("/v1/game-servers/{server_id}", gameServerHandler.Deregister)

	p2pRoomService := p2proom.NewService(p2proom.NewRepository(dbPool.Pool), cfg.P2PRoom)
	p2pRoomService.SetEntitlementChecker(entitlementRepository)
	vntSecretBox, ephemeralVNTSecretKey, err := p2proom.NewSecretBoxKeyring(
		os.Getenv("VNT_SECRET_ENCRYPTION_KEY_BASE64"),
		os.Getenv("VNT_SECRET_ENCRYPTION_KEY_ID"),
		os.Getenv("VNT_SECRET_DECRYPTION_KEYS"),
		cfg.Environment,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize VNT room secret encryption: %w", err)
	}
	if ephemeralVNTSecretKey {
		logger.Warn("using ephemeral development VNT room key; rooms will not survive a restart")
	}
	p2pRoomService.SetVNT(vntRepository, vntSecretBox)
	p2pRoomService.SetVNTEnabled(cfg.Update.VNTRoomsEnabled)
	p2pRoomService.SetVNTVersionPolicy(vntVersionPolicy)
	p2pRoomService.SetVNTLimiter(vntLimiter)
	p2pBattleLogRepository := p2pbattlelog.NewRepository(dbPool.Pool)
	p2pBattleLogService := p2pbattlelog.NewService(
		p2pBattleLogRepository, cfg.P2PBattleLog,
	)
	p2pRoomService.SetMatchLifecycle(p2pBattleLogService)
	p2pRoomHandler := p2proom.NewHTTPHandler(p2pRoomService, logger)
	p2pRoomHandler.SetTrustProxyHeaders(cfg.HTTP.TrustProxyHeaders)
	p2pBattleLogHandler := p2pbattlelog.NewHTTPHandler(
		p2pBattleLogService, logger, cfg.P2PBattleLog.MaxReportBytes,
	)
	p2pBattleLogAdminHandler := p2pbattlelog.NewAdminHTTPHandler(
		p2pbattlelog.NewAdminService(p2pBattleLogRepository, cfg.P2PBattleLog.ShadowMode), logger,
	)
	matchSignerEnvironment := cfg.Environment
	if !cfg.MatchLobby.StrictRosterV1Enabled {
		matchSignerEnvironment = "strict-roster-disabled"
	}
	matchAdmissionSigner, err := matchlobby.NewAdmissionSigner(
		cfg.MatchLobby.AdmissionSigningKeyID,
		cfg.MatchLobby.AdmissionPrivateKeyBase64,
		matchSignerEnvironment,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize match admission signer: %w", err)
	}
	if matchAdmissionSigner.Ephemeral() && cfg.MatchLobby.StrictRosterV1Enabled {
		logger.Warn("using ephemeral development match admission key; active attempts will not survive a restart")
	}
	matchLobbyService := matchlobby.NewService(
		matchlobby.NewRepository(dbPool.Pool), cfg.MatchLobby, matchAdmissionSigner,
		time.Duration(cfg.GameServer.UnhealthyAfterSeconds)*time.Second,
	)
	matchLobbyService.SetP2PTransport(p2pRoomService)
	matchLobbyService.SetP2PMatchProjector(p2pBattleLogService)
	if err := matchLobbyService.FailClosedDisabledAttempts(ctx); err != nil {
		return nil, nil, fmt.Errorf("fail closed strict-roster attempts: %w", err)
	}
	matchLobbyHandler := matchlobby.NewHTTPHandler(matchLobbyService, logger)
	router.Get("/v1/p2p-rooms", p2pRoomHandler.List)
	router.Get("/v1/p2p-rooms/{room_id}", p2pRoomHandler.Get)
	router.Group(func(router chi.Router) {
		router.Use(auth.RequireAccess(authService, logger))
		router.Use(auth.RequireActive)
		router.Use(auth.RequireVerified)
		router.Post("/v1/p2p-rooms", p2pRoomHandler.Create)
		router.Post("/v1/p2p-rooms/{room_id}/join", p2pRoomHandler.Join)
		router.Post("/v1/p2p-rooms/{room_id}/leave", p2pRoomHandler.Leave)
		router.Post("/v1/p2p-rooms/{room_id}/heartbeat", p2pRoomHandler.Heartbeat)
		router.Post("/v1/p2p-rooms/{room_id}/start", p2pRoomHandler.Start)
		router.Delete("/v1/p2p-rooms/{room_id}", p2pRoomHandler.Delete)
		router.Post("/v1/p2p-rooms/{room_id}/vnt/bootstrap", p2pRoomHandler.VNTBootstrap)
		router.Put("/v1/p2p-rooms/{room_id}/vnt/presence/me", p2pRoomHandler.UpdateVNTPresence)
		router.Put("/v1/p2p-rooms/{room_id}/vnt/host-ready", p2pRoomHandler.VNTHostReady)
		router.Post("/v1/p2p-rooms/{room_id}/vnt/rebind", p2pRoomHandler.VNTRebind)
		router.Get("/v1/p2p-rooms/{room_id}/matches/active", p2pBattleLogHandler.ActiveMatch)
		router.Post("/v1/p2p-matches/{match_id}/report-capability", p2pBattleLogHandler.IssueCapability)
		router.Put("/v1/p2p-matches/{match_id}/presence/me", p2pBattleLogHandler.Presence)
		router.Put("/v1/p2p-matches/{match_id}/reports/{report_id}", p2pBattleLogHandler.SubmitReport)
		router.Get("/v1/p2p-matches/{match_id}/result", p2pBattleLogHandler.Result)

		router.Get("/v1/match-lobbies", matchLobbyHandler.List)
		router.Get("/v1/match-lobbies/active", matchLobbyHandler.Active)
		router.Get("/v1/match-lobbies/{lobby_id}", matchLobbyHandler.Get)
		router.Post("/v1/match-lobbies", matchLobbyHandler.Create)
		router.Post("/v1/match-lobbies/{lobby_id}/join", matchLobbyHandler.Join)
		router.Put("/v1/match-lobbies/{lobby_id}/members/me/team", matchLobbyHandler.SelectTeam)
		router.Put("/v1/match-lobbies/{lobby_id}/members/me/ready", matchLobbyHandler.SetReady)
		router.Post("/v1/match-lobbies/{lobby_id}/presence", matchLobbyHandler.Presence)
		router.Post("/v1/match-lobbies/{lobby_id}/leave", matchLobbyHandler.Leave)
		router.Post("/v1/match-lobbies/{lobby_id}/start", matchLobbyHandler.Start)
		router.Post("/v1/match-attempts/{attempt_id}/join-grant", matchLobbyHandler.JoinGrant)
		router.Get("/v1/match-attempts/{attempt_id}/join-grants/{grant_jti}/delivery", matchLobbyHandler.GrantDelivery)
		router.Get("/v1/match-attempts/{attempt_id}/host/allocation", matchLobbyHandler.P2PHostAllocation)
		router.Get("/v1/match-attempts/{attempt_id}/host/admissions", matchLobbyHandler.P2PAuthorityAdmissions)
		router.Post("/v1/match-attempts/{attempt_id}/host/admissions/{grant_jti}/delivered", matchLobbyHandler.P2PAdmissionDelivered)
		router.Post("/v1/match-attempts/{attempt_id}/host/payload-installed", matchLobbyHandler.P2PPayloadInstalled)
		router.Post("/v1/match-attempts/{attempt_id}/host/ready", matchLobbyHandler.P2PAuthorityReady)
		router.Post("/v1/match-attempts/{attempt_id}/host/connected", matchLobbyHandler.P2PConnected)
		router.Post("/v1/match-attempts/{attempt_id}/host/disconnected", matchLobbyHandler.P2PDisconnected)
		router.Post("/v1/match-attempts/{attempt_id}/host/heartbeat", matchLobbyHandler.P2PAuthorityHeartbeat)
		router.Post("/v1/match-attempts/{attempt_id}/host/complete", matchLobbyHandler.P2PComplete)
	})
	router.With(gameServerHandler.RequireCredentialProof).
		Get("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/allocation", matchLobbyHandler.DedicatedAllocation)
	router.With(gameServerHandler.RequireCredentialProof).
		Get("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/admissions", matchLobbyHandler.DedicatedAuthorityAdmissions)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/admissions/{grant_jti}/delivered", matchLobbyHandler.DedicatedAdmissionDelivered)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/payload-installed", matchLobbyHandler.DedicatedPayloadInstalled)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/ready", matchLobbyHandler.DedicatedAuthorityReady)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/connected", matchLobbyHandler.Connected)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/disconnected", matchLobbyHandler.Disconnected)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/heartbeat", matchLobbyHandler.AuthorityHeartbeat)
	router.With(gameServerHandler.RequireCredentialProof).
		Post("/v1/game-servers/{server_id}/match-attempts/{attempt_id}/complete", matchLobbyHandler.Complete)

	realtimeHub := connection.NewHub(cfg.Connection.WebSocketQueueSize)
	connectionService := connection.NewService(
		connection.NewRepository(dbPool.Pool), p2pRoomService, realtimeHub, cfg.Connection,
	)
	p2pRoomService.SetConnectionCreator(connectionService)
	connectionHandler := connection.NewHTTPHandler(connectionService, logger)
	realtimeHandler := connection.NewRealtimeHandler(
		connectionService, realtimeHub, cfg.CORS, cfg.Connection.WebSocketMaxBytes, logger,
	)
	realtimeHandler.SetMetrics(metrics)
	router.With(auth.RequireAccess(authService, logger)).Get("/v1/connections/{connection_id}", connectionHandler.Get)
	router.Group(func(router chi.Router) {
		router.Use(auth.RequireAccess(authService, logger))
		router.Use(auth.RequireActive)
		router.Use(auth.RequireVerified)
		router.Post("/v1/connections", connectionHandler.Create)
		router.Delete("/v1/connections/{connection_id}", connectionHandler.Delete)
		router.Get("/v1/realtime/connect", realtimeHandler.Connect)
	})

	adminOnlineService := admin.NewOnlineService(
		dbPool.Pool,
		adminRepository,
		connectionService,
		gameServerRegistrationRepository,
		logger,
	)
	adminOnlineService.SetVNT(vntRepository, vntVersionPolicy)
	adminOnlineHandler := admin.NewOnlineHTTPHandler(
		adminOnlineService,
		logger,
		cfg.HTTP.TrustProxyHeaders,
	)
	adminRouter.Group(func(router chi.Router) {
		router.Use(adminSessionAuthenticator.Middleware)
		router.With(admin.RequirePermission("rooms.read")).Get("/p2p-rooms", p2pRoomHandler.List)
		router.With(admin.RequirePermission("rooms.read")).Get("/p2p-rooms/{room_id}", p2pRoomHandler.Get)
		router.With(admin.RequirePermission("rooms.read")).Get("/p2p-rooms/{room_id}/members", adminOnlineHandler.ListRoomMembers)
		router.With(admin.RequirePermission("rooms.close")).Post("/p2p-rooms/{room_id}/close", adminOnlineHandler.CloseRoom)
		router.With(admin.RequirePermission("rooms.delete")).Delete("/p2p-rooms/{room_id}", adminOnlineHandler.DeleteRoom)
		router.With(admin.RequirePermission("rooms.remove_member")).Post("/p2p-rooms/{room_id}/members/{player_id}/remove", adminOnlineHandler.RemoveRoomMember)
		router.With(admin.RequirePermission("game_servers.read")).Get("/game-servers", gameServerHandler.AdminList)
		router.With(admin.RequirePermission("game_servers.read")).Get("/game-servers/{server_id}", gameServerHandler.AdminGet)
		router.With(
			admin.RequirePermission("game_servers.register"),
			admin.RequireStepUp(adminAuthService),
		).Post("/game-servers/registration-tokens", adminOnlineHandler.CreateGameServerRegistration)
		router.With(admin.RequirePermission("game_servers.drain")).Post("/game-servers/{server_id}/drain", adminOnlineHandler.DrainGameServer)
		router.With(admin.RequirePermission("game_servers.drain")).Post("/game-servers/{server_id}/resume", adminOnlineHandler.ResumeGameServer)
		router.With(admin.RequirePermission("game_servers.disable")).Post("/game-servers/{server_id}/disable", adminOnlineHandler.DisableGameServer)
		router.With(admin.RequirePermission("game_servers.ban")).Post("/game-servers/{server_id}/ban", adminOnlineHandler.BanGameServer)
		router.With(admin.RequirePermission("game_servers.delete")).Delete("/game-servers/{server_id}", adminOnlineHandler.DeleteGameServer)
		router.With(admin.RequirePermission("vnt_nodes.read")).Get("/vnt-nodes", adminOnlineHandler.ListVNTNodes)
		router.With(admin.RequirePermission("vnt_nodes.read")).Get("/vnt-nodes/{node_id}", adminOnlineHandler.GetVNTNode)
		router.With(
			admin.RequirePermission("vnt_nodes.drain"),
			admin.RequireStepUp(adminAuthService),
		).Post("/vnt-nodes/{node_id}/drain", adminOnlineHandler.DrainVNTNode)
		router.With(
			admin.RequirePermission("vnt_nodes.revoke"),
			admin.RequireStepUp(adminAuthService),
		).Post("/vnt-nodes/{node_id}/revoke", adminOnlineHandler.RevokeVNTNode)
		router.With(admin.RequirePermission("connections.read")).Get("/connections", adminOnlineHandler.ListConnections)
		router.With(admin.RequirePermission("connections.read")).Get("/connections/{connection_id}", adminOnlineHandler.GetConnection)
		router.With(admin.RequirePermission("connections.close")).Post("/connections/{connection_id}/close", adminOnlineHandler.CloseConnection)
		router.With(admin.RequirePermission("connections.migrate")).Post("/connections/{connection_id}/migrate-relay", adminOnlineHandler.MigrateConnectionRelay)
		router.With(admin.RequirePermission("p2p.battlelog.read")).Get("/p2p-battlelog/matches/{match_id}", p2pBattleLogAdminHandler.MatchEvidence)
		router.With(admin.RequirePermission("p2p.battlelog.raw.read")).Get("/p2p-battlelog/reports/{evidence_id}/raw", p2pBattleLogAdminHandler.RawEvidence)
	})

	bootstrapCredentials, err := relayregistry.ParseBootstrapCredentials(cfg.RelayRegistry.BootstrapTokenSet)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize relay bootstrap credentials: %w", err)
	}
	if len(bootstrapCredentials) == 0 {
		logger.Warn("no relay bootstrap tokens configured; relay enrollment will be rejected")
	}
	relayAuthority, err := relayregistry.NewAuthority(cfg.RelayRegistry, cfg.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize relay certificate authority: %w", err)
	}
	if relayAuthority.Ephemeral() {
		logger.Warn("using ephemeral development relay CA; node certificates will not survive a restart")
	}
	relayTokenManager, err := relayregistry.NewRelayTokenManager(cfg.RelayRegistry, cfg.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize relay token signer: %w", err)
	}
	if relayTokenManager.Ephemeral() {
		logger.Warn("using ephemeral development relay-token key; allocations will not survive a restart")
	}
	relayRepository := relayregistry.NewRepository(dbPool.Pool)
	relayService := relayregistry.NewService(
		relayRepository, relayAuthority, relayTokenManager, p2pRoomService, cfg.RelayRegistry,
	)
	if err := relayService.Initialize(ctx, bootstrapCredentials); err != nil {
		return nil, nil, fmt.Errorf("synchronize relay bootstrap credentials: %w", err)
	}
	relayControlHub := relayregistry.NewControlHub()
	relayTelemetry := relayregistry.NewTelemetryStore()
	metrics.SetRelayMetricsWriter(relayregistry.NewRelayMetricsWriter(relayRepository, relayTelemetry))
	relayService.SetControlPublisher(relayControlHub)
	relayService.SetConnectionCoordinator(connectionService)
	relayService.SetMetrics(metrics)
	connectionService.SetRelayAllocator(relayService)
	adminOnlineService.SetRelayConnectionOperator(relayService)
	relayControlServer, err := relayregistry.NewControlServer(
		relayService, relayControlHub, relayTelemetry, relayAuthority, cfg.RelayRegistry, logger,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize relay control server: %w", err)
	}
	relayHandler := relayregistry.NewHTTPHandler(relayService, logger, cfg.HTTP.TrustProxyHeaders)
	adminRouter.Group(func(router chi.Router) {
		router.Use(adminSessionAuthenticator.Middleware)
		router.With(admin.RequirePermission("relay_nodes.read")).Get("/relay-nodes", relayHandler.List)
		router.With(admin.RequirePermission("relay_nodes.read")).Get("/relay-nodes/{node_id}", relayHandler.Get)
		router.With(admin.RequirePermission("relay_nodes.drain")).Post("/relay-nodes/{node_id}/drain", relayHandler.Drain)
		router.With(admin.RequirePermission("relay_nodes.resume")).Post("/relay-nodes/{node_id}/resume", relayHandler.Resume)
		router.With(
			admin.RequirePermission("relay_nodes.revoke"),
			admin.RequireStepUp(adminAuthService),
		).Post("/relay-nodes/{node_id}/revoke", relayHandler.Revoke)
	})
	router.Post("/internal/v1/relay-nodes/enroll", relayHandler.Enroll)
	router.Post("/internal/v1/relay-nodes/{node_id}/certificate/renew", relayHandler.RenewCertificate)
	router.Route("/internal/v1/relay-nodes", func(router chi.Router) {
		router.Use(adminNetworkGuard.Middleware)
		router.Use(adminAuthenticator.Middleware)
		router.Get("/", relayHandler.List)
		router.Get("/{node_id}", relayHandler.Get)
		router.Post("/{node_id}/drain", relayHandler.Drain)
		router.Post("/{node_id}/resume", relayHandler.Resume)
		router.Post("/{node_id}/revoke", relayHandler.Revoke)
	})
	router.With(adminNetworkGuard.Middleware, adminAuthenticator.Middleware).
		Post("/internal/v1/relay-signing-keys/{key_id}/activate", relayHandler.ActivateSigningKey)
	updateService, err := updateservice.NewService(cfg.Update, cfg.Environment, relayService)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize update service: %w", err)
	}
	updateService.SetStrictRosterV1(cfg.MatchLobby.StrictRosterV1Enabled)
	if updateService.EphemeralSigner() {
		logger.Warn("using ephemeral development update-signing key; manifests will not verify after restart")
	}
	if cfg.Downloads.Enabled {
		updateService.SetManagedReleaseURLs(cfg.Downloads.PublicBaseURL, cfg.Downloads.PublicProbeBase())
	}
	updateHandler := updateservice.NewHTTPHandler(updateService, logger)
	adminReleaseService := admin.NewReleaseService(
		dbPool.Pool,
		admin.NewReleaseRepository(dbPool.Pool),
		adminRepository,
		updateService,
		cfg.Update.Product,
	)
	updateService.SetManagedCatalog(adminReleaseService)
	vntVersionPolicy.SetPublishedCatalog(updateService)
	adminReleaseHandler := admin.NewReleaseHTTPHandler(
		adminReleaseService, logger, cfg.HTTP.TrustProxyHeaders,
	)
	downloadRepository := download.NewRepository(dbPool.Pool)
	var downloadStorage download.ObjectStorage
	if cfg.Downloads.Enabled {
		downloadStorage, err = download.NewS3Storage(ctx, cfg.Downloads)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize download object storage: %w", err)
		}
	}
	downloadService := download.NewService(dbPool.Pool, downloadRepository, downloadStorage, cfg.Downloads)
	downloadHandler := download.NewHTTPHandler(downloadService, logger)
	adminDownloadHandler := admin.NewDownloadHTTPHandler(downloadService, logger, cfg.HTTP.TrustProxyHeaders)
	adminRouter.Group(func(router chi.Router) {
		router.Use(adminSessionAuthenticator.Middleware)
		router.With(admin.RequirePermission("updates.read")).Get("/releases", adminReleaseHandler.List)
		router.With(admin.RequirePermission("updates.read")).Get("/releases/{release_id}", adminReleaseHandler.Get)
		router.With(admin.RequirePermission("updates.create")).Post("/releases", adminReleaseHandler.Create)
		router.With(admin.RequirePermission("updates.create")).Post("/releases/{release_id}/validate", adminReleaseHandler.Validate)
		router.With(
			admin.RequirePermission("updates.publish"),
			admin.RequireStepUp(adminAuthService),
		).Post("/releases/{release_id}/publish", adminReleaseHandler.Publish)
		router.With(
			admin.RequirePermission("updates.rollback"),
			admin.RequireStepUp(adminAuthService),
		).Post("/releases/{release_id}/rollback", adminReleaseHandler.Rollback)
		router.With(
			admin.RequirePermission("updates.rollback"),
			admin.RequireStepUp(adminAuthService),
		).Post("/releases/{release_id}/archive", adminReleaseHandler.Archive)
		router.With(admin.RequirePermission("updates.create")).Get("/release-files", adminDownloadHandler.ListReleaseFiles)
		router.With(admin.RequirePermission("downloads.read")).Get("/downloads/capabilities", adminDownloadHandler.Capabilities)
		router.With(admin.RequirePermission("downloads.read")).Get("/download-categories", adminDownloadHandler.ListCategories)
		router.With(admin.RequirePermission("downloads.create")).Post("/download-categories", adminDownloadHandler.CreateCategory)
		router.With(admin.RequirePermission("downloads.update")).Patch("/download-categories/{category_id}", adminDownloadHandler.UpdateCategory)
		router.With(
			admin.RequirePermission("downloads.archive"),
			admin.RequireStepUp(adminAuthService),
		).Post("/download-categories/{category_id}/archive", adminDownloadHandler.ArchiveCategory)
		router.With(admin.RequirePermission("downloads.read")).Get("/downloads", adminDownloadHandler.ListEntries)
		router.With(admin.RequirePermission("downloads.read")).Get("/downloads/{download_id}", adminDownloadHandler.GetEntry)
		router.With(admin.RequirePermission("downloads.create")).Post("/downloads", adminDownloadHandler.CreateEntry)
		router.With(admin.RequirePermission("downloads.update")).Patch("/downloads/{download_id}", adminDownloadHandler.UpdateEntry)
		router.With(
			admin.RequirePermission("downloads.archive"),
			admin.RequireStepUp(adminAuthService),
		).Post("/downloads/{download_id}/archive", adminDownloadHandler.ArchiveEntry)
		router.With(admin.RequirePermission("downloads.create")).Post("/downloads/{download_id}/uploads", adminDownloadHandler.CreateUpload)
		router.With(admin.RequirePermission("downloads.read")).Get("/download-uploads/{upload_id}", adminDownloadHandler.GetUpload)
		router.With(admin.RequirePermission("downloads.create")).Post("/download-uploads/{upload_id}/parts", adminDownloadHandler.SignParts)
		router.With(admin.RequirePermission("downloads.create")).Post("/download-uploads/{upload_id}/complete", adminDownloadHandler.CompleteUpload)
		router.With(admin.RequirePermission("downloads.create")).Post("/download-uploads/{upload_id}/abort", adminDownloadHandler.AbortUpload)
		router.With(
			admin.RequirePermission("downloads.publish"),
			admin.RequireStepUp(adminAuthService),
		).Post("/download-versions/{version_id}/publish", adminDownloadHandler.Publish)
		router.With(
			admin.RequirePermission("downloads.archive"),
			admin.RequireStepUp(adminAuthService),
		).Post("/download-versions/{version_id}/archive", adminDownloadHandler.ArchiveVersion)
	})
	router.Mount("/v1/admin", adminRouter)
	router.Get("/v1/downloads", downloadHandler.Catalog)
	router.Get("/v1/downloads/files/{version_id}", downloadHandler.File)
	router.Get("/v1/updates/check", updateHandler.Check)
	router.Get("/v1/updates/{platform}/{version}/manifest", updateHandler.Manifest)
	router.Get("/v1/updates/files/{file_id}", updateHandler.File)
	router.Get("/v1/client/config", updateHandler.ClientConfig)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.", nil)
	})
	gameServerSweeper := gameserver.NewSweeper(gameServerService, cfg.GameServer.SweepInterval(), logger)
	p2pRoomSweeper := p2proom.NewSweeper(p2pRoomService, cfg.P2PRoom.SweepInterval(), logger)
	matchLobbySweeper := matchlobby.NewSweeper(
		matchLobbyService, time.Duration(cfg.MatchLobby.SweepIntervalSeconds)*time.Second, logger,
	)
	vntNodeSweeper := vnt.NewSweeper(vntService, 30*time.Second, logger)
	connectionSweeper := connection.NewSweeper(connectionService, cfg.Connection.SweepInterval(), logger)
	relaySweeper := relayregistry.NewSweeper(relayService, cfg.RelayRegistry.SweepInterval(), logger)
	relayMigrationSweeper := relayregistry.NewMigrationSweeper(relayService, cfg.RelayRegistry.SweepInterval(), logger)
	p2pBattleLogFinalizer := p2pbattlelog.NewFinalizer(
		p2pBattleLogService, cfg.P2PBattleLog.FinalizerInterval(), logger,
	)
	downloadWorker := download.NewWorker(downloadService, cfg.Downloads.VerificationInterval(), logger)
	return appmiddleware.Chain(router, cfg, logger, limiter, metrics), []backgroundService{
		gameServerSweeper, p2pRoomSweeper, matchLobbySweeper, vntNodeSweeper, connectionSweeper, relaySweeper, relayMigrationSweeper,
		p2pBattleLogFinalizer, downloadWorker, realtimeHub, relayControlServer,
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	runCtx, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	s.background.Add(len(s.backgroundServices))
	for _, service := range s.backgroundServices {
		go func(service backgroundService) {
			defer s.background.Done()
			service.Run(runCtx)
		}(service)
	}
	errorCh := make(chan error, 1)
	go func() {
		s.logger.Info("control-plane listening", "address", s.cfg.HTTP.Addr)
		errorCh <- s.httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		cancelBackground()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout())
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("HTTP shutdown: %w", err)
		}
		s.background.Wait()
		err := <-errorCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	case err := <-errorCh:
		cancelBackground()
		s.background.Wait()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	}
}

func (s *Server) Close() {
	if s.cache != nil {
		_ = s.cache.Close()
	}
	if s.database != nil {
		s.database.Close()
	}
}
