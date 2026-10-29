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
)

const maxBodyBytes = 1 << 20

type apiServer struct {
	db       *sql.DB
	sessions *SessionPool
}

func (s *apiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /upstreams", s.handleListUpstreams)
	mux.HandleFunc("POST /upstreams", s.handleAddUpstream)
	mux.HandleFunc("PATCH /upstreams/{name}", s.handleEditUpstream)
	mux.HandleFunc("DELETE /upstreams/{name}", s.handleDeleteUpstream)
	return logRequests(mux)
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
	name, password, err := basicAuth(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req api.EditUpstreamRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, badRequest(err))
		return
	}
	if err := s.editUpstream(r.Context(), name, password, &req); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) handleDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	name, password, err := basicAuth(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.deleteUpstream(r.Context(), name, password); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) authenticate(ctx context.Context, name, password string) error {
	_, err := authenticateUpstream(ctx, s.db, name, password)
	return err
}

func (s *apiServer) listSessions() []api.Session {
	s.sessions.RLock()
	defer s.sessions.RUnlock()
	result := make([]api.Session, 0, len(s.sessions.streams))
	for key, upstream := range s.sessions.streams {
		// upstreams are added to the pool before they finish connecting
		if !upstream.IsConnected() {
			continue
		}
		result = append(result, api.Session{
			UpstreamName: key,
			TimeStarted:  upstream.startedAt,
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
	hash, err := bcrypt.GenerateFromPassword([]byte(r.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		"INSERT INTO upstreams (name, address, login, bcrypt, script) VALUES (?, ?, ?, ?, ?)",
		r.Name, r.Address, r.Login, hash, r.Script,
	)
	return err
}

func (s *apiServer) deleteUpstream(ctx context.Context, name, password string) error {
	if err := s.authenticate(ctx, name, password); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, "DELETE FROM upstreams WHERE name=?", name)
	return err
}

func (s *apiServer) editUpstream(ctx context.Context, name, password string, r *api.EditUpstreamRequest) error {
	if err := s.authenticate(ctx, name, password); err != nil {
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
	if r.NewPassword != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*r.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		set("bcrypt", hash)
	}
	if r.Address != nil {
		set("address", *r.Address)
	}
	if r.Login != nil {
		set("login", *r.Login)
	}
	if r.Script != nil {
		set("script", *r.Script)
	}

	if len(sets) == 0 {
		return nil
	}

	query := "UPDATE upstreams SET " + strings.Join(sets, ", ") + " WHERE name=?"
	args = append(args, name)

	_, err := s.db.ExecContext(ctx, query, args...)
	return err
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

// basicAuth returns the upstream name and password from the request's basic
// auth, which must name the same upstream as the URL.
func basicAuth(r *http.Request) (name, password string, err error) {
	name, password, ok := r.BasicAuth()
	if !ok {
		return "", "", &statusError{http.StatusUnauthorized, errors.New("basic auth is required")}
	}
	if name != r.PathValue("name") {
		return "", "", badRequest(errors.New("basic auth username does not match upstream name"))
	}
	return name, password, nil
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
		status, msg = http.StatusUnauthorized, "incorrect password"
	case errors.Is(err, bcrypt.ErrPasswordTooLong):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.As(err, &sqliteErr) &&
		(sqliteErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey ||
			sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique):
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
