package relayregistry

import (
	"errors"
	"net/http"
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
		return e.Code + ": " + e.Cause.Error()
	}
	return e.Code
}

func (e *ServiceError) Unwrap() error { return e.Cause }

func errorDetails(err error) (int, string, string, map[string]any) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Status, serviceError.Code, serviceError.Message, serviceError.Details
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}

func invalid(message string, details map[string]any) error {
	return &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: message, Details: details}
}

func unauthorized(code, message string) error {
	return &ServiceError{Status: 401, Code: code, Message: message}
}

func conflict(code, message string) error {
	return &ServiceError{Status: 409, Code: code, Message: message}
}

func notFound() error {
	return &ServiceError{Status: 404, Code: "RELAY_NODE_NOT_FOUND", Message: "Relay node not found."}
}

func unavailable(code, message string) error {
	return &ServiceError{Status: 503, Code: code, Message: message}
}

func internal(err error) error {
	return &ServiceError{Status: 500, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}
