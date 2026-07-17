package api

import (
	"encoding/json"
	"net/http"

	"github.com/projectrebound/matchserver/internal/requestctx"
)

type SuccessEnvelope struct {
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type ErrorEnvelope struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

func WriteData(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSON(w, status, SuccessEnvelope{
		Data:      data,
		RequestID: requestctx.RequestID(r.Context()),
	})
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, ErrorEnvelope{
		Error: ErrorBody{
			Code:    code,
			Message: message,
			Details: details,
		},
		RequestID: requestctx.RequestID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
