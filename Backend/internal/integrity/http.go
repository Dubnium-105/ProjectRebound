package integrity

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
)

type HTTPHandler struct {
	service          *Service
	logger           *slog.Logger
	trustProxyHeader bool
}

func NewHTTPHandler(service *Service, logger *slog.Logger, trustProxyHeader bool) *HTTPHandler {
	return &HTTPHandler{
		service: service, logger: logger, trustProxyHeader: trustProxyHeader,
	}
}

type proofRequest struct {
	Nonce     string `json:"nonce"`
	Proof     string `json:"proof"`
	Component string `json:"component"`
}

func (h *HTTPHandler) Challenge(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, auth.CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	challenge, err := h.service.Challenge(*principal)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, challenge)
}

func (h *HTTPHandler) Proof(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, auth.CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	var request proofRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, auth.CodeInvalidRequest, "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	if strings.TrimSpace(request.Nonce) == "" ||
		strings.TrimSpace(request.Proof) == "" ||
		strings.TrimSpace(request.Component) == "" {
		api.WriteError(w, r, http.StatusBadRequest, auth.CodeInvalidRequest, "nonce, proof, and component are required.", nil)
		return
	}
	result, err := h.service.Verify(
		r.Context(),
		*principal,
		request.Nonce,
		request.Proof,
		request.Component,
		h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"ok": result.OK})
}

// Verify is retained as a compatibility alias for clients built against the
// earlier placeholder route. New clients use POST /v1/integrity/proof.
func (h *HTTPHandler) Verify(w http.ResponseWriter, r *http.Request) {
	h.Proof(w, r)
}

func (h *HTTPHandler) requestMeta(r *http.Request) auth.RequestMeta {
	return auth.RequestMeta{
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxyHeader),
		UserAgent: r.UserAgent(),
		DeviceID:  r.Header.Get("X-Device-Id"),
	}
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := auth.ErrorDetails(err)
	if status >= http.StatusInternalServerError && h.logger != nil {
		h.logger.ErrorContext(r.Context(), "integrity request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
