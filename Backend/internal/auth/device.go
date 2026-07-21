package auth

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const maximumDeviceIDLength = 128

// NormalizeDeviceID validates an optional, client-generated installation ID.
// It is only a risk signal and must never be treated as proof of identity.
func NormalizeDeviceID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > maximumDeviceIDLength {
		return "", fmt.Errorf("must not exceed %d bytes", maximumDeviceIDLength)
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return "", fmt.Errorf("must contain only printable ASCII characters")
		}
	}
	return value, nil
}

func HashDeviceID(value string) []byte {
	if value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func DeviceIDSuffix(value string) string {
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}
