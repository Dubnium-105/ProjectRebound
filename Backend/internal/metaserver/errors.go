package metaserver

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ServiceError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Err     error
}

func (e *ServiceError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

func (e *ServiceError) Unwrap() error { return e.Err }

func notFound(code, message string) error {
	return &ServiceError{Status: http.StatusNotFound, Code: code, Message: message}
}

func forbidden(code, message string) error {
	return &ServiceError{Status: http.StatusForbidden, Code: code, Message: message}
}

func conflict(code, message string) error {
	return &ServiceError{Status: http.StatusConflict, Code: code, Message: message}
}

func invalid(details map[string]any) error {
	return &ServiceError{
		Status: http.StatusBadRequest, Code: "META_INVALID_REQUEST",
		Message: "The MetaServer request is invalid.", Details: details,
	}
}

func unprocessable(code string, details map[string]any) error {
	return &ServiceError{
		Status: http.StatusUnprocessableEntity, Code: code,
		Message: "The BattleLog snapshot could not be accepted.", Details: details,
	}
}

func internalError(err error) error {
	return &ServiceError{
		Status: http.StatusInternalServerError, Code: "META_INTERNAL_ERROR",
		Message: "The MetaServer request could not be completed.", Err: err,
	}
}

func normalizeRepositoryError(err error, code, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(code, message)
	}
	return internalError(err)
}

func safeErrorClass(err error) string {
	if err == nil {
		return "unknown"
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return "postgres_" + postgresError.Code
	}
	return reflect.TypeOf(err).String()
}
