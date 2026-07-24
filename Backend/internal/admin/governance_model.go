package admin

import "time"

type GovernedAdmin struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	MFAEnabled  bool       `json:"mfa_enabled"`
	Roles       []string   `json:"roles"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
}

type PermissionDefinition struct {
	Key         string `json:"key"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Description string `json:"description"`
	RiskLevel   string `json:"risk_level"`
}

type GovernedRole struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	SystemRole  bool      `json:"system_role"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateGovernedAdminInput struct {
	Username    string
	DisplayName string
	Password    string
	Roles       []string
	Reason      string
}

type CreateGovernedAdminResult struct {
	Admin           GovernedAdmin
	ProvisioningURI string
	RecoveryCodes   []string
}

type UpdateGovernedAdminInput struct {
	DisplayName    *string
	Status         *string
	Roles          []string
	RolesSet       bool
	RevokeSessions bool
	Reason         string
}

type ResetGovernedAdminMFAResult struct {
	Admin           GovernedAdmin
	ProvisioningURI string
	RecoveryCodes   []string
}
