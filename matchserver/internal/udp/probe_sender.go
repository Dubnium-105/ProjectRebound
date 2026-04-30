package udp

import (
	"fmt"
	"net"
)

type ProbeSender struct{}

func NewProbeSender() *ProbeSender {
	return &ProbeSender{}
}

func (s *ProbeSender) Send(host string, port int, nonce string) error {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(nonce))
	return err
}
