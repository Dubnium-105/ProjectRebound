package vnt

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
	var target *ServiceError
	if errors.As(err, &target) {
		return target.Status, target.Code, target.Message, target.Details
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}

func serviceError(status int, code, message string) error {
	return &ServiceError{Status: status, Code: code, Message: message}
}
func internal(err error) error {
	return &ServiceError{Status: 500, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}
