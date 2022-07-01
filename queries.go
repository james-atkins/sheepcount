package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/mattn/go-sqlite3"
)

// Check YYYY-MM-DD format
func validDate(date string) bool {
	if len(date) != 10 {
		return false
	}

	for i, c := range date {
		if i == 4 || i == 7 {
			if c == '-' {
				continue
			}
			return false
		} else {
			if unicode.IsDigit(c) {
				continue
			}
			return false
		}
	}

	return true
}

// SQLite produces JSON and we just return that. Nothing more!
func handleQueries(sheepcount *SheepCount, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !strings.HasPrefix(r.URL.Path, "/queries/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	token := getAuthCookie(r, sheepcount.CookieKey)
	if !token.LoggedIn {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	queryName := strings.TrimPrefix(r.URL.Path, "/queries/")

	query, err := sheepcount.queries.Get(queryName)
	if err == ErrQueryNotFound {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		log.Print(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert the query parameters to sql NamedParemeters
	params := r.URL.Query()
	args := make([]interface{}, 0, len(params))

	for k, vs := range params {
		if len(vs) > 0 {
			v := vs[0]

			// For common parameters, check they are of the correct types

			if k == "start_date" || k == "end_date" {
				if !validDate(v) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				args = append(args, sql.Named(k, v))
				continue
			}

			if k == "utc_offset" {
				offset, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				args = append(args, sql.Named(k, offset))
				continue
			}

			// For other parameters, try and convert to integer or float, and if this fails,
			// use as a string

			integer, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				args = append(args, sql.Named(k, integer))
				continue
			}

			float, err := strconv.ParseFloat(v, 64)
			if err == nil {
				args = append(args, sql.Named(k, float))
				continue
			}

			args = append(args, sql.Named(k, v))
		}
	}

	var output []byte
	row := query.QueryRowContext(r.Context(), args...)
	if err := row.Scan(&output); err != nil {
		if errsqlite, ok := err.(sqlite3.Error); ok {
			log.Print(errsqlite.Code)
			log.Print(errsqlite.ExtendedCode)
		}
		log.Print(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Pretty print JSON
	var buf bytes.Buffer
	if err := json.Indent(&buf, output, "", "  "); err != nil {
		log.Print(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	buf.WriteTo(w)
}
