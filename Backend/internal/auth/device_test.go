package auth

import (
	"bytes"
	"strings"
	"testing"
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
