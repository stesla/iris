package api

import (
	"errors"
	"regexp"
	"time"
)

// validName matches upstream names. Names are used in log file names, so they
// can't contain path separators or start with a dot.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var errInvalidName = errors.New("name must be 1 to 64 letters, digits, '.', '_', or '-', starting with a letter or digit")

// ValidName reports whether name can be used as an upstream name.
func ValidName(name string) bool {
	return validName.MatchString(name)
}

type Upstream struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Login   string `json:"login"`
}

type Session struct {
	UpstreamName string    `json:"upstream_name"`
	TimeStarted  time.Time `json:"time_started"`
}

type ListUpstreamsResponse struct {
	Upstreams []Upstream `json:"upstreams"`
}

type ListSessionsResponse struct {
	Sessions []Session `json:"sessions"`
}

type AddUpstreamRequest struct {
	Name     string  `json:"name"`
	Address  string  `json:"address"`
	Login    string  `json:"login"`
	Password string  `json:"password"`
	Script   *string `json:"script,omitempty"`
}

func (r *AddUpstreamRequest) Validate() error {
	switch {
	case r.Name == "":
		return errors.New("name is required")
	case !ValidName(r.Name):
		return errInvalidName
	case r.Address == "":
		return errors.New("address is required")
	case r.Login == "":
		return errors.New("login is required")
	case r.Password == "":
		return errors.New("password is required")
	}
	return nil
}

// EditUpstreamRequest only changes the fields that are non-nil. The upstream
// being edited is named in the URL.
type EditUpstreamRequest struct {
	NewName  *string `json:"new_name,omitempty"`
	Password *string `json:"password,omitempty"`
	Address  *string `json:"address,omitempty"`
	Login    *string `json:"login,omitempty"`
	Script   *string `json:"script,omitempty"`
}

func (r *EditUpstreamRequest) Validate() error {
	switch {
	case r.NewName != nil && *r.NewName == "":
		return errors.New("new_name cannot be empty")
	case r.NewName != nil && !ValidName(*r.NewName):
		return errInvalidName
	case r.Password != nil && *r.Password == "":
		return errors.New("password cannot be empty")
	case r.Address != nil && *r.Address == "":
		return errors.New("address cannot be empty")
	case r.Login != nil && *r.Login == "":
		return errors.New("login cannot be empty")
	}
	return nil
}

type ErrorResponse struct {
	Error string `json:"error"`
}
