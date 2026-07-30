package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type VerifiedTicket struct {
	Valid     bool   `json:"valid"`
	SteamID   string `json:"steam_id"`
	AppID     uint32 `json:"app_id"`
	IssueTime int64  `json:"issue_time"`
	VACBanned bool   `json:"vac_banned"`
}

type TicketVerifier interface {
	Verify(context.Context, string) (VerifiedTicket, error)
}

type ticketValidationError struct {
	code   string
	reason string
	cause  error
}

func (e *ticketValidationError) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

type ExecTicketVerifier struct {
	executable    string
	timeout       time.Duration
	maximumOutput int
	logger        *slog.Logger
}

func NewExecTicketVerifier(cfg config.AuthConfig, logger *slog.Logger) *ExecTicketVerifier {
	return &ExecTicketVerifier{
		executable:    strings.TrimSpace(cfg.TicketVerifierExecutable),
		timeout:       time.Duration(cfg.TicketVerifierTimeoutSeconds) * time.Second,
		maximumOutput: cfg.TicketMaximumOutputBytes,
		logger:        logger,
	}
}

func (v *ExecTicketVerifier) Verify(ctx context.Context, encryptedTicketHex string) (VerifiedTicket, error) {
	if v == nil || v.executable == "" {
		return VerifiedTicket{}, errors.New("ticket verifier executable is not configured")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	command := exec.CommandContext(timeoutCtx, v.executable)
	command.WaitDelay = 500 * time.Millisecond
	command.Stdin = strings.NewReader(encryptedTicketHex + "\n")
	stdout := &limitedBuffer{maximum: v.maximumOutput}
	stderr := &limitedBuffer{maximum: min(v.maximumOutput, 4096)}
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		if v.logger != nil {
			v.logger.WarnContext(ctx, "Steam ticket verifier failed",
				"error", err,
				"timed_out", errors.Is(timeoutCtx.Err(), context.DeadlineExceeded),
				"stderr", redactVerifierStderr(stderr.String(), encryptedTicketHex),
			)
		}
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return VerifiedTicket{}, fmt.Errorf("ticket verifier timed out: %w", timeoutCtx.Err())
		}
		if stdout.exceeded || stderr.exceeded {
			return VerifiedTicket{}, errors.New("ticket verifier output exceeded the configured limit")
		}
		return VerifiedTicket{}, fmt.Errorf("ticket verifier exited unsuccessfully: %w", err)
	}
	if stdout.exceeded {
		return VerifiedTicket{}, errors.New("ticket verifier stdout exceeded the configured limit")
	}

	var output struct {
		Valid     *bool  `json:"valid"`
		SteamID   string `json:"steam_id"`
		AppID     uint32 `json:"app_id"`
		IssueTime int64  `json:"issue_time"`
		VACBanned bool   `json:"vac_banned"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&output); err != nil {
		return VerifiedTicket{}, fmt.Errorf("decode ticket verifier output: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return VerifiedTicket{}, errors.New("ticket verifier output contains trailing data")
	}
	if output.Valid != nil && !*output.Valid {
		return VerifiedTicket{}, errors.New("ticket verifier reported an invalid ticket")
	}
	return VerifiedTicket{
		Valid: true, SteamID: output.SteamID, AppID: output.AppID,
		IssueTime: output.IssueTime, VACBanned: output.VACBanned,
	}, nil
}

func validateVerifiedTicket(
	requestedSteamID string,
	ticket VerifiedTicket,
	cfg config.AuthConfig,
	now time.Time,
) error {
	if !ticket.Valid {
		return &ticketValidationError{code: CodeInvalidSteamTicket, reason: "ticket_reported_invalid"}
	}
	ticket.SteamID = strings.TrimSpace(ticket.SteamID)
	if err := ValidateSteamID(ticket.SteamID); err != nil {
		return &ticketValidationError{code: CodeInvalidSteamTicket, reason: "invalid_ticket_steam_id", cause: err}
	}
	if ticket.SteamID != requestedSteamID {
		return &ticketValidationError{code: CodeSteamIDMismatch, reason: "steam_id_mismatch"}
	}
	if ticket.AppID != cfg.SteamAppID {
		return &ticketValidationError{code: CodeSteamTicketAppID, reason: "app_id_mismatch"}
	}
	issueTime := time.Unix(ticket.IssueTime, 0).UTC()
	if ticket.IssueTime < 1 ||
		issueTime.After(now.Add(time.Duration(cfg.TicketClockSkewSeconds)*time.Second)) ||
		now.Sub(issueTime) > time.Duration(cfg.TicketMaximumAgeSeconds)*time.Second {
		return &ticketValidationError{code: CodeSteamTicketExpired, reason: "expired_or_not_yet_valid"}
	}
	return nil
}

func redactVerifierStderr(stderr, encryptedTicketHex string) string {
	value := strings.TrimSpace(stderr)
	if value == "" {
		return ""
	}
	for _, ticket := range []string{
		encryptedTicketHex,
		strings.ToLower(encryptedTicketHex),
		strings.ToUpper(encryptedTicketHex),
	} {
		if ticket != "" {
			value = strings.ReplaceAll(value, ticket, "[REDACTED_TICKET]")
		}
	}
	return value
}

func normalizeEncryptedTicket(value string, maximumHexBytes int) (string, []byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, errors.New("ticket is empty")
	}
	if len(value) > maximumHexBytes {
		return "", nil, fmt.Errorf("ticket exceeds %d hexadecimal characters", maximumHexBytes)
	}
	if len(value)%2 != 0 {
		return "", nil, errors.New("ticket must contain an even number of hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return "", nil, errors.New("ticket must be hexadecimal")
	}
	if len(decoded) == 0 {
		return "", nil, errors.New("ticket is empty")
	}
	sum := sha256.Sum256(decoded)
	return strings.ToLower(value), sum[:], nil
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := b.maximum - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	if len(value) > remaining {
		_, _ = b.buffer.Write(value[:remaining])
		b.exceeded = true
		return remaining, errors.New("output limit exceeded")
	}
	return b.buffer.Write(value)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) String() string { return b.buffer.String() }
