package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"

	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/blake2b"
	"zgo.at/isbot"
)

func handleCount(env *SheepCount, r *http.Request) Error {
	var hit Hit
	if err := hit.FromEndpoint(env, r); err != nil {
		return err
	}

	ctx := r.Context()

	tx, err := env.Db.BeginTx(ctx, nil)
	if err != nil {
		return NewInternalError(err)
	}
	defer tx.Rollback()

	// In WAL mode, if we start a transaction and run a SELECT followed by an INSERT, SQLite will
	// immediately report a locked database error if there is already another write transaction.
	// As we know that we are going to insert data, let's always start the transaction in IMMEDIATE
	// mode. This works around this known bug: https://github.com/mattn/go-sqlite3/issues/400.
	if _, err := tx.ExecContext(ctx, "ROLLBACK; BEGIN IMMEDIATE"); err != nil {
		return NewInternalError(err)
	}

	if err := dbInsertHit(ctx, tx, &hit); err != nil {
		return NewInternalError(err)
	}

	if err := tx.Commit(); err != nil {
		return NewInternalError(err)
	}

	return nil
}

func handlePixel(w http.ResponseWriter, r *http.Request) error {
	if isbot.Prefetch(r.Header) {
		// Do not count yet...
		w.Header().Set("Cache-Control", "must-revalidate")

		// Serve image

		return nil
	}

	// Requests are of the form
	// /sheep.gif?url=/about/

	// There is much less information than the javascript POST request.
	// E.g. no referrer, no page size information etc

	u := r.URL.Query()

	pageUrl := u.Get("url")
	if pageUrl == "" {
		return BadInput(fmt.Errorf("missing URL parameter"))
	}

	return nil
}

func handleJavascript(ctx context.Context, env *SheepCount, w http.ResponseWriter, r *http.Request) error {
	tx, err := env.Db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// We have two situations: first the user is unknown and so we serve them a new payload, or the
	// user has been seen before and we reply 304 Not Modified.
	etag := r.Header.Get("If-None-Match")
	if etag != "" {
		// We have seen this user before ;) Get their identifier
		ident, userJsHash, err := decodeETag(etag, env.Key)
		if err == nil {
			// Bump when they were last seen
			_, err = tx.ExecContext(
				ctx,
				"UPDATE users SET last_seen = strftime('%s', 'now') WHERE identifier = ?",
				ident[:],
			)
			if err != nil {
				return err
			}

			if err := tx.Commit(); err != nil {
				return err
			}

			// Now check that the JavaScript hash is up-to-date
			js, jsHash, err := personalisedJS(env, ident)
			if err != nil {
				return err
			}

			if bytes.Equal(userJsHash[:], jsHash[:]) {
				w.WriteHeader(http.StatusNotModified)
			} else {
				servePersonalisedJS(env, w, ident, js, jsHash)
			}

			return nil
		}

		// The identifier did not decode correctly. Log and create a new one.
		log.Printf("Decoding ETag failed: %s", err.Error())
	}

	// Generate a new identifier
	// Chance of duplicates is TINY but use a loop to make sure
	var ident Identifier
	for {
		if _, err := rand.Read(ident[:]); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO users (identifier) VALUES (?)", ident[:]); err != nil {
			if sqliteErr, ok := err.(sqlite3.Error); ok {
				if sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
					continue
				}
			}
			return err
		}

		break
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Finally serve the personalised JS
	js, jsHash, err := personalisedJS(env, ident)
	if err != nil {
		return err
	}

	servePersonalisedJS(env, w, ident, js, jsHash)

	return nil
}

func personalisedJS(env *SheepCount, ident Identifier) ([]byte, JsHash, error) {
	var buf bytes.Buffer
	var jsHash JsHash

	params := struct {
		AllowLocalhost bool
		Url            string
		Token          string
	}{
		AllowLocalhost: env.AllowLocalhost,
		Url:            "/event",
		Token:          encodeToken(env.Key, ident),
	}

	if err := env.Tmpl.Execute(&buf, params); err != nil {
		return nil, jsHash, err
	}

	// Compute the truncated hash of the javascript
	hasher, err := blake2b.New(blakeSize128, nil)
	if err != nil {
		panic(err)
	}
	hasher.Write(buf.Bytes())
	hash := hasher.Sum(nil)
	copy(jsHash[:], hash[:16])

	return buf.Bytes(), jsHash, nil
}

func servePersonalisedJS(env *SheepCount, w http.ResponseWriter, ident Identifier, js []byte, jsHash JsHash) {
	// w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Cache-Control", "private, must-revalidate")

	w.Header().Set("ETag", encodeETag(env.Key, ident, jsHash))
	w.Header().Set("Content-Type", "application/javascript")

	if _, err := w.Write(js); err != nil {
		// Too late to return err so just log it.
		log.Print(err)
	}
}
