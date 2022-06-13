package main

import (
	_ "embed"
	"fmt"
	"net/http"
)

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
