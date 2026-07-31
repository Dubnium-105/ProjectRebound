package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maximumTicketHexCharacters = 4096
	symmetricKeyBytes          = 32
)

type nativeTicketLibrary interface {
	Decrypt(encryptedTicket []byte, key []byte) ([]byte, error)
	SteamID(decryptedTicket []byte) (uint64, error)
	AppID(decryptedTicket []byte) (uint32, error)
	IssueTime(decryptedTicket []byte) (uint32, error)
	VACBanned(decryptedTicket []byte) (bool, error)
	Close() error
}

type verifierOutput struct {
	Valid     bool   `json:"valid"`
	SteamID   string `json:"steam_id"`
	AppID     uint32 `json:"app_id"`
	IssueTime int64  `json:"issue_time"`
	VACBanned bool   `json:"vac_banned"`
}

type invalidTicketError struct {
	cause error
}

func (e *invalidTicketError) Error() string {
	return e.cause.Error()
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr, os.LookupEnv, loadNativeTicketLibrary))
}

func run(
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
	loadLibrary func(string) (nativeTicketLibrary, error),
) int {
	encryptedTicket, err := readTicket(stdin)
	if err != nil {
		writeVerifierError(stderr, err)
		return 1
	}
	defer clear(encryptedTicket)

	key, err := loadSymmetricKey(lookupEnv)
	if err != nil {
		writeVerifierError(stderr, err)
		return 2
	}
	defer clear(key)

	libraryPath := configuredLibraryPath(lookupEnv)
	library, err := loadLibrary(libraryPath)
	if err != nil {
		writeVerifierError(stderr, fmt.Errorf("load Steam ticket library: %w", err))
		return 2
	}
	defer func() {
		if err := library.Close(); err != nil {
			writeVerifierError(stderr, fmt.Errorf("close Steam ticket library: %w", err))
		}
	}()

	decryptedTicket, err := library.Decrypt(encryptedTicket, key)
	if err != nil {
		writeVerifierError(stderr, &invalidTicketError{cause: errors.New("ticket decryption failed")})
		return 1
	}
	defer clear(decryptedTicket)

	steamID, err := library.SteamID(decryptedTicket)
	if err != nil || steamID == 0 {
		writeVerifierError(stderr, &invalidTicketError{cause: errors.New("ticket contains an invalid SteamID")})
		return 1
	}
	// AppID, issue time, and VAC state are best-effort audit metadata. Ticket
	// acceptance depends only on successful decryption and the SteamID above.
	appID, _ := library.AppID(decryptedTicket)
	issueTime, _ := library.IssueTime(decryptedTicket)
	vacBanned, _ := library.VACBanned(decryptedTicket)

	if err := json.NewEncoder(stdout).Encode(verifierOutput{
		Valid: true, SteamID: strconv.FormatUint(steamID, 10),
		AppID: appID, IssueTime: int64(issueTime), VACBanned: vacBanned,
	}); err != nil {
		writeVerifierError(stderr, fmt.Errorf("write verifier output: %w", err))
		return 2
	}
	return 0
}

func readTicket(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maximumTicketHexCharacters+2)
	value, err := bufio.NewReader(limited).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, &invalidTicketError{cause: errors.New("read ticket input")}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, &invalidTicketError{cause: errors.New("ticket is empty")}
	}
	if len(value) > maximumTicketHexCharacters {
		return nil, &invalidTicketError{cause: errors.New("ticket exceeds the input limit")}
	}
	if len(value)%2 != 0 {
		return nil, &invalidTicketError{cause: errors.New("ticket must contain an even number of hexadecimal characters")}
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, &invalidTicketError{cause: errors.New("ticket must be hexadecimal")}
	}
	if len(decoded) == 0 {
		return nil, &invalidTicketError{cause: errors.New("ticket is empty")}
	}
	return decoded, nil
}

func loadSymmetricKey(lookupEnv func(string) (string, bool)) ([]byte, error) {
	keyHex, hasHex := lookupEnv("STEAM_ENCRYPTED_APP_TICKET_KEY_HEX")
	keyPath, hasPath := lookupEnv("STEAM_ENCRYPTED_APP_TICKET_KEY_FILE")
	keyHex = strings.TrimSpace(keyHex)
	keyPath = strings.TrimSpace(keyPath)
	hasHex = hasHex && keyHex != ""
	hasPath = hasPath && keyPath != ""
	if hasHex == hasPath {
		return nil, errors.New("configure exactly one of STEAM_ENCRYPTED_APP_TICKET_KEY_HEX or STEAM_ENCRYPTED_APP_TICKET_KEY_FILE")
	}
	if hasHex {
		return decodeSymmetricKey(keyHex)
	}
	value, err := os.ReadFile(filepath.Clean(keyPath))
	if err != nil {
		return nil, fmt.Errorf("read Steam encrypted app ticket key: %w", err)
	}
	if len(value) == symmetricKeyBytes {
		return append([]byte(nil), value...), nil
	}
	return decodeSymmetricKey(strings.TrimSpace(string(value)))
}

func decodeSymmetricKey(value string) ([]byte, error) {
	if len(value) != symmetricKeyBytes*2 {
		return nil, fmt.Errorf("Steam encrypted app ticket key must be %d bytes", symmetricKeyBytes)
	}
	key, err := hex.DecodeString(value)
	if err != nil {
		return nil, errors.New("Steam encrypted app ticket key must be hexadecimal")
	}
	return key, nil
}

func configuredLibraryPath(lookupEnv func(string) (string, bool)) string {
	if value, ok := lookupEnv("STEAM_ENCRYPTED_APP_TICKET_LIBRARY"); ok {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return defaultNativeTicketLibraryPath()
}

func writeVerifierError(writer io.Writer, err error) {
	var invalid *invalidTicketError
	category := "system_error"
	if errors.As(err, &invalid) {
		category = "invalid_ticket"
	}
	var buffer bytes.Buffer
	_ = json.NewEncoder(&buffer).Encode(map[string]string{
		"error": category,
	})
	_, _ = io.Copy(writer, &buffer)
}
