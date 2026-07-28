package metaserver

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const maximumProxyProtocolLine = 108

type proxyProtocolConn struct {
	net.Conn
	reader     *bufio.Reader
	remoteAddr net.Addr
}

func (c *proxyProtocolConn) Read(target []byte) (int, error) {
	return c.reader.Read(target)
}

func (c *proxyProtocolConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// acceptProxyProtocolV1 accepts a single HAProxy PROXY v1 header from the
// trusted Logic transport. It is enabled explicitly because accepting this
// header on an Internet-facing listener would allow source-address spoofing.
func acceptProxyProtocolV1(connection net.Conn, timeout time.Duration) (net.Conn, error) {
	if err := connection.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(connection, maximumProxyProtocolLine+1)
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maximumProxyProtocolLine {
		return nil, errors.New("PROXY protocol header is too large")
	}
	if err != nil {
		return nil, fmt.Errorf("read PROXY protocol header: %w", err)
	}
	if !strings.HasSuffix(string(line), "\r\n") {
		return nil, errors.New("PROXY protocol header is not CRLF terminated")
	}
	fields := strings.Fields(strings.TrimSuffix(string(line), "\r\n"))
	if len(fields) != 6 || fields[0] != "PROXY" ||
		(fields[1] != "TCP4" && fields[1] != "TCP6") {
		return nil, errors.New("unsupported PROXY protocol header")
	}
	sourceIP := net.ParseIP(fields[2])
	destinationIP := net.ParseIP(fields[3])
	sourcePort, sourceErr := strconv.Atoi(fields[4])
	destinationPort, destinationErr := strconv.Atoi(fields[5])
	if sourceIP == nil || destinationIP == nil ||
		sourceErr != nil || destinationErr != nil ||
		sourcePort < 1 || sourcePort > 65535 ||
		destinationPort < 1 || destinationPort > 65535 {
		return nil, errors.New("invalid PROXY protocol address")
	}
	if fields[1] == "TCP4" &&
		(sourceIP.To4() == nil || destinationIP.To4() == nil) {
		return nil, errors.New("PROXY TCP4 header contains a non-IPv4 address")
	}
	if fields[1] == "TCP6" &&
		(sourceIP.To4() != nil || destinationIP.To4() != nil) {
		return nil, errors.New("PROXY TCP6 header contains a non-IPv6 address")
	}
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &proxyProtocolConn{
		Conn: connection, reader: reader,
		remoteAddr: &net.TCPAddr{IP: sourceIP, Port: sourcePort},
	}, nil
}
