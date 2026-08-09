package httpapi

import (
	"encoding/json"
	"net/http"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

func (s *Server) requireDatabase(w http.ResponseWriter) bool {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The service is temporarily unavailable.")
		return false
	}
	return true
}
