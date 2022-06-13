package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/sync/errgroup"
)

//go:embed sheepcount.js
var javascriptTemplate string

type SheepCount struct {
	db        *sql.DB
	geo       *geoip2.Reader
	tmpl      *template.Template
	saltsfile *os.File

	Config
	Salts

	// Override default behaviour
	fingerprinter     func(*SheepCount, *http.Request) ([]byte, []byte, Error)
	javascriptHandler func(*SheepCount, http.ResponseWriter, *http.Request)
}

type Config struct {
	Domains []string `toml:"domains"`

	HeadersToHash        []string      `toml:"headers"`
	SaltRotationDuration time.Duration `toml:"rotation_frequency"`
	AllowLocalhost       bool
	ReverseProxy         bool
	Hostname             string `toml:"hostname"` // If behind a reverse proxy, the server hostname
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

func NewSheepCount(db *sql.DB, geo *geoip2.Reader, config Config, saltsfilename string) (*SheepCount, error) {
	tmpl, err := template.New("analytics.js").Parse(javascriptTemplate)
	if err != nil {
		return nil, err
	}

	saltsfile, err := os.OpenFile(saltsfilename, os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}

	sheepcount := &SheepCount{
		db:        db,
		geo:       geo,
		tmpl:      tmpl,
		saltsfile: saltsfile,
		Config:    config,
	}

	sheepcount.Salts.loadFromFile(saltsfile)

	if time.Since(sheepcount.Salts.LastRotated) >= config.SaltRotationDuration {
		if err := sheepcount.Salts.Rotate(); err != nil {
			return nil, err
		}
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
		sheepcount.Salts.RLock()
		nextRotation := time.Until(sheepcount.LastRotated.Add(sheepcount.SaltRotationDuration))
		sheepcount.Salts.RUnlock()

		if nextRotation > 0 {
			after := time.After(nextRotation)
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-after:
				if err := sheepcount.Salts.Rotate(); err != nil {
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
				if err := sheepcount.Salts.Rotate(); err != nil {
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

	// Goroutine to save salts on exit
	errgrp.Go(func() error {
		<-ctx.Done()

		if err := sheepcount.Salts.saveToFile(sheepcount.saltsfile); err != nil {
			log.Printf("error saving salts to file: %s", err)
			return err
		}

		if err := sheepcount.saltsfile.Close(); err != nil {
			log.Printf("cannot close salts file: %s", err)
			return err
		}

		return nil
	})

	// Create the HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { handleHome(sheepcount, w, r) })
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) { handleEvent(sheepcount, hits, w, r) })
	mux.HandleFunc("/sheep.js", sheepcount.handleJavascript)
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

	sheepcount.Salts.RLock()
	defer sheepcount.Salts.RUnlock()

	hasherCurrent, err := blake2b.New(blake2b.Size256, sheepcount.Salts.Current[:])
	if err != nil {
		return nil, nil, NewInternalError(err)
	}

	hasherPrevious, err := blake2b.New(blake2b.Size256, sheepcount.Salts.Previous[:])
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

func (salts *Salts) saveToFile(file *os.File) error {
	salts.RLock()
	defer salts.RUnlock()

	contents, err := json.Marshal(salts)
	if err != nil {
		return err
	}

	_, err = file.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("cannot seek: %w", err)
	}
	_, err = file.Write(contents)
	if err != nil {
		return fmt.Errorf("cannot write: %w", err)
	}

	return nil
}

func (salts *Salts) loadFromFile(file *os.File) error {
	salts.Lock()
	defer salts.Unlock()

	file.Seek(0, 0)

	contents, err := ioutil.ReadAll(file)
	if len(contents) == 0 {
		goto generateRandom
	}
	if err != nil {
		return fmt.Errorf("cannot read salts: %w", err)
	}

	err = json.Unmarshal(contents, &salts)
	if err != nil {
		return fmt.Errorf("cannot read salts: %w", err)
	}

	return nil

generateRandom:
	salts.LastRotated = time.Now().UTC()
	if _, err := rand.Read(salts.Current[:]); err != nil {
		return fmt.Errorf("cannot generate salts: %w", err)
	}
	if _, err := rand.Read(salts.Previous[:]); err != nil {
		return fmt.Errorf("cannot generate salts: %w", err)
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

func handleHome(sheepcount *SheepCount, w http.ResponseWriter, r *http.Request) {
	if !(r.URL.Path == "/" || r.URL.Path == "/index.html") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Write([]byte(`
<!doctype html>
<html>
<head>
<title>SheepCount</title>
<script src="/sheep.js" defer></script>
</head>
<body>
Welcome to SheepCount.
</body>
</html>
	`))
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

func sheepJS(tmpl *template.Template, allowLocalhost bool, url string) ([]byte, []byte, error) {
	var buf bytes.Buffer

	params := struct {
		AllowLocalhost bool
		Url            string
	}{
		AllowLocalhost: allowLocalhost,
		Url:            url,
	}

	if err := tmpl.Execute(&buf, params); err != nil {
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
