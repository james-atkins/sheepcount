//go:build development

package main

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path"
)

var contentFs fs.FS

func init() {
	contentFs = os.DirFS(".")
}

type DiskTemplates struct{}

func NewTemplates() (DiskTemplates, error) {
	return DiskTemplates{}, nil
}

func (templates DiskTemplates) ExecuteTemplate(wr io.Writer, name string, data interface{}) error {
	tmpl, err := template.ParseFiles("tmpl/base.html.tmpl", path.Join("tmpl", name))
	if err != nil {
		return err
	}

	return tmpl.ExecuteTemplate(wr, name, data)
}

type DiskQueries struct {
	db *sql.DB
}

func NewQueries(db *sql.DB) (*DiskQueries, error) {
	return &DiskQueries{db: db}, nil
}

func (queries *DiskQueries) Get(name string) (Query, error) {
	sqlPath := path.Join("db", "queries", name+".sql")

	query, err := fs.ReadFile(contentFs, sqlPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrQueryNotFound
	}
	if err != nil {
		return nil, err
	}

	return &DiskQuery{db: queries.db, query: string(query)}, nil
}

type DiskQuery struct {
	db    *sql.DB
	query string
}

func (query *DiskQuery) QueryRowContext(ctx context.Context, args ...interface{}) *sql.Row {
	return query.db.QueryRowContext(ctx, query.query, args...)
}
