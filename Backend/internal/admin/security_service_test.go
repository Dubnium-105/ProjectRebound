package admin

import (
	"context"
	"testing"
)

func TestMaskIPAddress(t *testing.T) {
	for input, expected := range map[string]string{
		"192.0.2.44":             "192.0.2.x",
		"2001:db8:abcd:12::1234": "2001:db8:abcd:12::/64",
		"not-an-address":         "",
	} {
		if actual := maskIPAddress(input); actual != expected {
			t.Errorf("maskIPAddress(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestRedactSensitiveMapRecurses(t *testing.T) {
	result := redactSensitiveMap(map[string]any{
		"token": "do-not-return",
		"safe":  "visible",
		"nested": map[string]any{
			"private_key": "do-not-return",
			"count":       float64(2),
		},
	})
	if result["token"] != "[REDACTED]" || result["safe"] != "visible" {
		t.Fatalf("top-level redaction = %#v", result)
	}
	nested := result["nested"].(map[string]any)
	if nested["private_key"] != "[REDACTED]" || nested["count"] != float64(2) {
		t.Fatalf("nested redaction = %#v", nested)
	}
}

func TestDashboardTimeseriesRejectsArbitraryPeriod(t *testing.T) {
	service := &SecurityService{}
	_, err := service.Timeseries(context.Background(), "1h; DROP TABLE players")
	if err == nil {
		t.Fatal("arbitrary dashboard period was accepted")
	}
	status, code, _, _ := errorDetails(err)
	if status != 400 || code != "INVALID_REQUEST" {
		t.Fatalf("invalid period error = %d %s: %v", status, code, err)
	}
}
