package metaserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/cache"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/health"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg       *config.Config
	logger    *slog.Logger
	database  *database.Pool
	cache     *cache.Client
	http      *http.Server
	tcp       *TCPServer
	scheduler *Scheduler
}

type schemaChecker struct{ database *database.Pool }

func (c schemaChecker) Check(ctx context.Context) error {
	var applied bool
	if err := c.database.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 31)
	`).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		return errors.New("MetaServer database migrations are not applied")
	}
	return nil
}

func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if err := cfg.ValidateMetaServer(); err != nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dbPool, err := database.Open(startupCtx, cfg.Database)
	if err != nil {
		return nil, err
	}
	redisClient, err := cache.Open(startupCtx, cfg.Redis)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	fail := func(err error) (*Server, error) {
		_ = redisClient.Close()
		dbPool.Close()
		return nil, err
	}

	tokenManager, err := auth.NewTokenVerifier(cfg.Auth)
	if err != nil {
		return fail(fmt.Errorf("initialize MetaServer access token verifier: %w", err))
	}
	authService := auth.NewService(
		dbPool.Pool, auth.NewRepository(), player.NewRepository(),
		tokenManager, nil, cfg.Auth, logger,
	)
	adminTokenManager, err := auth.NewTokenVerifier(cfg.Admin.AccessTokenConfig())
	if err != nil {
		return fail(fmt.Errorf("initialize MetaServer administrator token verifier: %w", err))
	}
	adminSessionVerifier := admin.NewSessionVerifier(
		admin.NewAuthRepository(dbPool.Pool),
		adminTokenManager,
	)
	adminSessionAuthenticator := admin.NewSessionAuthenticator(adminSessionVerifier)
	adminNetworkGuard, err := admin.NewNetworkGuard(cfg.Admin.TrustedCIDRs, cfg.HTTP.TrustProxyHeaders)
	if err != nil {
		return fail(err)
	}

	repository := NewRepository(
		dbPool.Pool,
		time.Duration(cfg.GameServer.UnhealthyAfterSeconds)*time.Second,
	)
	metrics := NewMetaMetrics()
	repository.SetMetrics(metrics)
	metrics.SetQueueProbe(func(ctx context.Context) (int64, error) {
		var depth int64
		err := dbPool.QueryRow(ctx, `
			SELECT COUNT(*) FROM meta_match_tickets WHERE state = 'QUEUED'
		`).Scan(&depth)
		return depth, err
	})
	gates := NewGateStore(redisClient, time.Duration(cfg.MetaServer.GateTicketTTLSeconds)*time.Second)
	gates.SetMetrics(metrics)
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		return fail(fmt.Errorf("load pinned MetaServer definitions: %w", err))
	}
	service := NewService(
		repository, gates, cfg.MetaServer.ProtocolVersion,
		cfg.MetaServer.PublicLogicEndpoint,
		time.Duration(cfg.MetaServer.MatchTicketTTLSeconds)*time.Second,
		time.Duration(cfg.MetaServer.RelayFreshnessSeconds)*time.Second,
		cfg.MetaServer.MaxFrameBytes,
		definitions,
	)
	handler := NewHTTPHandler(service, repository, logger)
	adminHandler := NewMetaAdminHandler(repository, service, logger, cfg.HTTP.TrustProxyHeaders)
	router := chi.NewRouter()
	healthHandler := health.NewHandler([]health.Dependency{
		{Name: "postgres", Checker: dbPool},
		{Name: "redis", Checker: redisClient},
		{Name: "schema", Checker: schemaChecker{database: dbPool}},
	}, cfg.Database.HealthTimeout(), logger)
	router.Get("/health/live", healthHandler.Live)
	router.Get("/health/ready", healthHandler.Ready)
	router.Get("/", handler.Root)
	router.Get("/v1/meta/regions", handler.Regions)
	router.Get("/v1/meta/playlists", handler.Playlists)
	router.Get("/v1/meta/notifications", handler.Notifications)
	router.Get("/internal/metrics", metrics.Handler().ServeHTTP)

	playerMiddleware := []func(http.Handler) http.Handler{
		auth.RequireAccess(authService, logger),
		auth.RequireActive,
		auth.RequireVerified,
	}
	router.With(playerMiddleware...).Post("/connectServer", handler.ConnectServer)
	router.Route("/v1", func(router chi.Router) {
		router.Group(func(router chi.Router) {
			router.Use(playerMiddleware...)
			router.Post("/meta/sessions", handler.Session)
			router.Get("/users/me/meta-profile", handler.Profile)
			router.Get("/users/me/loadouts", handler.ListLoadouts)
			router.Get("/users/me/loadouts/{role_id}", handler.GetLoadout)
			router.Put("/users/me/loadouts/{role_id}", handler.PutLoadout)
			router.Post("/meta/parties", handler.CreateParty)
			router.Get("/meta/parties/{party_id}", handler.GetParty)
			router.Post("/meta/parties/{party_id}/ready", handler.Ready)
			router.Post("/meta/parties/{party_id}/presence", handler.Presence)
			router.Post("/meta/matchmaking/tickets", handler.CreateMatchTicket)
			router.Get("/meta/matchmaking/tickets/{ticket_id}", handler.GetMatchTicket)
			router.Delete("/meta/matchmaking/tickets/{ticket_id}", handler.CancelMatchTicket)
		})
	})
	router.Route("/internal/v1/meta", func(router chi.Router) {
		router.Use(handler.RequireGameServer)
		router.Get("/matches/{match_id}/players/{player_id}/loadout", handler.InternalLoadout)
		router.Post("/matches/{match_id}/players/{player_id}/connected", handler.InternalConnected)
		router.Post("/matches/{match_id}/completed", handler.InternalCompleted)
		router.Put("/battlelog/reports/{report_id}", handler.InternalBattleLog)
	})
	router.Route("/v1/admin/meta", func(router chi.Router) {
		router.Use(adminNetworkGuard.Middleware)
		router.Use(adminSessionAuthenticator.Middleware)
		router.With(admin.RequirePermission("meta.read")).Get("/overview", adminHandler.Overview)
		router.With(admin.RequirePermission("meta.loadouts.read")).
			Get("/players/{player_id}/loadouts", adminHandler.PlayerLoadouts)
		router.With(
			admin.RequirePermission("meta.loadouts.update"),
			admin.RequireStepUp(adminSessionVerifier),
		).Put("/players/{player_id}/loadouts/{role_id}", adminHandler.PutPlayerLoadout)
		router.With(admin.RequirePermission("meta.read")).Get("/matches", adminHandler.ListMatches)
		router.With(
			admin.RequirePermission("meta.matches.manage"),
			admin.RequireStepUp(adminSessionVerifier),
		).Post("/matches/{match_id}/cancel", adminHandler.CancelMatch)
		router.With(
			admin.RequirePermission("meta.content.manage"),
			admin.RequireStepUp(adminSessionVerifier),
		).Put("/playlists/{slug}", adminHandler.UpsertPlaylist)
		router.With(
			admin.RequirePermission("meta.content.manage"),
			admin.RequireStepUp(adminSessionVerifier),
		).Put("/notifications/{notification_id}", adminHandler.UpsertNotification)
	})
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.", nil)
	})
	limiter := appmiddleware.NewIPRateLimiter(
		cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst, cfg.HTTP.TrustProxyHeaders,
	)
	return &Server{
		cfg: cfg, logger: logger, database: dbPool, cache: redisClient,
		http: &http.Server{
			Addr:              cfg.MetaServer.HTTPAddr,
			Handler:           appmiddleware.Chain(router, cfg, logger, limiter, metrics),
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout(),
			ReadTimeout:       cfg.HTTP.ReadTimeout(),
			WriteTimeout:      cfg.HTTP.WriteTimeout(),
			IdleTimeout:       cfg.HTTP.IdleTimeout(),
		},
		tcp: NewTCPServer(cfg.MetaServer, service, gates, metrics, logger),
		scheduler: NewScheduler(
			dbPool.Pool,
			time.Duration(cfg.MetaServer.SchedulerIntervalSeconds)*time.Second,
			time.Duration(cfg.GameServer.UnhealthyAfterSeconds)*time.Second,
			time.Duration(cfg.MetaServer.MatchReservationTTLSeconds)*time.Second,
			metrics,
			logger,
		),
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		s.scheduler.Run(runCtx)
	}()
	errorsCh := make(chan error, 2)
	go func() {
		s.logger.Info("MetaServer HTTP listening", "address", s.cfg.MetaServer.HTTPAddr)
		errorsCh <- s.http.ListenAndServe()
	}()
	go func() {
		s.logger.Info("MetaServer logic listening", "address", s.cfg.MetaServer.LogicAddr)
		errorsCh <- s.tcp.Run(runCtx)
	}()

	select {
	case <-ctx.Done():
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(), s.cfg.HTTP.ShutdownTimeout(),
		)
		defer shutdownCancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("MetaServer HTTP shutdown: %w", err)
		}
		background.Wait()
		return nil
	case err := <-errorsCh:
		cancel()
		background.Wait()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
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
