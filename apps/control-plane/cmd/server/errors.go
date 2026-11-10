package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

var errAuthenticationServiceUnavailable = errors.New("authentication service unavailable")

// dbStatus translates database errors into the small, intentional set of
// client statuses the control plane exposes. Unknown database failures must
// never leak SQL, host names, or query details to callers.
func dbStatus(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0
	}
	switch pgErr.Code {
	case "22P02": // invalid_text_representation (including malformed UUIDs)
		return http.StatusNotFound
	case "23505": // unique_violation
		return http.StatusConflict
	case "23503": // foreign_key_violation
		return http.StatusBadRequest
	default:
		return 0
	}
}

func writeAppError(w http.ResponseWriter, err error) {
	status := statusCodeForError(err)
	message := err.Error()
	if status >= http.StatusInternalServerError {
		log.Printf("control-plane request failed: %v", err)
		message = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": message})
}
