package invite

import (
	"errors"
	"fmt"
)

type invalidCodeError struct{}

func (invalidCodeError) Error() string           { return "invalid invite code" }
func (invalidCodeError) InvalidInviteCode() bool { return true }

var ErrInvalidCode error = invalidCodeError{}

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

func errorDetails(err error) (int, string, string, map[string]any) {
	var serviceError *ServiceError
	if errors.As(err, &serviceError) {
		return serviceError.Status, serviceError.Code, serviceError.Message, serviceError.Details
	}
	return 500, "INTERNAL_ERROR", "Internal server error.", nil
}
