package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/sync/errgroup"
)

type SheepCount struct {
	db      *sql.DB
	state   *State
	queries Queries
	tmpl    Templater

	Config

	// Override default behaviour
	fingerprinter     func(*SheepCount, *http.Request) ([]byte, []byte, Error)
	javascriptHandler func(*SheepCount, http.ResponseWriter, *http.Request)
}

type Config struct {
	Domains   []string `toml:"domains"`
	Password  string   `toml:"password"`
	CookieKey string   `toml:"cookie_key"`
	CSRFKey   string   `toml:"csrf_key"`

	HeadersToHash        []string      `toml:"headers"`
	SaltRotationDuration time.Duration `toml:"rotation_frequency"`
	AllowLocalhost       bool
	ReverseProxy         bool
	Hostname             string `toml:"hostname"` // If behind a reverse proxy, the server hostname
}

type State struct {
	Salts Salts `json:"salts"`
	GeoIP GeoIP `json:"geoip"`
}

// We want to track unique views over a T hour time period so we generate two
// random salts and rotate them every T/2 hours. When a new pageview comes in we
// try to find an existing session based on the current and previous salt.
// This ensures there isn't some arbitrary cut-off time when the salt is rotated.
type Salts struct {
	sync.RWMutex
	LastRotated time.Time `json:"last_rotated"`
	Current     [16]byte  `json:"current"`
	Previous    [16]byte  `json:"previous"`
}

type Templater interface {
	ExecuteTemplate(wr io.Writer, name string, data interface{}) error
}

var ErrQueryNotFound = errors.New("query not found")

type Queries interface {
	Get(name string) (Query, error)
}

type Query interface {
	QueryRowContext(context.Context, ...interface{}) *sql.Row
}

func NewSheepCount(db *sql.DB, config Config) (*SheepCount, error) {
	tmpl, err := NewTemplates()
	if err != nil {
		return nil, err
	}

	queries, err := NewQueries(db)
	if err != nil {
		return nil, err
	}

	state := &State{}
	if err := state.Load("sheepcount.state", &config); err != nil {
		return nil, fmt.Errorf("cannot load state: %w", err)
	}

	sheepcount := &SheepCount{
		db:      db,
		state:   state,
		queries: queries,
		tmpl:    tmpl,
		Config:  config,
	}

	return sheepcount, nil
}

func (sheepcount *SheepCount) Run(ctx context.Context, socket net.Listener) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	hits := make(chan Hit, 1024)

	errgrp.Go(func() error {
		return DatabaseWriter(ctx, sheepcount.db, hits)
	})

	// Goroutine to rotate the salts and delete expired identifiers
	errgrp.Go(func() error {
		// When is the next time we need to rotate the salts?
		sheepcount.state.Salts.RLock()
		nextRotation := time.Until(sheepcount.state.Salts.LastRotated.Add(sheepcount.SaltRotationDuration))
		sheepcount.state.Salts.RUnlock()

		if nextRotation > 0 {
			after := time.After(nextRotation)
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-after:
				if err := sheepcount.state.Salts.Rotate(); err != nil {
					return fmt.Errorf("error rotating salts: %w", err)
				}

				n, err := dbDeleteExpired(ctx, 2*sheepcount.SaltRotationDuration, sheepcount.db)
				if err != nil {
					return fmt.Errorf("cannot delete expired identifiers: %w", err)
				}

				if n > 0 {
					log.Printf("Deleted %d expired identifiers.", n)
				}
			}
		}

		// Now delete at a regular interval
		ticker := time.NewTicker(sheepcount.SaltRotationDuration)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-ticker.C:
				if err := sheepcount.state.Salts.Rotate(); err != nil {
					return fmt.Errorf("error rotating salts: %w", err)
				}

				n, err := dbDeleteExpired(ctx, 2*sheepcount.SaltRotationDuration, sheepcount.db)
				if err != nil {
					return fmt.Errorf("cannot delete expired identifiers: %w", err)
				}

				if n > 0 {
					log.Printf("Deleted %d expired identifiers.", n)
				}
			}
		}
	})

	// Goroutine to keep geolocation database up-to-date
	errgrp.Go(func() error {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-ticker.C:
				if err := sheepcount.state.GeoIP.Update(); err != nil {
					log.Printf("Cannot update GeoIP database: %s", err)
				}
			}
		}
	})

	// Goroutine to persist state on exit
	errgrp.Go(func() error {
		<-ctx.Done()

		if err := sheepcount.state.Save("sheepcount.state"); err != nil {
			return fmt.Errorf("error persisting state: %w", err)
		}

		return nil
	})

	// Create the HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { handleHome(sheepcount, w, r) })
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) { handleEvent(sheepcount, hits, w, r) })
	mux.HandleFunc("/count.js", sheepcount.handleJavascript)
	mux.HandleFunc("/queries/", func(w http.ResponseWriter, r *http.Request) {
		handleQueries(sheepcount, w, r)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		handleLogin(sheepcount, w, r)
	})
	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		handleLogout(sheepcount, w, r)
	})
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.FS(contentFs)).ServeHTTP(w, r)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		f, err := contentFs.Open("static/favicon.ico")
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "image/x-icon")
		io.Copy(w, f)
	})

	srv := http.Server{Handler: recoverer(ipAddress(sheepcount.ReverseProxy, mux))}

	// Goroutine to run the server
	errgrp.Go(func() error {
		if err := srv.Serve(socket); err != http.ErrServerClosed {
			return err
		}
		return nil
	})

	// Goroutine to shutdown the server gracefully
	errgrp.Go(func() error {
		<-ctx.Done()

		// Give the server a bit of time to shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	})

	return errgrp.Wait()
}

func (sheepcount *SheepCount) getHost(r *http.Request) string {
	if sheepcount.ReverseProxy {
		return sheepcount.Hostname
	} else {
		return r.Host
	}
}

func (sheepcount *SheepCount) handleJavascript(w http.ResponseWriter, r *http.Request) {
	if sheepcount.javascriptHandler != nil {
		sheepcount.javascriptHandler(sheepcount, w, r)
		return
	}

	var eventUrl url.URL
	eventUrl.Path = "event"
	if sheepcount.ReverseProxy {
		eventUrl.Scheme = "https"
		eventUrl.Host = sheepcount.Hostname
	} else {
		if r.TLS == nil {
			eventUrl.Scheme = "http"
		} else {
			eventUrl.Scheme = "https"
		}
		eventUrl.Host = r.Host
	}

	js, hash, err := sheepJS(sheepcount.tmpl, sheepcount.AllowLocalhost, eventUrl.String())
	if err != nil {
		log.Printf("cannot serve javascript: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	etag := fmt.Sprintf(`"%x"`, hash) // ETags must be quoted

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Cache-Control", "max-age=86400, must-revalidate")
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("ETag", etag)
	w.Write(js)
}

func (sheepcount *SheepCount) fingerprintRequest(r *http.Request) ([]byte, []byte, Error) {
	if sheepcount.fingerprinter != nil {
		return sheepcount.fingerprinter(sheepcount, r)
	}

	sheepcount.state.Salts.RLock()
	defer sheepcount.state.Salts.RUnlock()

	hasherCurrent, err := blake2b.New(blake2b.Size256, sheepcount.state.Salts.Current[:])
	if err != nil {
		return nil, nil, NewInternalError(err)
	}

	hasherPrevious, err := blake2b.New(blake2b.Size256, sheepcount.state.Salts.Previous[:])
	if err != nil {
		return nil, nil, NewInternalError(err)
	}

	hasherCurrent.Write([]byte(r.RemoteAddr))
	hasherPrevious.Write([]byte(r.RemoteAddr))

	for _, header := range sheepcount.HeadersToHash {
		hasherCurrent.Write([]byte(r.Header.Get(header)))
		hasherPrevious.Write([]byte(r.Header.Get(header)))
	}

	return hasherCurrent.Sum(nil), hasherPrevious.Sum(nil), nil
}

func DefaultConfig() Config {
	return Config{
		HeadersToHash:        []string{"User-Agent", "Accept-Encoding", "Accept-Language"},
		SaltRotationDuration: 12 * time.Hour,
		AllowLocalhost:       false,
		ReverseProxy:         false,
		Hostname:             "",
	}
}

func (state *State) Load(statePath string, config *Config) error {
	f, err := os.Open(statePath)
	if errors.Is(err, os.ErrNotExist) {
		if err := state.Salts.Load(config.SaltRotationDuration); err != nil {
			return err
		}
		if err := state.GeoIP.Load(); err != nil {
			return err
		}

		return nil
	}

	if err != nil {
		return err
	}
	defer f.Close()

	contents, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	state.Salts.Lock()
	state.GeoIP.Lock()
	err = json.Unmarshal(contents, state)
	state.GeoIP.Unlock()
	state.Salts.Unlock()

	if err != nil {
		return err
	}

	if err := state.Salts.Load(config.SaltRotationDuration); err != nil {
		return err
	}
	if err := state.GeoIP.Load(); err != nil {
		return err
	}

	return nil
}

func (state *State) Save(statePath string) error {
	state.Salts.RLock()
	defer state.Salts.RUnlock()

	contents, err := json.Marshal(state)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(statePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(contents)
	if err != nil {
		return err
	}

	return nil
}

func (salts *Salts) Load(rotationFreq time.Duration) error {
	if salts.LastRotated.IsZero() {
		log.Print("Generating random salts")

		salts.LastRotated = time.Now().UTC()
		if _, err := rand.Read(salts.Current[:]); err != nil {
			return err
		}
		if _, err := rand.Read(salts.Previous[:]); err != nil {
			return err
		}

		return nil
	}

	if time.Since(salts.LastRotated) >= rotationFreq {
		if err := salts.Rotate(); err != nil {
			return err
		}
	}

	return nil
}

func (salts *Salts) Rotate() error {
	salts.Lock()
	defer salts.Unlock()

	var next [16]byte
	if _, err := rand.Read(next[:]); err != nil {
		return err
	}

	salts.LastRotated = time.Now().UTC()
	copy(salts.Previous[:], salts.Current[:])
	copy(salts.Current[:], next[:])

	return nil
}

func handleEvent(sheepcount *SheepCount, hits chan<- Hit, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")

	hit, err := NewHit(sheepcount, r)
	if err != nil {
		w.WriteHeader(err.StatusCode())
		log.Print(err)
		return
	}

	hits <- hit
	w.WriteHeader(http.StatusNoContent)
}

func sheepJS(tmpl Templater, allowLocalhost bool, url string) ([]byte, []byte, error) {
	var buf bytes.Buffer

	params := struct {
		AllowLocalhost bool
		Url            string
	}{
		AllowLocalhost: allowLocalhost,
		Url:            url,
	}

	if err := tmpl.ExecuteTemplate(&buf, "sheepcount.js.tmpl", params); err != nil {
		return nil, nil, err
	}

	// Compute the hash of the Javascript for use as an ETag
	hasher, err := blake2b.New(16, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot create blake2b hasher: %w", err)
	}
	hasher.Write(buf.Bytes())
	hash := hasher.Sum(nil)

	return buf.Bytes(), hash, nil
}
