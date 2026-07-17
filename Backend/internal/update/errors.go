package update

import "net/http"

type serviceError struct {
	status  int
	code    string
	message string
	details map[string]any
	cause   error
}

func (e *serviceError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}

func invalid(message string, details map[string]any) error {
	return &serviceError{status: http.StatusBadRequest, code: "INVALID_UPDATE_REQUEST", message: message, details: details}
}

func notFound(message string) error {
	return &serviceError{status: http.StatusNotFound, code: "UPDATE_NOT_FOUND", message: message}
}

func internal(err error) error {
	return &serviceError{status: http.StatusInternalServerError, code: "INTERNAL_ERROR", message: "Internal server error.", cause: err}
}

func errorDetails(err error) (int, string, string, map[string]any) {
	if typed, ok := err.(*serviceError); ok {
		return typed.status, typed.code, typed.message, typed.details
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil
}
