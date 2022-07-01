//go:build !development

package main

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

//go:embed static
//go:embed db
//go:embed tmpl/*.tmpl
var contentFs embed.FS

type TemplateMap map[string]*template.Template

func (templates TemplateMap) ExecuteTemplate(wr io.Writer, name string, data interface{}) error {
	tmpl, ok := templates[name]
	if !ok {
		return fmt.Errorf("no such template: %s", name)
	}

	return tmpl.ExecuteTemplate(wr, name, data)
}

func NewTemplates() (TemplateMap, error) {
	tmpls := make(map[string]*template.Template)

	fs.WalkDir(contentFs, "tmpl", func(templatePath string, d fs.DirEntry, err error) error {
		name := path.Base(templatePath)
		if name != "tmpl/base.html.tmpl" && strings.HasSuffix(name, ".tmpl") {
			t, err := template.ParseFS(contentFs, "tmpl/base.html.tmpl", path.Join("tmpl", name))
			if err != nil {
				return err
			}
			tmpls[name] = t
		}

		return nil
	})

	return tmpls, nil
}

type PreparedQueries map[string]*sql.Stmt

func (queries PreparedQueries) Get(name string) (Query, error) {
	stmt, ok := queries[name]
	if ok {
		return stmt, nil
	}

	return nil, ErrQueryNotFound
}

func (queries PreparedQueries) Close() error {
	for _, stmt := range queries {
		if err := stmt.Close(); err != nil {
			return err
		}
	}

	return nil
}

func NewQueries(db *sql.DB) (PreparedQueries, error) {
	entries, err := contentFs.ReadDir("db/queries")
	if err != nil {
		return nil, err
	}

	stmts := make(PreparedQueries)

	for _, entry := range entries {
		fileInfo, err := entry.Info()
		if err != nil {
			return nil, err
		}

		if !strings.HasSuffix(fileInfo.Name(), ".sql") {
			continue
		}

		name := strings.TrimSuffix(fileInfo.Name(), ".sql")
		fpath := strings.Join([]string{"db", "queries", fileInfo.Name()}, "/")

		query, err := contentFs.ReadFile(fpath)
		if err != nil {
			return nil, err
		}

		stmt, err := db.Prepare(string(query))
		if err != nil {
			return nil, fmt.Errorf("cannot prepare statement: %w", err)
		}

		stmts[name] = stmt
	}

	return stmts, nil
}
