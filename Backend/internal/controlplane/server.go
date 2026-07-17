package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/api"
	"github.com/projectrebound/matchserver/internal/auth"
	"github.com/projectrebound/matchserver/internal/cache"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/health"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/player"
)

type Server struct {
	cfg        *config.Config
	logger     *slog.Logger
	database   *database.Pool
	cache      *cache.Client
	httpServer *http.Server
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
	handler, err := buildHandler(cfg, logger, dbPool, redisClient, limiter)
	if err != nil {
		_ = redisClient.Close()
		dbPool.Close()
		return nil, err
	}

	return &Server{
		cfg:      cfg,
		logger:   logger,
		database: dbPool,
		cache:    redisClient,
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
) (http.Handler, error) {
	router := chi.NewRouter()
	healthHandler := health.NewHandler([]health.Dependency{
		{Name: "postgres", Checker: dbPool},
		{Name: "redis", Checker: redisClient},
	}, cfg.Database.HealthTimeout(), logger)
	router.Get("/health/live", healthHandler.Live)
	router.Get("/health/ready", healthHandler.Ready)

	tokenManager, ephemeralKey, err := auth.NewTokenManager(cfg.Auth, cfg.Environment)
	if err != nil {
		return nil, fmt.Errorf("initialize access token signer: %w", err)
	}
	if ephemeralKey {
		logger.Warn("using ephemeral development access-token key; tokens will not survive a restart")
	}
	authService := auth.NewService(
		dbPool.Pool,
		auth.NewRepository(),
		player.NewRepository(),
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
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.", nil)
	})
	return appmiddleware.Chain(router, cfg, logger, limiter), nil
}

func (s *Server) Run(ctx context.Context) error {
	errorCh := make(chan error, 1)
	go func() {
		s.logger.Info("control-plane listening", "address", s.cfg.HTTP.Addr)
		errorCh <- s.httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout())
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("HTTP shutdown: %w", err)
		}
		err := <-errorCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	case err := <-errorCh:
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
