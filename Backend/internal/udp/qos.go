package udp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

type QoSService struct {
	conn *net.UDPConn
	wg   sync.WaitGroup
}

func NewQoSService() *QoSService {
	return &QoSService{}
}

func (s *QoSService) Start(ctx context.Context, port int) error {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("qos listen: %w", err)
	}
	s.conn = conn

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	s.wg.Add(1)
	go s.run(ctx)
	return nil
}

func (s *QoSService) run(ctx context.Context) {
	defer s.wg.Done()

	buf := make([]byte, 1500)
	for {
		n, remote, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("qos read error", "error", err)
			continue
		}

		if n < 11 || buf[0] != 0x59 {
			continue
		}

		resp := make([]byte, 2+n-11)
		resp[0] = 0x95
		resp[1] = 0x00
		copy(resp[2:], buf[11:n])
		s.conn.WriteToUDP(resp, remote)
	}
}

func (s *QoSService) Shutdown() {
	if s.conn != nil {
		s.conn.Close()
	}
	s.wg.Wait()
}
