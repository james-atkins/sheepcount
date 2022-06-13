package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"golang.org/x/sync/errgroup"
	"zgo.at/gadget"
	"zgo.at/isbot"
)

//go:embed db/*.sql
var dbFs embed.FS

func DatabaseWriter(ctx context.Context, db *sql.DB, hitC <-chan Hit) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	// Writing each hit one-by-one can be slow. So instead, batch them and then
	// write the whole batch to the database.
	// This functions creates two goroutines. The first reads individual hits from
	// the channel and then batches them into a slice. Once the slice is big enough
	// or the elapsed time has passed, then the goroutine sends the slice to the
	// batched channel and the second goroutine then commits the whole slice to the
	// database.
	hitsC := make(chan []Hit)

	errgrp.Go(func() error {
		ticker := time.NewTicker(10 * time.Second)
		hits := make([]Hit, 0, 16)

		for {
			select {
			case <-ctx.Done():
				// Before shutting down, make sure that we submit any remaining hits
				// to the database writer goroutine.
				if len(hits) > 0 {
					hitsC <- hits
				}

				// Signal to the database writer goroutine that we are shutting down
				close(hitsC)
				return ctx.Err()

			case <-ticker.C:
				if len(hits) == 0 {
					continue
				}
				hitsC <- hits
				hits = make([]Hit, 0, 16)

			case hit := <-hitC:
				hits = append(hits, hit)
				if len(hits) >= 256 {
					hitsC <- hits
					hits = make([]Hit, 0, 16)
				}
			}
		}
	})

	errgrp.Go(func() error {
		// Grab a connection from the pool and keep it for the whole life of the goroutine
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()

		// TODO: prepared statements

		// When ctx.Done() closes, the above goroutine sends any remaining batched hits
		// to the channel and then closes it. So there is no need to select on ctx.Done()
		// here too.
		// Note: As we want to write hits to the database even when we are shutting down, we use
		// the background context in all database function calls.
		for hits := range hitsC {
			err := func() error {
				tx, err := conn.BeginTx(context.Background(), nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()

				// In WAL mode, if we start a transaction and run a SELECT followed by an INSERT, SQLite will
				// immediately report a locked database error if there is already another write transaction.
				// As we know that we are going to insert data, let's always start the transaction in IMMEDIATE
				// mode. This works around this known bug: https://github.com/mattn/go-sqlite3/issues/400.
				if _, err := tx.ExecContext(context.Background(), "ROLLBACK; BEGIN IMMEDIATE"); err != nil {
					return err
				}

				for _, hit := range hits {
					if err := dbInsertHit(context.Background(), tx, &hit); err != nil {
						return err
					}
				}

				return tx.Commit()
			}()

			if err != nil {
				log.Print(err)
			}
		}

		return nil
	})

	return errgrp.Wait()
}

func dbConnect(path string) (*sql.DB, error) {
	uri := fmt.Sprintf("%s?_foreign_keys=true&_journal=WAL&_synchronous=NORMAL&__secure_delete=true&_busy_timeout=5000", path)

	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	schema, err := dbFs.ReadFile("db/schema.sql")
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(string(schema)); err != nil {
		return nil, err
	}

	languages, err := dbFs.ReadFile("db/languages.sql")
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(string(languages)); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return db, nil
}

func dbInsertHit(ctx context.Context, tx *sql.Tx, hit *Hit) error {
	// User ID
	userId, err := dbInsertUser(ctx, tx, hit.IdentifierCurrent, hit.IdentifierPrevious)
	if err != nil {
		return err
	}

	// Path
	var pathId int64
	row := tx.QueryRowContext(ctx, "SELECT path_id FROM paths WHERE domain = ? AND path = ?", hit.Domain, hit.Path)
	err = row.Scan(&pathId)
	if err != nil {
		if err != sql.ErrNoRows {
			return fmt.Errorf("path select error: %w", err)
		}

		row := tx.QueryRowContext(ctx, "INSERT INTO paths (domain, path) VALUES (?, ?) RETURNING path_id", hit.Domain, hit.Path)
		if err := row.Scan(&pathId); err != nil {
			return fmt.Errorf("path insert error: %w", err)
		}
	}

	// Referrer
	var referrerId sql.NullInt64
	if hit.ReferrerDomain.Valid {
		row := tx.QueryRowContext(ctx, "SELECT referrer_id FROM referrers WHERE domain = ? AND path IS ?", hit.ReferrerDomain, hit.ReferrerPath)
		err := row.Scan(&referrerId)
		if err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("referrer select error: %w", err)
			}

			row := tx.QueryRowContext(ctx, "INSERT INTO referrers (domain, path) VALUES (?, ?) RETURNING referrer_id", hit.ReferrerDomain, hit.ReferrerPath)
			if err := row.Scan(&referrerId); err != nil {
				return fmt.Errorf("referrer insert error: %w", err)
			}
		}
	}

	// User Agent
	userAgentId, err := dbInsertUserAgent(ctx, tx, hit.UserAgent)
	if err != nil {
		return err
	}

	// Language
	var languageId sql.NullInt64
	if hit.Language != "" {
		row := tx.QueryRowContext(ctx, "SELECT language_id FROM languages WHERE iso_639_3 = ?", hit.Language)
		if err := row.Scan(&languageId); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("language select error: %w", err)
		}
	}

	// Location
	locationId, err := dbInsertLocation(ctx, tx, &hit.Location)
	if err != nil {
		return err
	}

	// Display
	var displayId sql.NullInt64
	if hit.ScreenHeight.Valid && hit.ScreenWidth.Valid && hit.PixelRatio.Valid {
		row := tx.QueryRowContext(
			ctx,
			"SELECT display_id FROM displays WHERE screen_height = ? AND screen_width = ? AND pixel_ratio = ?",
			hit.ScreenHeight,
			hit.ScreenWidth,
			hit.PixelRatio,
		)
		err := row.Scan(&displayId)
		if err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("display select error: %w", err)
			}

			row := tx.QueryRowContext(
				ctx,
				"INSERT INTO displays (screen_height, screen_width, pixel_ratio) VALUES (?, ?, ?) RETURNING display_id",
				hit.ScreenHeight,
				hit.ScreenWidth,
				hit.PixelRatio,
			)
			if err := row.Scan(&displayId); err != nil {
				return fmt.Errorf("display insert error: %w", err)
			}
		}
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO hits ( timestamp
			              , event
			              , user_id
			              , user_agent_id
						  , bot
						  , path_id
						  , referrer_id
						  , location_id
						  , language_id
						  , display_id )
		VALUES ( :timestamp
			   , :event
			   , :user_id
			   , :user_agent_id
			   , :bot
			   , :path_id
			   , :referrer_id
			   , :location_id
			   , :language_id
			   , :display_id )`,
		sql.Named("timestamp", hit.Timestamp),
		sql.Named("event", hit.Event),
		sql.Named("user_id", userId),
		sql.Named("user_agent_id", userAgentId),
		sql.Named("bot", hit.Bot),
		sql.Named("path_id", pathId),
		sql.Named("referrer_id", referrerId),
		sql.Named("location_id", locationId),
		sql.Named("language_id", languageId),
		sql.Named("display_id", displayId),
	)
	if err != nil {
		return err
	}

	return nil
}

func dbInsertUser(ctx context.Context, tx *sql.Tx, currentIdentifier []byte, previousIdentifier []byte) (int64, error) {
	var userId int64
	var identifier []byte

	row := tx.QueryRowContext(
		ctx,
		"SELECT user_id, identifier FROM users WHERE identifier = ? OR identifier = ?",
		currentIdentifier,
		previousIdentifier,
	)

	err := row.Scan(&userId, &identifier)
	if err != nil && err != sql.ErrNoRows {
		return userId, err
	}

	if err == sql.ErrNoRows {
		row := tx.QueryRowContext(
			ctx,
			"INSERT INTO users (identifier) VALUES (?) RETURNING user_id",
			currentIdentifier,
		)
		if err := row.Scan(&userId); err != nil {
			return userId, err
		}
	} else if bytes.Equal(identifier, currentIdentifier) {
		_, err := tx.ExecContext(
			ctx,
			"UPDATE users SET last_seen = strftime('%s', 'now') WHERE user_id = ?",
			userId,
		)
		if err != nil {
			return userId, err
		}
	} else if bytes.Equal(identifier, previousIdentifier) {
		_, err := tx.ExecContext(
			ctx,
			"UPDATE users SET identifier = ?, last_seen = strftime('%s', 'now') WHERE user_id = ?",
			currentIdentifier,
			userId,
		)
		if err != nil {
			return userId, err
		}
	} else {
		panic("this should not happen")
	}

	return userId, nil
}

func dbInsertUserAgent(ctx context.Context, tx *sql.Tx, userAgent string) (int64, error) {
	row := tx.QueryRowContext(
		ctx,
		"SELECT user_agent_id FROM user_agents WHERE user_agent = ?",
		userAgent,
	)

	var uaId int64
	err := row.Scan(&uaId)
	if err == nil {
		return uaId, nil
	}

	if err != sql.ErrNoRows {
		return uaId, err
	}

	// User agent does not exist in the database. Let's go and insert it...

	// First extract the browser/OS name and version
	ua := gadget.ParseUA(userAgent)

	var (
		browserName    sql.NullString
		browserVersion sql.NullString
		osName         sql.NullString
		osVersion      sql.NullString
	)

	if ua.BrowserName != "" {
		browserName = sql.NullString{String: ua.BrowserName, Valid: true}
	}
	if ua.BrowserVersion != "" {
		browserVersion = sql.NullString{String: ua.BrowserVersion, Valid: true}
	}
	if ua.OSName != "" {
		osName = sql.NullString{String: ua.OSName, Valid: true}
	}
	if ua.OSVersion != "" {
		osVersion = sql.NullString{String: ua.OSVersion, Valid: true}
	}

	bot := isbot.UserAgent(userAgent)

	// Browsers
	var browserId sql.NullInt64

	if browserName.Valid {
		rowBrowser := tx.QueryRowContext(
			ctx,
			"SELECT browser_id FROM browsers WHERE browser_name = ? AND browser_version IS ?",
			browserName,
			browserVersion,
		)

		if err := rowBrowser.Scan(&browserId); err != nil {
			if err != sql.ErrNoRows {
				return uaId, err
			}

			row := tx.QueryRowContext(
				ctx,
				"INSERT INTO browsers (browser_name, browser_version) VALUES (?, ?) RETURNING browser_id",
				browserName,
				browserVersion,
			)
			if err := row.Scan(&browserId); err != nil {
				return uaId, err
			}
		}
	}

	// Operating systems
	var osId sql.NullInt64

	if osName.Valid {
		rowOS := tx.QueryRowContext(
			ctx,
			"SELECT os_id FROM systems WHERE os_name = ? AND os_version IS ?",
			osName,
			osVersion,
		)

		if err := rowOS.Scan(&osId); err != nil {
			if err != sql.ErrNoRows {
				return uaId, err
			}

			row := tx.QueryRowContext(
				ctx,
				"INSERT INTO systems (os_name, os_version) VALUES (?, ?) RETURNING os_id",
				osName,
				osVersion,
			)
			if err := row.Scan(&osId); err != nil {
				return uaId, err
			}
		}
	}

	// Now insert user agent
	row = tx.QueryRowContext(
		ctx,
		"INSERT INTO user_agents (user_agent, browser_id, os_id, bot) VALUES (?, ?, ?, ?) RETURNING user_agent_id",
		userAgent,
		browserId,
		osId,
		bot,
	)
	if err := row.Scan(&uaId); err != nil {
		return uaId, err
	}

	return uaId, nil
}

func dbInsertLocation(ctx context.Context, tx *sql.Tx, location *Location) (sql.NullInt64, error) {
	if !location.Country.Valid {
		// Unknown location
		return sql.NullInt64{}, nil
	}

	// Get the location or the nearest parent location
	const query = `
	WITH RECURSIVE
		l(location_id, parent_id, country, subdivision, city, postal) AS (
			SELECT location_id, parent_id, country, subdivision, city, postal FROM locations WHERE country = :country
			UNION ALL
			SELECT locations.location_id
				, locations.parent_id
				, CASE WHEN locations.country IS NOT NULL THEN locations.country ELSE l.country END
				, CASE WHEN locations.subdivision IS NOT NULL THEN locations.subdivision ELSE l.subdivision END
				, CASE WHEN locations.city IS NOT NULL THEN locations.city ELSE l.city END
				, CASE WHEN locations.postal IS NOT NULL THEN locations.postal ELSE l.postal END
			FROM locations INNER JOIN l ON locations.parent_id = l.location_id
			WHERE (locations.subdivision IS NULL OR locations.subdivision = :subdivision OR l.subdivision = :subdivision)
			AND   (locations.city IS NULL OR locations.city = :city OR l.city = :city)
			AND   (locations.postal IS NULL OR locations.postal = :postal OR l.postal = :postal)
		)
	SELECT location_id, country, subdivision, city, postal FROM l
	ORDER BY country NULLS LAST
		, subdivision NULLS LAST
		, city NULLS LAST
		, postal NULLS LAST
	LIMIT 1`

	row := tx.QueryRowContext(
		ctx,
		query,
		sql.Named("country", location.Country),
		sql.Named("subdivision", location.Subdivision),
		sql.Named("city", location.City),
		sql.Named("postal", location.Postal),
	)

	var (
		locationId  sql.NullInt64
		country     sql.NullString
		subdivision sql.NullString
		city        sql.NullString
		postal      sql.NullString
	)
	if err := row.Scan(&locationId, &country, &subdivision, &city, &postal); err != nil && err != sql.ErrNoRows {
		return sql.NullInt64{}, err
	}

	// This exact location is already in the database :)
	if location.Country == country && location.Subdivision == subdivision && location.City == city && location.Postal == postal {
		if !locationId.Valid {
			panic("locationId must be valid")
		}
		return locationId, nil
	}

	// We have to insert some or part of the location

	if country != location.Country && location.Country.Valid {
		row := tx.QueryRowContext(ctx, "INSERT INTO locations (country) VALUES (?) RETURNING location_id", location.Country)
		if err := row.Scan(&locationId); err != nil {
			return sql.NullInt64{}, err
		}
	}

	if subdivision != location.Subdivision && location.Subdivision.Valid {
		row := tx.QueryRowContext(ctx, "INSERT INTO locations (parent_id, subdivision) VALUES (?, ?) RETURNING location_id", locationId, location.Subdivision)
		if err := row.Scan(&locationId); err != nil {
			return sql.NullInt64{}, err
		}
	}

	if city != location.City && location.City.Valid {
		row := tx.QueryRowContext(ctx, "INSERT INTO locations (parent_id, city) VALUES (?, ?) RETURNING location_id", locationId, location.City)
		if err := row.Scan(&locationId); err != nil {
			return sql.NullInt64{}, err
		}
	}

	if postal != location.Postal && location.Postal.Valid {
		row := tx.QueryRowContext(ctx, "INSERT INTO locations (parent_id, postal) VALUES (?, ?) RETURNING location_id", locationId, location.Postal)
		if err := row.Scan(&locationId); err != nil {
			return sql.NullInt64{}, err
		}
	}

	if !locationId.Valid {
		panic("locationId must be valid")
	}
	return locationId, nil
}

func dbDeleteExpired(ctx context.Context, deleteSince time.Duration, db *sql.DB) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(
		ctx,
		"UPDATE users SET identifier = NULL WHERE identifier IS NOT NULL AND last_seen + ? < CAST(strftime('%s','now') AS INTEGER)",
		deleteSince.Seconds(),
	)
	if err != nil {
		return 0, err
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}
