package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestMain(m *testing.M) {
	switch os.Getenv("PROJECTREBOUND_TICKET_HELPER_MODE") {
	case "success":
		if len(os.Args) != 1 {
			fmt.Fprintln(os.Stderr, "ticket was provided as a command-line argument")
			os.Exit(2)
		}
		input, _ := io.ReadAll(os.Stdin)
		if strings.TrimSpace(string(input)) != "012345abcdef" {
			fmt.Fprintln(os.Stderr, "ticket was not provided through stdin")
			os.Exit(2)
		}
		fmt.Print(`{"steam_id":"76561198000000000","app_id":480,"issue_time":1800000000,"vac_banned":false}`)
		os.Exit(0)
	case "failure":
		fmt.Fprint(os.Stderr, "controlled helper failure")
		os.Exit(7)
	case "reported_invalid":
		fmt.Print(`{"valid":false}`)
		os.Exit(0)
	case "timeout":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "oversized":
		fmt.Print(strings.Repeat("x", 1024))
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}

func TestExecTicketVerifierUsesStdinAndParsesBoundedJSON(t *testing.T) {
	t.Setenv("PROJECTREBOUND_TICKET_HELPER_MODE", "success")
	cfg := config.Defaults.Auth
	cfg.TicketVerifierExecutable = os.Args[0]
	cfg.TicketVerifierTimeoutSeconds = 2
	cfg.TicketMaximumOutputBytes = 512
	verifier := NewExecTicketVerifier(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := verifier.Verify(context.Background(), "012345abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.SteamID != "76561198000000000" ||
		result.AppID != 480 || result.IssueTime != 1_800_000_000 {
		t.Fatalf("verification result = %#v", result)
	}
}

func TestExecTicketVerifierHandlesFailureTimeoutAndOutputLimit(t *testing.T) {
	for _, test := range []struct {
		name          string
		mode          string
		timeout       int
		maximumOutput int
	}{
		{name: "nonzero exit", mode: "failure", timeout: 2, maximumOutput: 512},
		{name: "reported invalid", mode: "reported_invalid", timeout: 2, maximumOutput: 512},
		{name: "timeout", mode: "timeout", timeout: 1, maximumOutput: 512},
		{name: "output limit", mode: "oversized", timeout: 2, maximumOutput: 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PROJECTREBOUND_TICKET_HELPER_MODE", test.mode)
			cfg := config.Defaults.Auth
			cfg.TicketVerifierExecutable = os.Args[0]
			cfg.TicketVerifierTimeoutSeconds = test.timeout
			cfg.TicketMaximumOutputBytes = test.maximumOutput
			verifier := NewExecTicketVerifier(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if _, err := verifier.Verify(context.Background(), "00"); err == nil {
				t.Fatal("verifier accepted a failing helper")
			}
		})
	}
}

func TestNormalizeEncryptedTicket(t *testing.T) {
	canonical, firstHash, err := normalizeEncryptedTicket(" AABBcc ", 32)
	if err != nil || canonical != "aabbcc" || len(firstHash) != 32 {
		t.Fatalf("normalizeEncryptedTicket() = %q, %x, %v", canonical, firstHash, err)
	}
	_, secondHash, err := normalizeEncryptedTicket("aabbcc", 32)
	if err != nil || string(firstHash) != string(secondHash) {
		t.Fatal("ticket hash is not canonical and stable")
	}
	for _, value := range []string{"", "0", "zz", strings.Repeat("aa", 17)} {
		if _, _, err := normalizeEncryptedTicket(value, 32); err == nil {
			t.Errorf("normalizeEncryptedTicket(%q) accepted", value)
		}
	}
}

func TestValidateVerifiedTicket(t *testing.T) {
	cfg := config.Defaults.Auth
	now := time.Unix(1_800_000_000, 0).UTC()
	const requestedSteamID = "76561198000000000"
	valid := VerifiedTicket{
		Valid: true, SteamID: requestedSteamID, AppID: cfg.SteamAppID, IssueTime: now.Unix(),
	}
	if err := validateVerifiedTicket(requestedSteamID, valid, cfg, now); err != nil {
		t.Fatalf("valid ticket rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VerifiedTicket)
		code   string
	}{
		{name: "reported invalid", mutate: func(ticket *VerifiedTicket) { ticket.Valid = false }, code: CodeInvalidSteamTicket},
		{name: "bad SteamID", mutate: func(ticket *VerifiedTicket) { ticket.SteamID = "invalid" }, code: CodeInvalidSteamTicket},
		{name: "SteamID mismatch", mutate: func(ticket *VerifiedTicket) { ticket.SteamID = "76561198000000001" }, code: CodeSteamIDMismatch},
		{name: "wrong app", mutate: func(ticket *VerifiedTicket) { ticket.AppID++ }, code: CodeSteamTicketAppID},
		{name: "expired", mutate: func(ticket *VerifiedTicket) { ticket.IssueTime = now.Add(-10 * time.Minute).Unix() }, code: CodeSteamTicketExpired},
		{name: "future", mutate: func(ticket *VerifiedTicket) { ticket.IssueTime = now.Add(2 * time.Minute).Unix() }, code: CodeSteamTicketExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := valid
			test.mutate(&ticket)
			err := validateVerifiedTicket(requestedSteamID, ticket, cfg, now)
			var validationErr *ticketValidationError
			if !errors.As(err, &validationErr) || validationErr.code != test.code {
				t.Fatalf("error = %#v, want code %s", err, test.code)
			}
		})
	}
}

func TestVerifierStderrRedactsTicket(t *testing.T) {
	const ticket = "aabbccdd"
	value := redactVerifierStderr("failure for AABBCCDD and aabbccdd", ticket)
	if strings.Contains(strings.ToLower(value), ticket) ||
		strings.Count(value, "[REDACTED_TICKET]") != 2 {
		t.Fatalf("redacted stderr = %q", value)
	}
}
