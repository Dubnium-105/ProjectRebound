package auth

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/projectrebound/matchserver/internal/config"
)

func TestNormalizeDeviceID(t *testing.T) {
	value, err := NormalizeDeviceID("  installation-1234  ")
	if err != nil || value != "installation-1234" {
		t.Fatalf("NormalizeDeviceID() = %q, %v", value, err)
	}
	for _, invalid := range []string{"contains\nnewline", "café", strings.Repeat("a", 129)} {
		if _, err := NormalizeDeviceID(invalid); err == nil {
			t.Errorf("NormalizeDeviceID(%q) accepted", invalid)
		}
	}
}

func TestNormalizeStructuredDeviceFingerprint(t *testing.T) {
	value, err := NormalizeDeviceID("CP:F846FFB743ECA479|uu:B09BC26D38BB76D6|ds:A867D4D49D01C90A")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "v1|uu:b09bc26d38bb76d6|ds:a867d4d49d01c90a|cp:f846ffb743eca479"
	if value != expected {
		t.Fatalf("NormalizeDeviceID() = %q", value)
	}
	factors, recognized, err := ParseDeviceFingerprint(value)
	if err != nil || !recognized {
		t.Fatalf("ParseDeviceFingerprint() = %#v, %v, %v", factors, recognized, err)
	}
	if factors.FactorMask != 7 || factors.SMBIOSUUID != "b09bc26d38bb76d6" ||
		factors.DiskSerial != "a867d4d49d01c90a" || factors.CPUID != "f846ffb743eca479" {
		t.Fatalf("factors = %#v", factors)
	}
}

func TestStructuredDeviceFingerprintRejectsMalformedValues(t *testing.T) {
	for _, invalid := range []string{
		"v1|uu:short",
		"uu:b09bc26d38bb76d6|uu:b09bc26d38bb76d6",
		"v1|xx:b09bc26d38bb76d6",
		"v1|ds:zzzzzzzzzzzzzzzz",
	} {
		if _, err := NormalizeDeviceID(invalid); err == nil {
			t.Errorf("NormalizeDeviceID(%q) accepted", invalid)
		}
	}
}

func TestDeviceFingerprinterUsesDomainSeparatedStableDigests(t *testing.T) {
	cfg := config.Defaults.Auth
	cfg.DeviceFingerprintHMACKeyBase64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	fingerprinter, ephemeral, err := NewDeviceFingerprinter(cfg, "production")
	if err != nil || ephemeral {
		t.Fatalf("NewDeviceFingerprinter() = %v, %v", ephemeral, err)
	}
	values, recognized, err := ParseDeviceFingerprint(
		"v1|uu:b09bc26d38bb76d6|ds:a867d4d49d01c90a|cp:f846ffb743eca479",
	)
	if err != nil || !recognized {
		t.Fatal(err)
	}
	first := fingerprinter.Fingerprint(values)
	second := fingerprinter.Fingerprint(values)
	if len(first.CompositeDigest) != 32 || !bytes.Equal(first.CompositeDigest, second.CompositeDigest) {
		t.Fatalf("composite digest = %x / %x", first.CompositeDigest, second.CompositeDigest)
	}
	if bytes.Equal(first.SMBIOSUUIDDigest, first.DiskSerialDigest) ||
		bytes.Equal(first.DiskSerialDigest, first.CPUIDDigest) {
		t.Fatal("factor digests are not domain separated")
	}
}

func TestDeviceIDStoredRepresentation(t *testing.T) {
	first := HashDeviceID("installation-1234")
	second := HashDeviceID("installation-1234")
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatalf("unexpected device hash %x", first)
	}
	if suffix := DeviceIDSuffix("installation-1234"); suffix != "1234" {
		t.Fatalf("DeviceIDSuffix() = %q", suffix)
	}
}
