package admin

import "time"

type AdminSetting struct {
	Key         string    `json:"key"`
	Category    string    `json:"category"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	Value       any       `json:"value"`
	ValueType   string    `json:"value_type"`
	Editable    bool      `json:"editable"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AdminCapabilityResource struct {
	Name       string   `json:"name"`
	Operations []string `json:"operations"`
}

type AdminCapabilities struct {
	APIVersion             string                    `json:"api_version"`
	Resources              []AdminCapabilityResource `json:"resources"`
	MaxBatchOperations     int                       `json:"max_batch_operations"`
	RealtimeSubscriptions  bool                      `json:"realtime_subscriptions"`
	DualApproval           bool                      `json:"dual_approval"`
	PollingFallbackSeconds map[string]int            `json:"polling_fallback_seconds"`
}
