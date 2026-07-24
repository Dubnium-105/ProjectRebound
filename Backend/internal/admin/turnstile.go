package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/config"
)

var ErrTurnstileUnavailable = errors.New("Turnstile verification is not configured")

type TurnstileResult struct {
	Success    bool
	ErrorCodes []string
	Hostname   string
	Action     string
	Latency    time.Duration
}

type TurnstileVerifier interface {
	Verify(context.Context, string, string) (TurnstileResult, error)
	Configured() bool
	SiteKey() string
}

type CloudflareTurnstileVerifier struct {
	client           *http.Client
	verifyURL        string
	siteKey          string
	secretKey        string
	expectedHostname string
	expectedAction   string
}

func NewCloudflareTurnstileVerifier(cfg config.AdminConfig) *CloudflareTurnstileVerifier {
	return &CloudflareTurnstileVerifier{
		client:           &http.Client{Timeout: cfg.TurnstileTimeout()},
		verifyURL:        strings.TrimSpace(cfg.TurnstileVerifyURL),
		siteKey:          strings.TrimSpace(cfg.TurnstileSiteKey),
		secretKey:        strings.TrimSpace(cfg.TurnstileSecretKey),
		expectedHostname: strings.TrimSpace(cfg.TurnstileExpectedHostname),
		expectedAction:   strings.TrimSpace(cfg.TurnstileExpectedAction),
	}
}

func (v *CloudflareTurnstileVerifier) Configured() bool {
	return v.siteKey != "" && v.secretKey != "" && v.verifyURL != "" &&
		v.expectedHostname != "" && v.expectedAction != ""
}

func (v *CloudflareTurnstileVerifier) SiteKey() string { return v.siteKey }

type siteverifyResponse struct {
	Success     bool     `json:"success"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	Action      string   `json:"action"`
	ErrorCodes  []string `json:"error-codes"`
}

func (v *CloudflareTurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) (TurnstileResult, error) {
	if !v.Configured() {
		return TurnstileResult{}, ErrTurnstileUnavailable
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 2048 {
		return TurnstileResult{ErrorCodes: []string{"invalid-input-response"}}, nil
	}
	form := url.Values{}
	form.Set("secret", v.secretKey)
	form.Set("response", token)
	form.Set("idempotency_key", uuid.NewString())
	if remoteIP = strings.TrimSpace(remoteIP); remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	startedAt := time.Now()
	var lastResult TurnstileResult
	for attempt := 0; attempt < 2; attempt++ {
		result, retryable, err := v.verifyOnce(ctx, form)
		lastResult = result
		if err == nil {
			result.Latency = time.Since(startedAt)
			return v.validateResult(result), nil
		}
		if !retryable || attempt == 1 {
			lastResult.Latency = time.Since(startedAt)
			return lastResult, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			lastResult.Latency = time.Since(startedAt)
			return lastResult, fmt.Errorf("retry Turnstile verification: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return lastResult, errors.New("Turnstile verification failed")
}

func (v *CloudflareTurnstileVerifier) verifyOnce(
	ctx context.Context,
	form url.Values,
) (TurnstileResult, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TurnstileResult{}, false, fmt.Errorf("create Turnstile verification request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := v.client.Do(request)
	if err != nil {
		return TurnstileResult{}, true, fmt.Errorf("verify Turnstile token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return TurnstileResult{}, retryable, fmt.Errorf("Turnstile Siteverify returned HTTP %d", response.StatusCode)
	}
	var decoded siteverifyResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	if err := decoder.Decode(&decoded); err != nil {
		return TurnstileResult{}, false, fmt.Errorf("decode Turnstile Siteverify response: %w", err)
	}
	return TurnstileResult{
		Success:    decoded.Success,
		ErrorCodes: append([]string(nil), decoded.ErrorCodes...),
		Hostname:   strings.TrimSpace(decoded.Hostname),
		Action:     strings.TrimSpace(decoded.Action),
	}, false, nil
}

func (v *CloudflareTurnstileVerifier) validateResult(result TurnstileResult) TurnstileResult {
	if result.Success && !strings.EqualFold(result.Hostname, v.expectedHostname) {
		result.Success = false
		result.ErrorCodes = append(result.ErrorCodes, "hostname-mismatch")
	}
	if result.Success && result.Action != v.expectedAction {
		result.Success = false
		result.ErrorCodes = append(result.ErrorCodes, "action-mismatch")
	}
	return result
}
