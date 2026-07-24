package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/api"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

type GovernanceHTTPService interface {
	ListAdmins(context.Context) ([]GovernedAdmin, error)
	ListRoles(context.Context) ([]GovernedRole, []PermissionDefinition, error)
	CreateAdmin(context.Context, CreateGovernedAdminInput, RequestMeta) (CreateGovernedAdminResult, error)
	UpdateAdmin(context.Context, string, UpdateGovernedAdminInput, RequestMeta) (GovernedAdmin, error)
	ResetMFA(context.Context, string, string, RequestMeta) (ResetGovernedAdminMFAResult, error)
	UpdateRole(context.Context, string, []string, string, RequestMeta) (GovernedRole, error)
}

type GovernanceHTTPHandler struct {
	service    GovernanceHTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewGovernanceHTTPHandler(
	service GovernanceHTTPService,
	logger *slog.Logger,
	trustProxy bool,
) *GovernanceHTTPHandler {
	return &GovernanceHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type createAdminRequest struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`
	Reason      string   `json:"reason"`
}

type updateAdminRequest struct {
	DisplayName    *string  `json:"display_name"`
	Status         *string  `json:"status"`
	Roles          []string `json:"roles"`
	RevokeSessions bool     `json:"revoke_sessions"`
	Reason         string   `json:"reason"`
}

type updateRoleRequest struct {
	Permissions []string `json:"permissions"`
	Reason      string   `json:"reason"`
}

func (h *GovernanceHTTPHandler) ListAdmins(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListAdmins(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *GovernanceHTTPHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, permissions, err := h.service.ListRoles(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": roles, "permissions": permissions})
}

func (h *GovernanceHTTPHandler) CreateAdmin(w http.ResponseWriter, r *http.Request) {
	var request createAdminRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.CreateAdmin(r.Context(), CreateGovernedAdminInput{
		Username: request.Username, DisplayName: request.DisplayName,
		Password: request.Password, Roles: request.Roles, Reason: request.Reason,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"admin": result.Admin, "totp_provisioning_uri": result.ProvisioningURI,
		"recovery_codes": result.RecoveryCodes,
	})
}

func (h *GovernanceHTTPHandler) UpdateAdmin(w http.ResponseWriter, r *http.Request) {
	var request updateAdminRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	item, err := h.service.UpdateAdmin(
		r.Context(), chi.URLParam(r, "admin_id"),
		UpdateGovernedAdminInput{
			DisplayName: request.DisplayName, Status: request.Status,
			Roles: request.Roles, RolesSet: request.Roles != nil,
			RevokeSessions: request.RevokeSessions, Reason: request.Reason,
		},
		h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, item)
}

func (h *GovernanceHTTPHandler) ResetMFA(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.ResetMFA(
		r.Context(), chi.URLParam(r, "admin_id"), request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"admin": result.Admin, "totp_provisioning_uri": result.ProvisioningURI,
		"recovery_codes": result.RecoveryCodes,
	})
}

func (h *GovernanceHTTPHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	var request updateRoleRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	item, err := h.service.UpdateRole(
		r.Context(), chi.URLParam(r, "role_id"), request.Permissions,
		request.Reason, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, item)
}

func (h *GovernanceHTTPHandler) requestMeta(r *http.Request) RequestMeta {
	principal := PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return RequestMeta{
		AdminID: adminID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy), UserAgent: r.UserAgent(),
	}
}

func (h *GovernanceHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator governance request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
