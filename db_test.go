package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInsertLocation(t *testing.T) {
	db, err := dbConnect(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO locations (country) VALUES ('GB');                          -- 1
		 INSERT INTO locations (parent_id, subdivision) VALUES (1, 'ENG');       -- 2
		 INSERT INTO locations (parent_id, subdivision) VALUES (1, 'SCT');       -- 3
		 INSERT INTO locations (parent_id, city) VALUES (2, 'London');           -- 4
		 INSERT INTO locations (parent_id, postal) VALUES (4, 'SW1');            -- 5
		 INSERT INTO locations (parent_id, postal) VALUES (4, 'SW19');           -- 6
		 INSERT INTO locations (parent_id, city) VALUES (3, 'Edinburgh');        -- 7
		 INSERT INTO locations (parent_id, postal) VALUES (7, 'EH1');            -- 8
		 INSERT INTO locations (parent_id, postal) VALUES (7, 'EH8');            -- 9
		 INSERT INTO locations (country) VALUES ('CA');                          -- 10
		 INSERT INTO locations (parent_id, subdivision) VALUES (10, 'Ontario');  -- 11
		 INSERT INTO locations (parent_id, city) VALUES (11, 'London');          -- 12
		 INSERT INTO locations (country) VALUES ('AI');                          -- 13
		 INSERT INTO locations (parent_id, city) VALUES (13, 'The Valley');      -- 14
		`,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Helper functions for running tests
	location := func(country, subdivision, city, postal string) *Location {
		var l Location
		if country != "" {
			l.Country = sql.NullString{String: country, Valid: true}
		}

		if subdivision != "" {
			l.Subdivision = sql.NullString{String: subdivision, Valid: true}
		}

		if city != "" {
			l.City = sql.NullString{String: city, Valid: true}
		}

		if postal != "" {
			l.Postal = sql.NullString{String: postal, Valid: true}
		}

		return &l
	}

	getOrInsertId := func(location *Location) sql.NullInt64 {
		id, err := dbInsertLocation(ctx, tx, location)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	validId := func(id int) sql.NullInt64 {
		return sql.NullInt64{Int64: int64(id), Valid: true}
	}

	assert.Equal(t, sql.NullInt64{}, getOrInsertId(&Location{}))

	// Get existing locations
	assert.Equal(t, validId(1), getOrInsertId(location("GB", "", "", "")))
	assert.Equal(t, validId(2), getOrInsertId(location("GB", "ENG", "", "")))
	assert.Equal(t, validId(3), getOrInsertId(location("GB", "SCT", "", "")))
	assert.Equal(t, validId(4), getOrInsertId(location("GB", "ENG", "London", "")))
	assert.Equal(t, validId(5), getOrInsertId(location("GB", "ENG", "London", "SW1")))
	assert.Equal(t, validId(12), getOrInsertId(location("CA", "Ontario", "London", "")))
	assert.Equal(t, validId(14), getOrInsertId(location("AI", "", "The Valley", "")))

	// Now let's insert some new locations

	// INSERT INTO locations (parent_id, subdivision) VALUES (1, 'NIR');       -- 15
	assert.Equal(t, validId(15), getOrInsertId(location("GB", "NIR", "", "")))

	// INSERT INTO locations (parent_id, city) VALUES (15, 'Belfast');         -- 16
	// INSERT INTO locations (parent_id, postal) VALUES (16, 'BT4');           -- 17
	assert.Equal(t, validId(17), getOrInsertId(location("GB", "NIR", "Belfast", "BT4")))

	assert.Equal(t, validId(16), getOrInsertId(location("GB", "NIR", "Belfast", "")))

	// INSERT INTO locations (parent_id, postal) VALUES (16, 'BT15');          -- 18
	assert.Equal(t, validId(18), getOrInsertId(location("GB", "NIR", "Belfast", "BT15")))

	// INSERT INTO locations (country) VALUES ('US');                          -- 19
	// INSERT INTO locations (parent_id, subdivision) VALUES (19, 'IL');       -- 20
	// INSERT INTO locations (parent_id, city) VALUES (20, 'Chicago');         -- 21
	// INSERT INTO locations (parent_id, postal) VALUES (21, '60208');         -- 22
	assert.Equal(t, validId(22), getOrInsertId(location("US", "IL", "Chicago", "60208")))

	assert.Equal(t, validId(19), getOrInsertId(location("US", "", "", "")))
	assert.Equal(t, validId(20), getOrInsertId(location("US", "IL", "", "")))
	assert.Equal(t, validId(21), getOrInsertId(location("US", "IL", "Chicago", "")))

	// INSERT INTO locations (parent_id, city) VALUES (20, 'Springfield');     -- 23
	assert.Equal(t, validId(23), getOrInsertId(location("US", "IL", "Springfield", "")))

	// INSERT INTO locations (parent_id, postal) VALUES (23, 'Springfield');   -- 24
	assert.Equal(t, validId(24), getOrInsertId(location("US", "IL", "Springfield", "62701")))

	// INSERT INTO locations (parent_id, city) VALUES (13, 'White Hill');      -- 25
	assert.Equal(t, validId(25), getOrInsertId(location("AI", "", "White Hill", "")))

	// INSERT INTO locations (country) VALUES ('FR');                          -- 26
	assert.Equal(t, validId(26), getOrInsertId(location("FR", "", "", "")))

	// INSERT INTO locations (parent_id, subdivision) VALUES (26, 'IDF');      -- 27
	// INSERT INTO locations (parent_id, city) VALUES (27, 'Paris');           -- 28
	assert.Equal(t, validId(28), getOrInsertId(location("FR", "IDF", "Paris", "")))
	assert.Equal(t, validId(27), getOrInsertId(location("FR", "IDF", "", "")))
}
