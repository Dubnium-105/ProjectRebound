package admin

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

type safeHTTPError interface {
	StatusCode() int
	ErrorCode() string
	ErrorMessage() string
	ErrorDetails() map[string]any
}

func errorDetails(err error) (int, string, string, map[string]any) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Status, serviceError.Code, serviceError.Message, serviceError.Details
	}
	var safeError safeHTTPError
	if errors.As(err, &safeError) {
		return safeError.StatusCode(), safeError.ErrorCode(), safeError.ErrorMessage(), safeError.ErrorDetails()
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}
