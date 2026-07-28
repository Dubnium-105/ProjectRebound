package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maximumHTTPBody = 2 << 20

type tunnel struct {
	metaBaseURL     *url.URL
	logicAddress    string
	logicServerName string
	accessToken     string
	localTCP        net.Listener
	httpClient      *http.Client
	logger          *slog.Logger
}

func main() {
	metaBase := flag.String("meta-base-url", "https://meta.dubnium.top", "public MetaServer HTTPS base URL")
	logicAddress := flag.String("logic-address", "logic.dubnium.top:443", "public MetaServer native TLS endpoint")
	logicServerName := flag.String("logic-server-name", "logic.dubnium.top", "TLS server name for the native endpoint")
	httpListen := flag.String("http-listen", "127.0.0.1:0", "loopback HTTP listen address")
	tcpListen := flag.String("tcp-listen", "127.0.0.1:0", "loopback native TCP listen address")
	tokenStdin := flag.Bool("token-stdin", true, "read the access token from stdin")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	baseURL, err := url.Parse(*metaBase)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.RawQuery != "" {
		logger.Error("invalid MetaServer base URL")
		os.Exit(2)
	}
	if !validServerName(*logicServerName) {
		logger.Error("invalid logic TLS server name")
		os.Exit(2)
	}
	if _, _, err := net.SplitHostPort(*logicAddress); err != nil {
		logger.Error("invalid logic endpoint")
		os.Exit(2)
	}
	if !isLoopbackListen(*httpListen) || !isLoopbackListen(*tcpListen) {
		logger.Error("MetaTunnel listeners must bind loopback")
		os.Exit(2)
	}
	if !*tokenStdin {
		logger.Error("an anonymous stdin pipe is required for the access token")
		os.Exit(2)
	}
	token, err := readAccessToken(os.Stdin)
	if err != nil {
		logger.Error("read access token", "error", err)
		os.Exit(2)
	}

	localTCP, err := net.Listen("tcp", *tcpListen)
	if err != nil {
		logger.Error("listen for local native protocol", "error", err)
		os.Exit(1)
	}
	localHTTP, err := net.Listen("tcp", *httpListen)
	if err != nil {
		_ = localTCP.Close()
		logger.Error("listen for local MetaServer HTTP", "error", err)
		os.Exit(1)
	}
	instance := &tunnel{
		metaBaseURL: baseURL, logicAddress: *logicAddress,
		logicServerName: *logicServerName, accessToken: token, localTCP: localTCP,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
		logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", instance.health)
	mux.HandleFunc("/connectServer", instance.connectServer)
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- instance.serveTCP(ctx) }()
	go func() { errorsCh <- httpServer.Serve(localHTTP) }()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"event": "ready", "http_url": "http://" + localHTTP.Addr().String(),
		"logic_endpoint": localTCP.Addr().String(), "pid": os.Getpid(),
	})

	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			logger.Error("MetaTunnel stopped", "error", err)
		}
	}
	stop()
	_ = localTCP.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func readAccessToken(reader io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(reader, 16*1024)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	token := strings.TrimSpace(line)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("access token is missing or malformed")
	}
	return token, nil
}

func (t *tunnel) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"live"}`+"\n")
}

func (t *tunnel) connectServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maximumHTTPBody+1))
	if err != nil || len(body) > maximumHTTPBody {
		http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	target := t.metaBaseURL.ResolveReference(&url.URL{Path: "/connectServer"})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "create upstream request", http.StatusBadGateway)
		return
	}
	request.Header.Set("Authorization", "Bearer "+t.accessToken)
	request.Header.Set("Content-Type", "application/json")
	if requestID := strings.TrimSpace(r.Header.Get("X-Request-Id")); requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	response, err := t.httpClient.Do(request)
	if err != nil {
		t.logger.Warn("MetaServer HTTP bridge failed", "error", err)
		http.Error(w, "MetaServer unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumHTTPBody+1))
	if err != nil || len(responseBody) > maximumHTTPBody {
		http.Error(w, "invalid MetaServer response", http.StatusBadGateway)
		return
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		responseBody, err = rewriteConnectEndpoint(responseBody, t.localTCP.Addr().String())
		if err != nil {
			http.Error(w, "invalid MetaServer response", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func rewriteConnectEndpoint(body []byte, endpoint string) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if data, ok := response["data"].(map[string]any); ok {
		data["endpoint"] = endpoint
	} else {
		response["endpoint"] = endpoint
	}
	return json.Marshal(response)
}

func (t *tunnel) serveTCP(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = t.localTCP.Close()
	}()
	for {
		local, err := t.localTCP.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go t.bridge(local)
	}
}

func (t *tunnel) bridge(local net.Conn) {
	defer local.Close()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	remote, err := tls.DialWithDialer(dialer, "tcp", t.logicAddress, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: t.logicServerName,
	})
	if err != nil {
		t.logger.Warn("MetaServer TLS bridge failed", "error", err)
		return
	}
	defer remote.Close()
	var copyGroup sync.WaitGroup
	copyGroup.Add(2)
	go func() {
		defer copyGroup.Done()
		_, _ = io.Copy(remote, local)
		_ = remote.CloseWrite()
	}()
	go func() {
		defer copyGroup.Done()
		_, _ = io.Copy(local, remote)
		if tcp, ok := local.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	copyGroup.Wait()
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validServerName(value string) bool {
	return value != "" && !strings.ContainsAny(value, "/:\\ \t\r\n")
}
