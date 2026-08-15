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
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maximumHTTPBody = 2 << 20

type tunnel struct {
	metaBaseURL     *url.URL
	logicAddress    string
	logicServerName string
	accessToken     atomic.Value
	localTCP        net.Listener
	httpProxy       *httputil.ReverseProxy
	logger          *slog.Logger
}

type rewriteConnectEndpointKey struct{}

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
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" {
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
	tokenReader := bufio.NewReaderSize(os.Stdin, 16*1024)
	token, err := readAccessToken(tokenReader)
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
		logicServerName: *logicServerName, localTCP: localTCP, logger: logger,
	}
	instance.setAccessToken(token)
	instance.httpProxy = instance.newHTTPProxy(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
	})
	httpServer := &http.Server{
		Handler:           http.HandlerFunc(instance.serveHTTP),
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go instance.watchAccessTokens(ctx, tokenReader)
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

func readAccessToken(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return "", io.EOF
	}
	if len(line) > 16*1024 {
		return "", errors.New("access token is too large")
	}
	token := strings.TrimSpace(line)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("access token is missing or malformed")
	}
	return token, nil
}

func (t *tunnel) setAccessToken(token string) {
	t.accessToken.Store(token)
}

func (t *tunnel) currentAccessToken() string {
	return t.accessToken.Load().(string)
}

func (t *tunnel) watchAccessTokens(ctx context.Context, reader *bufio.Reader) {
	for {
		token, err := readAccessToken(reader)
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		if err != nil {
			t.logger.Warn("ignored malformed access token update", "error", err)
			continue
		}
		t.setAccessToken(token)
		t.logger.Info("access token updated")
	}
}

func (t *tunnel) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/_meta-tunnel/health/live" {
		t.health(w, r)
		return
	}
	if r.URL.Path == "/connectServer" {
		t.connectServer(w, r)
		return
	}
	t.httpProxy.ServeHTTP(w, r)
}

func (t *tunnel) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, `{"status":"live"}`+"\n")
	}
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
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	ctx := context.WithValue(r.Context(), rewriteConnectEndpointKey{}, true)
	t.httpProxy.ServeHTTP(w, r.WithContext(ctx))
}

func (t *tunnel) newHTTPProxy(transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(t.metaBaseURL)
			request.Out.Host = t.metaBaseURL.Host
			request.Out.Header.Set("Authorization", "Bearer "+t.currentAccessToken())
			request.Out.Header.Del("X-Forwarded-For")
			request.Out.Header.Del("X-Forwarded-Host")
			request.Out.Header.Del("X-Forwarded-Proto")
			request.Out.Header.Del("Forwarded")
			if request.Out.Context().Value(rewriteConnectEndpointKey{}) == true {
				request.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
		ModifyResponse: t.modifyHTTPResponse,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			t.logger.Warn("MetaServer HTTP bridge failed", "error", err)
			http.Error(w, "MetaServer unavailable", http.StatusBadGateway)
		},
		Transport:     transport,
		FlushInterval: -1,
	}
}

func (t *tunnel) modifyHTTPResponse(response *http.Response) error {
	requestPath := ""
	requestMethod := ""
	requestID := response.Header.Get("X-Request-ID")
	if response.Request != nil {
		requestMethod = response.Request.Method
		if response.Request.URL != nil {
			// Deliberately exclude RawQuery: compatibility requests may carry
			// identifiers there and the tunnel must never log credentials or
			// user-controlled payload data.
			requestPath = response.Request.URL.Path
		}
	}
	logFields := []any{
		"method", requestMethod,
		"path", requestPath,
		"status", response.StatusCode,
		"request_id", requestID,
	}
	if response.StatusCode >= http.StatusBadRequest {
		t.logger.Warn("MetaServer HTTP bridge response", logFields...)
	} else {
		t.logger.Info("MetaServer HTTP bridge response", logFields...)
	}

	if response.Request.Context().Value(rewriteConnectEndpointKey{}) != true ||
		response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumHTTPBody+1))
	_ = response.Body.Close()
	if err != nil || len(body) > maximumHTTPBody {
		return errors.New("invalid MetaServer connect response")
	}
	body, err = rewriteConnectEndpoint(body, t.localTCP.Addr().String())
	if err != nil {
		return errors.New("invalid MetaServer connect response")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Content-Type", "application/json; charset=utf-8")
	response.Header.Del("Content-Encoding")
	response.Header.Del("Content-MD5")
	response.Header.Del("ETag")
	return nil
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
