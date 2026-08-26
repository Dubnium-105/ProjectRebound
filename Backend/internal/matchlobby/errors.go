package matchlobby

import (
	"errors"
	"net/http"
)

type serviceError struct {
	status  int
	code    string
	message string
	details map[string]any
	cause   error
}

func (e *serviceError) Error() string {
	if e.cause != nil {
		return e.message + ": " + e.cause.Error()
	}
	return e.message
}

func (e *serviceError) Unwrap() error { return e.cause }

func invalid(message string, details map[string]any) error {
	return &serviceError{status: http.StatusBadRequest, code: "INVALID_REQUEST", message: message, details: details}
}
func unauthorized(code, message string) error {
	return &serviceError{status: http.StatusUnauthorized, code: code, message: message}
}
func forbidden(code, message string) error {
	return &serviceError{status: http.StatusForbidden, code: code, message: message}
}
func notFound(code, message string) error {
	return &serviceError{status: http.StatusNotFound, code: code, message: message}
}
func conflict(code, message string, details map[string]any) error {
	return &serviceError{status: http.StatusConflict, code: code, message: message, details: details}
}
func internal(err error) error {
	return &serviceError{status: http.StatusInternalServerError, code: "INTERNAL_ERROR", message: "Internal server error.", cause: err}
}

func errorDetails(err error) (int, string, string, map[string]any) {
	var target *serviceError
	if errors.As(err, &target) {
		return target.status, target.code, target.message, target.details
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}
