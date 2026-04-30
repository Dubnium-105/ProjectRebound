package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/db"
	httppkg "github.com/projectrebound/matchserver/internal/http"
	"github.com/projectrebound/matchserver/internal/lifecycle"
	"github.com/projectrebound/matchserver/internal/matchmaking"
	"github.com/projectrebound/matchserver/internal/store"
	"github.com/projectrebound/matchserver/internal/udp"
)

type Server struct {
	cfg        *config.Config
	db         *sql.DB
	httpServer *http.Server

	rendezvous *udp.RendezvousService
	relay      *udp.RelayService
	qos        *udp.QoSService
	probeSender *udp.ProbeSender

	sweeper            *lifecycle.Sweeper
	p2pMatcher         *matchmaking.P2PMatcher
	metaserverMatcher  *matchmaking.MetaServerMatcher

	wg sync.WaitGroup
}

func New(cfg *config.Config) (*Server, error) {
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	natStore := store.NewNatTraversalStore()
	relayStore := store.NewRelayStore()
	probeSender := udp.NewProbeSender()

	deps := &httppkg.Deps{
		DB:          database,
		Config:      cfg,
		NatStore:    natStore,
		RelayStore:  relayStore,
		ProbeSender: probeSender,
	}

	mux := http.NewServeMux()
	httppkg.RegisterRoutes(mux, deps)

	return &Server{
		cfg:        cfg,
		db:         database,
		httpServer: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: httppkg.WithMiddleware(mux),
		},
		rendezvous: udp.NewRendezvousService(natStore),
		relay:      udp.NewRelayService(relayStore),
		qos:        udp.NewQoSService(),
		probeSender: probeSender,
		sweeper:            lifecycle.New(database, &cfg.MatchServer, natStore, relayStore),
		p2pMatcher:         matchmaking.NewP2PMatcher(database, &cfg.MatchServer, natStore, relayStore),
		metaserverMatcher:  matchmaking.NewMetaServerMatcher(database, &cfg.MatchServer, natStore, relayStore),
	}, nil
}

func (s *Server) Start(ctx context.Context) {
	// HTTP
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		slog.Info("http server listening", "addr", s.cfg.HTTPAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
		}
	}()

	// UDP Rendezvous
	if err := s.rendezvous.Start(ctx, s.cfg.UDPRendezvousPort); err != nil {
		slog.Error("rendezvous start error", "error", err)
	} else {
		slog.Info("udp rendezvous listening", "port", s.cfg.UDPRendezvousPort)
	}

	// UDP Relay
	if err := s.relay.Start(ctx, s.cfg.UDPRelayPort); err != nil {
		slog.Error("relay start error", "error", err)
	} else {
		slog.Info("udp relay listening", "port", s.cfg.UDPRelayPort)
	}

	// UDP QoS
	if err := s.qos.Start(ctx, s.cfg.UDPQoSPort); err != nil {
		slog.Error("qos start error", "error", err)
	} else {
		slog.Info("udp qos listening", "port", s.cfg.UDPQoSPort)
	}

	// Lifecycle sweeper
	s.wg.Add(1)
	go s.sweeper.Run(ctx, &s.wg)

	// P2P Matchmaker
	s.wg.Add(1)
	go s.p2pMatcher.Run(ctx, &s.wg)

	// MetaServer Matchmaker
	s.wg.Add(1)
	go s.metaserverMatcher.Run(ctx, &s.wg)
}

func (s *Server) Shutdown() error {
	slog.Info("shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Stop HTTP
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown error", "error", err)
	}

	// 2. Stop UDP services
	s.rendezvous.Shutdown()
	s.relay.Shutdown()
	s.qos.Shutdown()

	// 3. Wait for all background goroutines
	s.wg.Wait()

	// 4. Close database
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	slog.Info("shutdown complete")
	return nil
}
