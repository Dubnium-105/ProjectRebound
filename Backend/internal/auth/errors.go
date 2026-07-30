package auth

import (
	"errors"
	"fmt"
	"net/http"
)

const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeBindRateLimited    = "AUTH_BIND_RATE_LIMITED"
	CodeInvalidInvite      = "INVALID_INVITE_CODE"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeSessionRevoked     = "SESSION_REVOKED"
	CodeSessionNotFound    = "SESSION_NOT_FOUND"
	CodeRefreshTokenReused = "REFRESH_TOKEN_REUSED"
	CodeAccountDeleted     = "ACCOUNT_DELETED"
	CodeInvalidSteamTicket = "STEAM_TICKET_INVALID"
	CodeSteamIDMismatch    = "STEAM_ID_MISMATCH"
	CodeSteamTicketExpired = "STEAM_TICKET_EXPIRED"
	CodeSteamTicketAppID   = "STEAM_TICKET_APP_ID_MISMATCH"
	CodeSteamTicketReplay  = "STEAM_TICKET_REPLAY"
	CodeDeviceRestricted   = "DEVICE_RESTRICTED"
	CodeVerifiedRequired   = "STEAM_VERIFICATION_REQUIRED"
	CodeIntegrityFailed    = "INTEGRITY_PROOF_INVALID"
	CodeInternalError      = "INTERNAL_ERROR"
)

type ServiceError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func (e *ServiceError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	}
	return e.Code
}

func (e *ServiceError) Unwrap() error { return e.Cause }

func ErrorDetails(err error) (int, string, string, map[string]any) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Status, serviceError.Code, serviceError.Message, serviceError.Details
	}
	return http.StatusInternalServerError, CodeInternalError, "Internal server error.", nil
}

func ErrorCode(err error) string {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Code
	}
	return CodeInternalError
}

func invalidRequest(message string, details map[string]any) error {
	return &ServiceError{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: message, Details: details}
}

func unauthorized(message string, cause error) error {
	return &ServiceError{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: message, Cause: cause}
}

func internalError(cause error) error {
	return &ServiceError{Status: http.StatusInternalServerError, Code: CodeInternalError, Message: "Internal server error.", Cause: cause}
}
