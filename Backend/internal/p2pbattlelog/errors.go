package p2pbattlelog

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

func unauthorized(code, message string) error {
	return &ServiceError{Status: http.StatusUnauthorized, Code: code, Message: message}
}

func forbidden(code, message string) error {
	return &ServiceError{Status: http.StatusForbidden, Code: code, Message: message}
}

func notFound(code, message string) error {
	return &ServiceError{Status: http.StatusNotFound, Code: code, Message: message}
}

func invalid(code, message string, details map[string]any) error {
	return &ServiceError{Status: http.StatusBadRequest, Code: code, Message: message, Details: details}
}

func unprocessable(code, message string, details map[string]any) error {
	return &ServiceError{Status: http.StatusUnprocessableEntity, Code: code, Message: message, Details: details}
}

func conflict(code, message string) error {
	return &ServiceError{Status: http.StatusConflict, Code: code, Message: message}
}

func internal(err error) error {
	return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}
