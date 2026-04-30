package udp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"sync"

	"github.com/projectrebound/matchserver/internal/store"
)

type forwardJob struct {
	data   []byte
	target net.UDPAddr
}

type RelayService struct {
	store    *store.RelayStore
	conn     *net.UDPConn
	workerCh chan forwardJob
	wg       sync.WaitGroup
}

func NewRelayService(relayStore *store.RelayStore) *RelayService {
	return &RelayService{store: relayStore}
}

func (s *RelayService) Start(ctx context.Context, port int) error {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("relay listen: %w", err)
	}
	s.conn = conn

	s.workerCh = make(chan forwardJob, 256)
	workers := runtime.GOMAXPROCS(0)
	for i := 0; i < workers; i++ {
		s.wg.Add(1)
		go s.runWorker(ctx)
	}

	s.wg.Add(1)
	go s.runReceiver(ctx)
	return nil
}

func (s *RelayService) runReceiver(ctx context.Context) {
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
			slog.Warn("relay read error", "error", err)
			continue
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])

		if s.handleControlPacket(packet, remote) {
			continue
		}

		s.forwardData(packet, remote)
	}
}

func (s *RelayService) handleControlPacket(data []byte, addr *net.UDPAddr) bool {
	if len(data) == 0 || data[0] != '{' {
		return false
	}

	var pkt struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
		Role      string `json:"role"`
		Secret    string `json:"secret"`
	}
	if err := json.Unmarshal(data, &pkt); err != nil {
		return false
	}

	if pkt.Type != "PRB_RELAY_REGISTER_V1" || pkt.SessionID == "" || pkt.Role == "" || pkt.Secret == "" {
		return false
	}

	reg := s.store.ObserveRegistration(pkt.SessionID, pkt.Role, pkt.Secret, addr)

	var resp map[string]any
	if reg == nil {
		resp = map[string]any{
			"type":    "PRB_RELAY_REGISTERED_V1",
			"ok":      false,
			"message": "relay registration rejected",
		}
	} else {
		resp = map[string]any{
			"type":         "PRB_RELAY_REGISTERED_V1",
			"ok":           true,
			"sessionId":    reg.SessionID,
			"role":         reg.Role,
			"observedIp":   reg.Endpoint.IP.String(),
			"observedPort": reg.Endpoint.Port,
		}
	}
	respBytes, _ := json.Marshal(resp)
	s.conn.WriteToUDP(respBytes, addr)
	return true
}

func (s *RelayService) forwardData(data []byte, source *net.UDPAddr) {
	targets, compression := s.store.GetForwardTargets(source)
	if len(targets) == 0 {
		return
	}

	out := maybeCompress(data, compression)

	for _, target := range targets {
		tmpTarget := *target
		select {
		case s.workerCh <- forwardJob{out, tmpTarget}:
		default:
			s.conn.WriteToUDP(out, &tmpTarget)
		}
	}
}

func (s *RelayService) runWorker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-s.workerCh:
			if !ok {
				return
			}
			s.conn.WriteToUDP(job.data, &job.target)
		}
	}
}

func (s *RelayService) Shutdown() {
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
}

