package admin

import "testing"

func TestNormalizeAdminSettingValue(t *testing.T) {
	booleanSetting := AdminSetting{Key: "features.matchmaking", ValueType: "BOOLEAN"}
	value, err := normalizeAdminSettingValue(booleanSetting, true)
	if err != nil || value != true {
		t.Fatalf("boolean setting = %#v, %v", value, err)
	}
	if _, err := normalizeAdminSettingValue(booleanSetting, "true"); err == nil {
		t.Fatal("string was accepted for a boolean setting")
	}

	urlSetting := AdminSetting{Key: "integrations.grafana_url", ValueType: "URL"}
	for _, raw := range []string{
		"http://grafana.internal",
		"https://user:password@grafana.example.com",
		"javascript:alert(1)",
	} {
		if _, err := normalizeAdminSettingValue(urlSetting, raw); err == nil {
			t.Errorf("unsafe integration URL was accepted: %s", raw)
		}
	}
	value, err = normalizeAdminSettingValue(urlSetting, " https://grafana.example.com/d/control ")
	if err != nil || value != "https://grafana.example.com/d/control" {
		t.Fatalf("HTTPS integration URL = %#v, %v", value, err)
	}
}
