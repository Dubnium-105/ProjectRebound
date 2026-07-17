package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/admin"
	"github.com/projectrebound/matchserver/internal/api"
	"github.com/projectrebound/matchserver/internal/auth"
	"github.com/projectrebound/matchserver/internal/cache"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/gameserver"
	"github.com/projectrebound/matchserver/internal/health"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/player"
	"github.com/projectrebound/matchserver/internal/p2proom"
)

type Server struct {
	cfg               *config.Config
	logger            *slog.Logger
	database          *database.Pool
	cache             *cache.Client
	httpServer        *http.Server
	gameServerSweeper *gameserver.Sweeper
	p2pRoomSweeper    *p2proom.Sweeper
	background        sync.WaitGroup
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
	handler, gameServerSweeper, p2pRoomSweeper, err := buildHandler(cfg, logger, dbPool, redisClient, limiter)
	if err != nil {
		_ = redisClient.Close()
		dbPool.Close()
		return nil, err
	}

	return &Server{
		cfg:               cfg,
		logger:            logger,
		database:          dbPool,
		cache:             redisClient,
		gameServerSweeper: gameServerSweeper,
		p2pRoomSweeper:    p2pRoomSweeper,
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
	cfg *config.Config,
	logger *slog.Logger,
	dbPool *database.Pool,
	redisClient *cache.Client,
	limiter *appmiddleware.IPRateLimiter,
) (http.Handler, *gameserver.Sweeper, *p2proom.Sweeper, error) {
	router := chi.NewRouter()
	healthHandler := health.NewHandler([]health.Dependency{
		{Name: "postgres", Checker: dbPool},
		{Name: "redis", Checker: redisClient},
	}, cfg.Database.HealthTimeout(), logger)
	router.Get("/health/live", healthHandler.Live)
	router.Get("/health/ready", healthHandler.Ready)

	tokenManager, ephemeralKey, err := auth.NewTokenManager(cfg.Auth, cfg.Environment)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initialize access token signer: %w", err)
	}
	if ephemeralKey {
		logger.Warn("using ephemeral development access-token key; tokens will not survive a restart")
	}
	authRepository := auth.NewRepository()
	playerRepository := player.NewRepository()
	authService := auth.NewService(
		dbPool.Pool,
		authRepository,
		playerRepository,
		tokenManager,
		cfg.Auth,
		logger,
	)
	authHandler := auth.NewHTTPHandler(authService, logger, cfg.HTTP.TrustProxyHeaders)
	bindLimiter := appmiddleware.NewIPRateLimiter(
		float64(cfg.Auth.BindRequestsPerMinute)/60.0,
		cfg.Auth.BindBurst,
		cfg.HTTP.TrustProxyHeaders,
	)
	router.Route("/v1", func(router chi.Router) {
		router.With(bindLimiter.Middleware).Post("/auth/bind", authHandler.Bind)
		router.Post("/auth/refresh", authHandler.Refresh)
		router.With(auth.RequireAccess(authService, logger)).Post("/auth/logout", authHandler.Logout)
		router.With(auth.RequireAccess(authService, logger)).Get("/users/me", authHandler.Me)
	})

	adminAuthenticator, err := admin.NewAuthenticator(cfg.Admin)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initialize administrator authentication: %w", err)
	}
	if !adminAuthenticator.Configured() {
		logger.Warn("no administrator tokens configured; admin API will reject all requests")
	}
	adminNetworkGuard, err := admin.NewNetworkGuard(cfg.Admin.TrustedCIDRs, cfg.HTTP.TrustProxyHeaders)
	if err != nil {
		return nil, nil, nil, err
	}
	adminService := admin.NewService(dbPool.Pool, playerRepository, authRepository, admin.NewRepository())
	adminHandler := admin.NewHTTPHandler(adminService, logger, cfg.HTTP.TrustProxyHeaders)
	router.Route("/v1/admin", func(router chi.Router) {
		router.Use(adminNetworkGuard.Middleware)
		router.Use(adminAuthenticator.Middleware)
		router.Get("/players", adminHandler.ListPlayers)
		router.Get("/players/{player_id}", adminHandler.GetPlayer)
		router.Patch("/players/{player_id}", adminHandler.PatchPlayer)
		router.Post("/players/{player_id}/revoke-sessions", adminHandler.RevokeSessions)
	})

	gameServerRegistrationAuth, err := gameserver.NewRegistrationAuthenticator(cfg.GameServer.RegistrationTokenSet)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initialize game server registration authentication: %w", err)
	}
	if !gameServerRegistrationAuth.Configured() {
		logger.Warn("no game server registration tokens configured; registrations will be rejected")
	}
	gameServerRepository := gameserver.NewRepository(dbPool.Pool)
	gameServerService := gameserver.NewService(gameServerRepository, cfg.GameServer)
	gameServerHandler := gameserver.NewHTTPHandler(gameServerService, logger)
	router.With(gameServerRegistrationAuth.Middleware).Post("/v1/game-servers", gameServerHandler.Register)
	router.Get("/v1/game-servers", gameServerHandler.List)
	router.Get("/v1/game-servers/{server_id}", gameServerHandler.Get)
	router.Post("/v1/game-servers/{server_id}/heartbeat", gameServerHandler.Heartbeat)
	router.Delete("/v1/game-servers/{server_id}", gameServerHandler.Deregister)

	p2pRoomService := p2proom.NewService(p2proom.NewRepository(dbPool.Pool), cfg.P2PRoom)
	p2pRoomHandler := p2proom.NewHTTPHandler(p2pRoomService, logger)
	router.Get("/v1/p2p-rooms", p2pRoomHandler.List)
	router.Get("/v1/p2p-rooms/{room_id}", p2pRoomHandler.Get)
	router.Group(func(router chi.Router) {
		router.Use(auth.RequireAccess(authService, logger))
		router.Use(auth.RequireActive)
		router.Post("/v1/p2p-rooms", p2pRoomHandler.Create)
		router.Post("/v1/p2p-rooms/{room_id}/join", p2pRoomHandler.Join)
		router.Post("/v1/p2p-rooms/{room_id}/leave", p2pRoomHandler.Leave)
		router.Post("/v1/p2p-rooms/{room_id}/heartbeat", p2pRoomHandler.Heartbeat)
		router.Post("/v1/p2p-rooms/{room_id}/start", p2pRoomHandler.Start)
		router.Delete("/v1/p2p-rooms/{room_id}", p2pRoomHandler.Delete)
	})
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.", nil)
	})
	gameServerSweeper := gameserver.NewSweeper(gameServerService, cfg.GameServer.SweepInterval(), logger)
	p2pRoomSweeper := p2proom.NewSweeper(p2pRoomService, cfg.P2PRoom.SweepInterval(), logger)
	return appmiddleware.Chain(router, cfg, logger, limiter), gameServerSweeper, p2pRoomSweeper, nil
}

func (s *Server) Run(ctx context.Context) error {
	runCtx, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	s.background.Add(2)
	go func() {
		defer s.background.Done()
		s.gameServerSweeper.Run(runCtx)
	}()
	go func() {
		defer s.background.Done()
		s.p2pRoomSweeper.Run(runCtx)
	}()
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
