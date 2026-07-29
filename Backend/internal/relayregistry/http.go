package relayregistry

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	Enroll(context.Context, string, EnrollInput) (EnrollResult, error)
	RenewCertificate(context.Context, string, string, string) (EnrollResult, error)
	Get(context.Context, string) (Node, error)
	List(context.Context, ListFilter) (ListResult, error)
	Drain(context.Context, string, DrainInput, AdminMeta) (Node, error)
	Resume(context.Context, string, AdminMeta) (Node, error)
	Revoke(context.Context, string, AdminMeta) (Node, error)
	ActivateSigningKey(context.Context, string, AdminMeta) (Keyset, error)
}

type HTTPHandler struct {
	service    HTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger, trustProxy bool) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type enrollRequest struct {
	DisplayName         string     `json:"display_name"`
	Region              string     `json:"region"`
	Zone                string     `json:"zone"`
	Provider            string     `json:"provider"`
	SoftwareVersion     string     `json:"software_version"`
	ProtocolVersion     int        `json:"protocol_version"`
	AdvertisedEndpoints []Endpoint `json:"advertised_endpoints"`
	SupportedProtocols  []string   `json:"supported_protocols"`
	Capacity            struct {
		MaxAllocations int   `json:"max_allocations"`
		MaxEgressBPS   int64 `json:"max_egress_bps"`
	} `json:"capacity"`
	CSRPEM string `json:"csr_pem"`
}

type renewRequest struct {
	CSRPEM string `json:"csr_pem"`
}

type drainRequest struct {
	DeadlineSeconds int    `json:"deadline_seconds"`
	MigrateExisting bool   `json:"migrate_existing"`
	Reason          string `json:"reason"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type nodeResponse struct {
	NodeID                 string     `json:"node_id"`
	DisplayName            string     `json:"display_name"`
	Region                 string     `json:"region"`
	Zone                   string     `json:"zone"`
	Provider               string     `json:"provider"`
	State                  State      `json:"state"`
	LoadState              LoadState  `json:"load_state"`
	SoftwareVersion        string     `json:"software_version"`
	ProtocolVersion        int        `json:"protocol_version"`
	PublicEndpoints        []Endpoint `json:"public_endpoints"`
	SupportedProtocols     []string   `json:"supported_protocols"`
	MaxAllocations         int        `json:"max_allocations"`
	MaxEgressBPS           int64      `json:"max_egress_bps"`
	ActiveAllocations      int        `json:"active_allocations"`
	CurrentEgressBPS       int64      `json:"current_egress_bps"`
	CurrentIngressBPS      int64      `json:"current_ingress_bps"`
	CertificateFingerprint string     `json:"certificate_fingerprint"`
	CertificateExpiresAt   time.Time  `json:"certificate_expires_at"`
	ConfigVersion          int64      `json:"config_version"`
	LastHeartbeatAt        *time.Time `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt         *time.Time `json:"lease_expires_at,omitempty"`
	DrainDeadline          *time.Time `json:"drain_deadline,omitempty"`
	DrainMigrateExisting   bool       `json:"drain_migrate_existing"`
}

func (h *HTTPHandler) Enroll(w http.ResponseWriter, r *http.Request) {
	var request enrollRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Enroll(r.Context(), bearerToken(r), EnrollInput{
		DisplayName: request.DisplayName, Region: request.Region, Zone: request.Zone, Provider: request.Provider,
		SoftwareVersion: request.SoftwareVersion, ProtocolVersion: request.ProtocolVersion,
		PublicEndpoints: request.AdvertisedEndpoints, SupportedProtocols: request.SupportedProtocols,
		MaxAllocations: request.Capacity.MaxAllocations, MaxEgressBPS: request.Capacity.MaxEgressBPS,
		CSRPEM: request.CSRPEM,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"node": resultNode(result.Node), "node_token": result.NodeToken,
		"certificate_pem": result.CertificatePEM, "ca_certificate_pem": result.CACertificatePEM,
		"certificate_expires_at": result.CertificateExpiresAt, "relay_token_keyset": result.Keyset,
	})
}

func (h *HTTPHandler) RenewCertificate(w http.ResponseWriter, r *http.Request) {
	var request renewRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.RenewCertificate(r.Context(), chi.URLParam(r, "node_id"), bearerToken(r), request.CSRPEM)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"node": resultNode(result.Node), "certificate_pem": result.CertificatePEM,
		"ca_certificate_pem":     result.CACertificatePEM,
		"certificate_expires_at": result.CertificateExpiresAt, "relay_token_keyset": result.Keyset,
	})
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	node, err := h.service.Get(r.Context(), chi.URLParam(r, "node_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, resultNode(node))
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultQuery(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.List(r.Context(), ListFilter{
		Region: r.URL.Query().Get("region"), Zone: r.URL.Query().Get("zone"),
		Provider: r.URL.Query().Get("provider"), State: State(r.URL.Query().Get("state")),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]nodeResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, resultNode(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) Drain(w http.ResponseWriter, r *http.Request) {
	var request drainRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	node, err := h.service.Drain(r.Context(), chi.URLParam(r, "node_id"), DrainInput{
		DeadlineSeconds: request.DeadlineSeconds, MigrateExisting: request.MigrateExisting,
	}, h.adminMeta(r, request.Reason))
	h.writeNodeOperation(w, r, node, err)
}

func (h *HTTPHandler) Resume(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	node, err := h.service.Resume(r.Context(), chi.URLParam(r, "node_id"), h.adminMeta(r, request.Reason))
	h.writeNodeOperation(w, r, node, err)
}

func (h *HTTPHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	node, err := h.service.Revoke(r.Context(), chi.URLParam(r, "node_id"), h.adminMeta(r, request.Reason))
	h.writeNodeOperation(w, r, node, err)
}

func (h *HTTPHandler) ActivateSigningKey(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	keyset, err := h.service.ActivateSigningKey(r.Context(), chi.URLParam(r, "key_id"), h.adminMeta(r, request.Reason))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, keyset)
}

func (h *HTTPHandler) writeNodeOperation(w http.ResponseWriter, r *http.Request, node Node, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, resultNode(node))
}

func (h *HTTPHandler) adminMeta(r *http.Request, reason string) AdminMeta {
	principal := admin.PrincipalFromContext(r.Context())
	actorID := ""
	if principal != nil {
		actorID = principal.AdminID
	}
	return AdminMeta{
		ActorID: actorID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy),
		UserAgent: r.UserAgent(), Reason: reason,
	}
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "relay registry request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func defaultQuery(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func resultNode(node Node) nodeResponse {
	return nodeResponse{
		NodeID: node.ID, DisplayName: node.DisplayName, Region: node.Region, Zone: node.Zone, Provider: node.Provider,
		State: node.State, LoadState: node.LoadState, SoftwareVersion: node.SoftwareVersion, ProtocolVersion: node.ProtocolVersion,
		PublicEndpoints: node.PublicEndpoints, SupportedProtocols: node.SupportedProtocols,
		MaxAllocations: node.MaxAllocations, MaxEgressBPS: node.MaxEgressBPS,
		ActiveAllocations: node.ActiveAllocations, CurrentEgressBPS: node.CurrentEgressBPS,
		CurrentIngressBPS: node.CurrentIngressBPS, CertificateFingerprint: node.CertificateFingerprint,
		CertificateExpiresAt: node.CertificateExpiresAt, ConfigVersion: node.ConfigVersion,
		LastHeartbeatAt: node.LastHeartbeatAt, LeaseExpiresAt: node.LeaseExpiresAt, DrainDeadline: node.DrainDeadline,
		DrainMigrateExisting: node.DrainMigrateExisting,
	}
}
