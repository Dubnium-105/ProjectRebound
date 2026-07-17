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
	"github.com/projectrebound/matchserver/internal/cache"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/health"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
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
	handler := buildHandler(cfg, logger, dbPool, redisClient, limiter)

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
) http.Handler {
	router := chi.NewRouter()
	healthHandler := health.NewHandler([]health.Dependency{
		{Name: "postgres", Checker: dbPool},
		{Name: "redis", Checker: redisClient},
	}, cfg.Database.HealthTimeout(), logger)
	router.Get("/health/live", healthHandler.Live)
	router.Get("/health/ready", healthHandler.Ready)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.", nil)
	})
	return appmiddleware.Chain(router, cfg, logger, limiter)
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
