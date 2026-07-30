// Command test-ticket-verifier is an intentionally insecure fixture for
// isolated integration tests. Production images do not include it.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var steamIDPattern = regexp.MustCompile(`^[0-9]{16,20}$`)

func main() {
	if os.Getenv("ALLOW_INSECURE_TEST_TICKET_VERIFIER") != "1" {
		fmt.Fprintln(os.Stderr, "insecure test ticket verifier is disabled")
		os.Exit(78)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 4098))
	encodedTicket := strings.TrimSpace(string(input))
	if err != nil || len(encodedTicket) > 4096 {
		fmt.Fprintln(os.Stderr, "invalid fixture input")
		os.Exit(2)
	}
	decoded, err := hex.DecodeString(encodedTicket)
	steamID, nonce, hasNonce := strings.Cut(string(decoded), "|")
	if err != nil || !hasNonce || !steamIDPattern.MatchString(steamID) ||
		strings.TrimSpace(nonce) == "" {
		fmt.Fprintln(os.Stderr, "fixture ticket does not encode a SteamID")
		os.Exit(3)
	}
	appID := uint64(480)
	if configured := strings.TrimSpace(os.Getenv("STEAM_APP_ID")); configured != "" {
		appID, err = strconv.ParseUint(configured, 10, 32)
		if err != nil || appID == 0 {
			fmt.Fprintln(os.Stderr, "invalid STEAM_APP_ID")
			os.Exit(78)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"valid":      true,
		"steam_id":   steamID,
		"app_id":     appID,
		"issue_time": time.Now().UTC().Unix(),
		"vac_banned": false,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "encode fixture response")
		os.Exit(4)
	}
}
