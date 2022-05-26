package main

import (
	"database/sql"
	_ "embed"
	"fmt"
	"net/http"
	"text/template"

	"github.com/oschwald/geoip2-golang"
)

//go:embed analytics.js
var javascriptTemplate string

type SheepCount struct {
	Db   *sql.DB
	Geo  *geoip2.Reader
	Tmpl *template.Template

	// Config
	Domains        []string
	Key            []byte
	AllowLocalhost bool
	ReverseProxy   bool
}

func NewSheepCount(db *sql.DB, geo *geoip2.Reader) (*SheepCount, error) {
	tmpl, err := template.New("analytics.js").Parse(javascriptTemplate)
	if err != nil {
		return nil, err
	}

	env := &SheepCount{
		Db:             db,
		Geo:            geo,
		Tmpl:           tmpl,
		AllowLocalhost: true,
	}

	return env, nil
}

type Error interface {
	error
	Unwrap() error
	StatusCode() int
}

type ErrNotAuthorized struct {
	wrapped error
}

func (err *ErrNotAuthorized) Error() string {
	return fmt.Sprintf("not authorized: %s", err.wrapped)
}

func (err *ErrNotAuthorized) Unwrap() error {
	return err.wrapped
}

func (err *ErrNotAuthorized) StatusCode() int {
	return http.StatusForbidden
}

func NotAuthorizedErr(err error) error {
	return &ErrNotAuthorized{wrapped: err}
}

type ErrBadInput struct {
	wrapped error
}

func BadInput(err error) Error {
	return &ErrBadInput{wrapped: err}
}

func (err *ErrBadInput) Error() string {
	return fmt.Sprintf("bad input: %s", err.wrapped)
}

func (err *ErrBadInput) Unwrap() error {
	return err.wrapped
}

func (err *ErrBadInput) StatusCode() int {
	return http.StatusBadRequest
}

type InternalError struct{ wrapped error }

func NewInternalError(err error) Error {
	return &InternalError{wrapped: err}
}

func (err *InternalError) Error() string {
	return fmt.Sprintf("bad input: %s", err.wrapped)
}

func (err *InternalError) Unwrap() error {
	return err.wrapped
}

func (err *InternalError) StatusCode() int {
	return http.StatusInternalServerError
}
