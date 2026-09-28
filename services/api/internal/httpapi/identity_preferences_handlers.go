package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type notificationPreferences struct {
	CompetitionPush  bool      `json:"competitionPush"`
	MatchPush        bool      `json:"matchPush"`
	ResultPush       bool      `json:"resultPush"`
	CompetitionEmail bool      `json:"competitionEmail"`
	MatchEmail       bool      `json:"matchEmail"`
	MarketingEmail   bool      `json:"marketingEmail"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type notificationPreferencesPatch struct {
	CompetitionPush  *bool `json:"competitionPush"`
	MatchPush        *bool `json:"matchPush"`
	ResultPush       *bool `json:"resultPush"`
	CompetitionEmail *bool `json:"competitionEmail"`
	MatchEmail       *bool `json:"matchEmail"`
	MarketingEmail   *bool `json:"marketingEmail"`
}

type legalDocumentView struct {
	DocumentType string    `json:"documentType"`
	Version      string    `json:"version"`
	ContentURL   string    `json:"contentUrl"`
	EffectiveAt  time.Time `json:"effectiveAt"`
}

type legalAcceptanceView struct {
	DocumentType string    `json:"documentType"`
	Version      string    `json:"version"`
	AcceptedAt   time.Time `json:"acceptedAt"`
}

type legalAcceptanceInput struct {
	Acceptances []struct {
		DocumentType string `json:"documentType"`
		Version      string `json:"version"`
	} `json:"acceptances"`
}

func (s *Server) getNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	preferences, err := loadNotificationPreferences(r.Context(), s.db.Writer, identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load notification preferences.")
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func (s *Server) patchNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input notificationPreferencesPatch
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.CompetitionPush == nil && input.MatchPush == nil && input.ResultPush == nil &&
		input.CompetitionEmail == nil && input.MatchEmail == nil && input.MarketingEmail == nil {
		writeError(w, http.StatusBadRequest, "empty_update", "Provide at least one preference to update.")
		return
	}
	var result notificationPreferences
	err := s.db.Writer.QueryRow(r.Context(), `INSERT INTO notification_preferences
		(user_id,competition_push,match_push,result_push,competition_email,match_email,marketing_email)
		VALUES ($1,COALESCE($2::boolean,true),COALESCE($3::boolean,true),COALESCE($4::boolean,true),
			COALESCE($5::boolean,true),COALESCE($6::boolean,true),COALESCE($7::boolean,false))
		ON CONFLICT (user_id) DO UPDATE SET
			competition_push=COALESCE($2::boolean,notification_preferences.competition_push),
			match_push=COALESCE($3::boolean,notification_preferences.match_push),
			result_push=COALESCE($4::boolean,notification_preferences.result_push),
			competition_email=COALESCE($5::boolean,notification_preferences.competition_email),
			match_email=COALESCE($6::boolean,notification_preferences.match_email),
			marketing_email=COALESCE($7::boolean,notification_preferences.marketing_email),updated_at=now()
		RETURNING competition_push,match_push,result_push,competition_email,match_email,marketing_email,updated_at`,
		identityFromContext(r.Context()).UserID, input.CompetitionPush, input.MatchPush, input.ResultPush,
		input.CompetitionEmail, input.MatchEmail, input.MarketingEmail).
		Scan(&result.CompetitionPush, &result.MatchPush, &result.ResultPush, &result.CompetitionEmail,
			&result.MatchEmail, &result.MarketingEmail, &result.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update notification preferences.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func loadNotificationPreferences(ctx context.Context, pool *pgxpool.Pool, userID string) (notificationPreferences, error) {
	var result notificationPreferences
	err := pool.QueryRow(ctx, `SELECT
		COALESCE(preference.competition_push,true),COALESCE(preference.match_push,true),
		COALESCE(preference.result_push,true),COALESCE(preference.competition_email,true),
		COALESCE(preference.match_email,true),COALESCE(preference.marketing_email,false),
		COALESCE(preference.updated_at,player.created_at)
		FROM users player LEFT JOIN notification_preferences preference ON preference.user_id=player.id
		WHERE player.id=$1`, userID).Scan(&result.CompetitionPush, &result.MatchPush, &result.ResultPush,
		&result.CompetitionEmail, &result.MatchEmail, &result.MarketingEmail, &result.UpdatedAt)
	return result, err
}

func (s *Server) getCurrentLegalDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	documents, err := queryCurrentLegalDocuments(r.Context(), s.db.Reader)
	if err != nil {
		s.logger.Warn("reader query failed; falling back to writer", "operation", "current_legal_documents", "error", err)
		documents, err = queryCurrentLegalDocuments(r.Context(), s.db.Writer)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load legal documents.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": documents})
}

func queryCurrentLegalDocuments(ctx context.Context, pool *pgxpool.Pool) ([]legalDocumentView, error) {
	rows, err := pool.Query(ctx, `SELECT document_type,version,content_url,effective_at
		FROM legal_documents WHERE is_current=true ORDER BY document_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []legalDocumentView{}
	for rows.Next() {
		var item legalDocumentView
		if err := rows.Scan(&item.DocumentType, &item.Version, &item.ContentURL, &item.EffectiveAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Server) listLegalAcceptances(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	data, err := queryLegalAcceptances(r.Context(), s.db.Writer, identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load legal acceptances.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) acceptLegalDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input legalAcceptanceInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Acceptances) < 1 || len(input.Acceptances) > 2 {
		writeError(w, http.StatusBadRequest, "invalid_acceptances", "Provide one or two legal-document acceptances.")
		return
	}
	seen := map[string]bool{}
	for index := range input.Acceptances {
		input.Acceptances[index].DocumentType = strings.ToLower(strings.TrimSpace(input.Acceptances[index].DocumentType))
		input.Acceptances[index].Version = strings.TrimSpace(input.Acceptances[index].Version)
		item := input.Acceptances[index]
		if (item.DocumentType != "terms" && item.DocumentType != "privacy") || item.Version == "" || len(item.Version) > 32 || seen[item.DocumentType] {
			writeError(w, http.StatusBadRequest, "invalid_acceptances", "Each document type may be accepted once with a valid version.")
			return
		}
		seen[item.DocumentType] = true
	}

	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record legal acceptance.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	for _, item := range input.Acceptances {
		var current bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM legal_documents
			WHERE document_type=$1 AND version=$2 AND is_current=true AND effective_at<=now())`,
			item.DocumentType, item.Version).Scan(&current); err != nil {
			break
		}
		if !current {
			writeError(w, http.StatusConflict, "legal_version_outdated", "A legal document version is no longer current. Refresh the documents and review them again.")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO legal_acceptances
			(user_id,document_type,version,accepted_ip,user_agent) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (user_id,document_type,version) DO NOTHING`,
			userID, item.DocumentType, item.Version, s.clientIP(r), r.UserAgent())
		if err != nil {
			break
		}
		column := "terms_accepted_at"
		if item.DocumentType == "privacy" {
			column = "privacy_accepted_at"
		}
		_, err = tx.Exec(r.Context(), `UPDATE users SET `+column+`=COALESCE(`+column+`,now()),updated_at=now() WHERE id=$1`, userID)
		if err != nil {
			break
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to record legal acceptance.")
		return
	}
	data, err := queryLegalAcceptances(r.Context(), s.db.Writer, userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Acceptance was recorded but could not be reloaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func queryLegalAcceptances(ctx context.Context, pool *pgxpool.Pool, userID string) ([]legalAcceptanceView, error) {
	rows, err := pool.Query(ctx, `SELECT document_type,version,accepted_at FROM legal_acceptances
		WHERE user_id=$1 ORDER BY document_type,accepted_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []legalAcceptanceView{}
	for rows.Next() {
		var item legalAcceptanceView
		if err := rows.Scan(&item.DocumentType, &item.Version, &item.AcceptedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
