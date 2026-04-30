package udp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/projectrebound/matchserver/internal/store"
)

type RendezvousService struct {
	store  *store.NatTraversalStore
	conn   *net.UDPConn
	wg     sync.WaitGroup
}

func NewRendezvousService(natStore *store.NatTraversalStore) *RendezvousService {
	return &RendezvousService{store: natStore}
}

func (s *RendezvousService) Start(ctx context.Context, port int) error {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("rendezvous listen: %w", err)
	}
	s.conn = conn

	s.wg.Add(1)
	go s.run(ctx)
	return nil
}

func (s *RendezvousService) run(ctx context.Context) {
	defer s.wg.Done()

	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()

	buf := make([]byte, 1500)
	for {
		n, remote, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("rendezvous read error", "error", err)
			continue
		}

		var pkt struct {
			Type      string `json:"type"`
			Token     string `json:"token"`
			LocalPort int    `json:"localPort"`
		}
		if err := json.Unmarshal(buf[:n], &pkt); err != nil || pkt.Token == "" {
			continue
		}

		binding := s.store.ObserveBinding(pkt.Token, remote)
		if binding == nil {
			continue
		}

		resp := map[string]any{
			"type":       "nat-binding",
			"token":      binding.BindingToken,
			"publicIp":   binding.PublicIP,
			"publicPort": binding.PublicPort,
		}
		respBytes, _ := json.Marshal(resp)
		s.conn.WriteToUDP(respBytes, remote)
	}
}

func (s *RendezvousService) Shutdown() {
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
}
