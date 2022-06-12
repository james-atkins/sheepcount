package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oschwald/geoip2-golang"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// Exit on Ctrl-C
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	defer func() {
		signal.Stop(signalChan)
		close(signalChan)
	}()

	go func() {
		// First signal: exit gracefully
		select {
		case <-signalChan:
			log.Print("Exiting...")
			cancel()
		case <-ctx.Done():
			return
		}

		// Second signal: force exit
		_, ok := <-signalChan
		if ok {
			os.Exit(1)
		}
	}()

	var keyPath string
	var key []byte

	var databasePath string
	var db *sql.DB

	var geoPath string
	var geo *geoip2.Reader

	var port int
	var socket string

	cmd := cobra.Command{
		Use: "sheepcount",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			key, err = os.ReadFile(keyPath)
			if err != nil {
				return err
			}
			if len(key) < 16 {
				return fmt.Errorf("key must be longer than 16 bytes")
			}

			db, err = dbConnect(databasePath)
			if err != nil {
				log.Print(err)
				return
			}

			geo, err = geoip2.Open(geoPath)
			if err != nil {
				return err
			}

			var env *SheepCount
			env, err = NewSheepCount(db, geo)
			if err != nil {
				return err
			}
			env.Domains = []string{"jdsa3.user.srcf.net", "www.jamesatkins.net"}
			env.Key = key

			var l net.Listener
			if socket != "" {
				// Delete the socket first
				err = os.Remove(socket)
				if err != nil && !os.IsNotExist(err) {
					return err
				}

				l, err = net.Listen("unix", socket)
				if err != nil {
					return err
				}

				// Restrict access to socket
				err = os.Chmod(socket, 0700)
				if err != nil {
					return err
				}

				env.AllowLocalhost = false
				env.ReverseProxy = true
			} else {
				l, err = net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
				env.AllowLocalhost = true
			}
			if err != nil {
				return err
			}

			if err := main2(ctx, env, l); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("%+v", err)
			}

			return nil
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			if err := geo.Close(); err != nil {
				log.Print(err)
			}

			if _, err := db.Exec("PRAGMA optimize"); err != nil {
				log.Print(err)
			}

			if err := db.Close(); err != nil {
				log.Print(err)
			}
		},
	}

	cmd.PersistentFlags().StringVar(&keyPath, "key", "sheepcount.key", "Path to keyfile")
	cmd.PersistentFlags().StringVar(&databasePath, "database", "sheepcount.sqlite3", "Path to database")
	cmd.PersistentFlags().StringVar(&geoPath, "geoip-database", "GeoLite2-City.mmdb", "Path to GeoIP2 database")
	cmd.PersistentFlags().IntVar(&port, "port", 4444, "Port to listen on")
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Socket to listen on")

	cmd.Execute()
}

func main2(ctx context.Context, env *SheepCount, socket net.Listener) error {
	// Now create the HTTP server
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	<title>Home</title>
	<script src="/sheep.js" defer></script>
</head>
<body>
	Hello!
</body>
</html>
		`))
	})

	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		if err := handleCount(env, r); err != nil {
			w.WriteHeader(err.StatusCode())
			log.Print(err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("/sheep.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if err := handleJavascript(r.Context(), env, w, r); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Print(err)
		}
	})

	srv := http.Server{Handler: recoverer(mux)}

	errgrp, ctx := errgroup.WithContext(ctx)

	errgrp.Go(func() error {
		if err := srv.Serve(socket); err != http.ErrServerClosed {
			return err
		}
		return nil
	})

	errgrp.Go(func() error {
		<-ctx.Done()

		// Give the server a bit of time to shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	})

	errgrp.Go(func() error {
		return DatabaseWriter(ctx, env.Db, env.Hits)
	})

	errgrp.Go(func() error {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-ticker.C:
				n, err := DeleteExpired(ctx, 730*24*time.Hour, env.Db)
				if err != nil {
					return fmt.Errorf("cannot delete expired identifiers: %w", err)
				}

				if n > 0 {
					log.Printf("Deleted %d expired identifiers.", n)
				}
			}
		}
	})

	return errgrp.Wait()
}

func recoverer(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil && rvr != http.ErrAbortHandler {
				log.Print(rvr)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}
