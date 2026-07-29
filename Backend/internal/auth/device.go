package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

const maximumDeviceIDLength = 128

const (
	deviceFingerprintFormatVersion int16 = 1
	deviceFactorSMBIOSMask         int16 = 1
	deviceFactorDiskMask           int16 = 2
	deviceFactorCPUMask            int16 = 4
)

type DeviceFactorValues struct {
	CanonicalID string
	SMBIOSUUID  string
	DiskSerial  string
	CPUID       string
	FactorMask  int16
}

type DeviceFingerprinter struct {
	keyID string
	key   []byte
}

func NewDeviceFingerprinter(cfg config.AuthConfig, environment string) (*DeviceFingerprinter, bool, error) {
	keyID := strings.TrimSpace(cfg.DeviceFingerprintKeyID)
	if keyID == "" {
		return nil, false, fmt.Errorf("device fingerprint key ID is required")
	}
	encoded := strings.TrimSpace(cfg.DeviceFingerprintHMACKeyBase64)
	ephemeral := false
	var key []byte
	if encoded == "" {
		if strings.EqualFold(strings.TrimSpace(environment), "production") {
			return nil, false, fmt.Errorf("DEVICE_FINGERPRINT_HMAC_KEY_BASE64 is required in production")
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, false, fmt.Errorf("generate development device fingerprint key: %w", err)
		}
		ephemeral = true
	} else {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false, fmt.Errorf("decode device fingerprint HMAC key: %w", err)
		}
		if len(decoded) < 32 {
			return nil, false, fmt.Errorf("device fingerprint HMAC key must contain at least 32 bytes")
		}
		key = decoded
	}
	return &DeviceFingerprinter{keyID: keyID, key: key}, ephemeral, nil
}

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
	if factors, recognized, err := ParseDeviceFingerprint(value); recognized {
		if err != nil {
			return "", err
		}
		return factors.CanonicalID, nil
	}
	return value, nil
}

func ParseDeviceFingerprint(value string) (DeviceFactorValues, bool, error) {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	recognized := strings.HasPrefix(lower, "v1|") ||
		strings.HasPrefix(lower, "uu:") ||
		strings.HasPrefix(lower, "ds:") ||
		strings.HasPrefix(lower, "cp:")
	if !recognized {
		return DeviceFactorValues{}, false, nil
	}
	parts := strings.Split(lower, "|")
	if parts[0] == "v1" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint must contain at least one factor")
	}

	var result DeviceFactorValues
	for _, part := range parts {
		name, encoded, ok := strings.Cut(part, ":")
		if !ok || len(encoded) != 16 {
			return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint factors must contain exactly 16 hexadecimal characters")
		}
		if _, err := hex.DecodeString(encoded); err != nil {
			return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint factors must be hexadecimal")
		}
		switch name {
		case "uu":
			if result.SMBIOSUUID != "" {
				return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint contains duplicate uu factor")
			}
			result.SMBIOSUUID = encoded
			result.FactorMask |= deviceFactorSMBIOSMask
		case "ds":
			if result.DiskSerial != "" {
				return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint contains duplicate ds factor")
			}
			result.DiskSerial = encoded
			result.FactorMask |= deviceFactorDiskMask
		case "cp":
			if result.CPUID != "" {
				return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint contains duplicate cp factor")
			}
			result.CPUID = encoded
			result.FactorMask |= deviceFactorCPUMask
		default:
			return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint contains unknown factor %q", name)
		}
	}
	if result.FactorMask == 0 {
		return DeviceFactorValues{}, true, fmt.Errorf("device fingerprint must contain at least one factor")
	}
	canonical := []string{"v1"}
	if result.SMBIOSUUID != "" {
		canonical = append(canonical, "uu:"+result.SMBIOSUUID)
	}
	if result.DiskSerial != "" {
		canonical = append(canonical, "ds:"+result.DiskSerial)
	}
	if result.CPUID != "" {
		canonical = append(canonical, "cp:"+result.CPUID)
	}
	result.CanonicalID = strings.Join(canonical, "|")
	return result, true, nil
}

func (f *DeviceFingerprinter) Fingerprint(values DeviceFactorValues) DeviceFingerprint {
	return DeviceFingerprint{
		FormatVersion:    deviceFingerprintFormatVersion,
		DigestKeyID:      f.keyID,
		CompositeDigest:  f.digest("composite", values.CanonicalID),
		SMBIOSUUIDDigest: f.optionalDigest("smbios_uuid", values.SMBIOSUUID),
		DiskSerialDigest: f.optionalDigest("disk_serial", values.DiskSerial),
		CPUIDDigest:      f.optionalDigest("cpu_id", values.CPUID),
		FactorMask:       values.FactorMask,
	}
}

func (f *DeviceFingerprinter) optionalDigest(domain, value string) []byte {
	if value == "" {
		return nil
	}
	return f.digest(domain, value)
}

func (f *DeviceFingerprinter) digest(domain, value string) []byte {
	mac := hmac.New(sha256.New, f.key)
	_, _ = mac.Write([]byte("projectrebound/device-fingerprint/v1/" + domain + "\x00" + value))
	return mac.Sum(nil)
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
