package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"
)

const fallbackBodyLimit = 1 << 20

func DecodeJSON(r *http.Request, destination any) error {
	contents, err := io.ReadAll(io.LimitReader(r.Body, fallbackBodyLimit+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(contents) > fallbackBodyLimit {
		return errors.New("request body is too large")
	}
	if !utf8.Valid(contents) {
		return errors.New("request body must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}
