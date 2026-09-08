package connection

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

// ErrorDetails lets the room service preserve a typed connection conflict
// when a managed-room join creates a connection through its internal
// ConnectionCreator interface.
func (e *ServiceError) ErrorDetails() (int, string, string, map[string]any) {
	return e.Status, e.Code, e.Message, e.Details
}

// errManagedConnectionScope is deliberately internal.  The HTTP/API layer
// exposes the stable CONNECTION_ATTEMPT_SCOPE_REQUIRED code, while repository
// callers cannot accidentally manufacture a valid managed scope.
var errManagedConnectionScope = errors.New("managed connection scope is no longer current")

func errorDetails(err error) (int, string, string, map[string]any) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Status, serviceError.Code, serviceError.Message, serviceError.Details
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}

func invalid(message string, details map[string]any) error {
	return &ServiceError{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: message, Details: details}
}

func forbidden(code, message string) error {
	return &ServiceError{Status: http.StatusForbidden, Code: code, Message: message}
}

func conflict(code, message string) error {
	return &ServiceError{Status: http.StatusConflict, Code: code, Message: message}
}

func notFound() error {
	return &ServiceError{Status: http.StatusNotFound, Code: "CONNECTION_NOT_FOUND", Message: "Connection not found."}
}

func internal(err error) error {
	return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}
