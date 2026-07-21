package auth

import "testing"

func TestMaskIPAddress(t *testing.T) {
	for input, expected := range map[string]string{
		"192.0.2.123":    "192.0.2.xxx",
		"2001:db8::1234": "2001:db8:0:0::",
		"not-an-ip":      "",
	} {
		if actual := maskIPAddress(input); actual != expected {
			t.Errorf("maskIPAddress(%q) = %q, want %q", input, actual, expected)
		}
	}
}
