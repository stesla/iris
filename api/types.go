package api

import (
	"errors"
	"time"
)

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
// being edited is named in the URL and authenticated with basic auth.
type EditUpstreamRequest struct {
	NewName     *string `json:"new_name,omitempty"`
	NewPassword *string `json:"new_password,omitempty"`
	Address     *string `json:"address,omitempty"`
	Login       *string `json:"login,omitempty"`
	Script      *string `json:"script,omitempty"`
}

func (r *EditUpstreamRequest) Validate() error {
	switch {
	case r.NewName != nil && *r.NewName == "":
		return errors.New("new_name cannot be empty")
	case r.NewPassword != nil && *r.NewPassword == "":
		return errors.New("new_password cannot be empty")
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
