package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/api"
	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/secret"
)

const maxBodyBytes = 1 << 20

type apiServer struct {
	db       *sql.DB
	sessions *SessionPool
	box      *secret.Box
}

func (s *apiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /upstreams", s.handleListUpstreams)
	mux.HandleFunc("POST /upstreams", s.handleAddUpstream)
	mux.HandleFunc("PATCH /upstreams/{name}", s.handleEditUpstream)
	mux.HandleFunc("DELETE /upstreams/{name}", s.handleDeleteUpstream)
	return logRequests(s.requireAuth(mux))
}

// requireAuth requires every request to have basic auth with the Iris
// password. The username is ignored until Iris supports multiple users.
func (s *apiServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok {
			writeError(w, r, &statusError{http.StatusUnauthorized, errors.New("basic auth is required")})
			return
		}
		if err := auth.CheckPassword(r.Context(), s.db, password); err != nil {
			writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *apiServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.ListSessionsResponse{Sessions: s.listSessions()})
}

func (s *apiServer) handleListUpstreams(w http.ResponseWriter, r *http.Request) {
	upstreams, err := s.listUpstreams(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ListUpstreamsResponse{Upstreams: upstreams})
}

func (s *apiServer) handleAddUpstream(w http.ResponseWriter, r *http.Request) {
	var req api.AddUpstreamRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, badRequest(err))
		return
	}
	if err := s.addUpstream(r.Context(), &req); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *apiServer) handleEditUpstream(w http.ResponseWriter, r *http.Request) {
	var req api.EditUpstreamRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, badRequest(err))
		return
	}
	if err := s.editUpstream(r.Context(), r.PathValue("name"), &req); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) handleDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteUpstream(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) listSessions() []api.Session {
	s.sessions.RLock()
	defer s.sessions.RUnlock()
	result := make([]api.Session, 0, len(s.sessions.streams))
	for _, upstream := range s.sessions.streams {
		name, startedAt, connected := upstream.info()
		// upstreams are added to the pool before they finish connecting
		if !connected {
			continue
		}
		result = append(result, api.Session{
			UpstreamName: name,
			TimeStarted:  startedAt,
		})
	}
	slices.SortFunc(result, func(a, b api.Session) int {
		return strings.Compare(a.UpstreamName, b.UpstreamName)
	})
	return result
}

func (s *apiServer) listUpstreams(ctx context.Context) ([]api.Upstream, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name, address, login FROM upstreams ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []api.Upstream{}
	for rows.Next() {
		var upstream api.Upstream
		if err := rows.Scan(&upstream.Name, &upstream.Address, &upstream.Login); err != nil {
			return nil, err
		}
		result = append(result, upstream)
	}
	return result, rows.Err()
}

func (s *apiServer) addUpstream(ctx context.Context, r *api.AddUpstreamRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// the password is sealed with the new row's id, so it can only be set
	// once the row exists
	res, err := tx.ExecContext(ctx,
		"INSERT INTO upstreams (name, address, login, script) VALUES (?, ?, ?, ?)",
		r.Name, r.Address, r.Login, scriptColumn(r.Script),
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE upstreams SET password=? WHERE id=?", s.box.Seal(id, []byte(r.Password)), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// scriptColumn returns the value to store for a connect script: NULL, which
// means the default, if it is missing or empty.
func scriptColumn(script *string) any {
	if script == nil || *script == "" {
		return nil
	}
	return *script
}

func (s *apiServer) deleteUpstream(ctx context.Context, name string) error {
	var id int64
	err := s.db.QueryRowContext(ctx, "DELETE FROM upstreams WHERE name=? RETURNING id", name).Scan(&id)
	if err != nil {
		return err
	}
	s.sessions.closeUpstream(id)
	return nil
}

func (s *apiServer) editUpstream(ctx context.Context, name string, r *api.EditUpstreamRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM upstreams WHERE name=?", name).Scan(&id); err != nil {
		return err
	}

	var sets []string
	var args []any
	set := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}

	if r.NewName != nil {
		set("name", *r.NewName)
	}
	if r.Password != nil {
		set("password", s.box.Seal(id, []byte(*r.Password)))
	}
	if r.Address != nil {
		set("address", *r.Address)
	}
	if r.Login != nil {
		set("login", *r.Login)
	}
	if r.Script != nil {
		set("script", scriptColumn(r.Script))
	}

	if len(sets) == 0 {
		return nil
	}

	query := "UPDATE upstreams SET " + strings.Join(sets, ", ") + " WHERE id=?"
	args = append(args, id)

	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.NewName != nil {
		s.sessions.renameUpstream(id, *r.NewName)
	}
	return nil
}

type statusError struct {
	status int
	err    error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

func badRequest(err error) error {
	return &statusError{http.StatusBadRequest, err}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return badRequest(errors.New("request body is empty"))
		}
		return badRequest(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return badRequest(errors.New("request body must contain a single JSON object"))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error().Err(err).Msg("error writing response")
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)

	var se *statusError
	var sqliteErr sqlite3.Error
	switch {
	case errors.As(err, &se):
		status, msg = se.status, se.Error()
	case errors.Is(err, sql.ErrNoRows):
		status, msg = http.StatusNotFound, "upstream not found"
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		status, msg = http.StatusUnauthorized, "incorrect Iris password"
	case errors.Is(err, auth.ErrPasswordNotSet):
		status, msg = http.StatusServiceUnavailable, err.Error()
	case errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique:
		status, msg = http.StatusConflict, "upstream already exists"
	default:
		logger.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).Msg("api error")
	}

	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="iris"`)
	}
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Dur("duration", time.Since(start)).
			Msg("api request")
	})
}
