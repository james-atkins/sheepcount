PRAGMA foreign_keys = ON;
PRAGMA secure_delete = ON;

-- Represents an unique user identified by either a persistent identifier (generally frowned upon)
-- or an pseudo-anonymised identifier such as a cryptographic hash of the user-agent and IP address.
-- The identifier is cleared after some time to anonomise users and save space.
CREATE TABLE IF NOT EXISTS users (
    user_id    INTEGER PRIMARY KEY,
    identifier BLOB UNIQUE,
    first_seen INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    last_seen  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;


CREATE TABLE IF NOT EXISTS paths (
    path_id INTEGER PRIMARY KEY,
    domain  TEXT NOT NULL CHECK(domain != ''),
    path    TEXT NOT NULL CHECK(path != '')
) STRICT;


CREATE TABLE IF NOT EXISTS referrers (
    referrer_id INTEGER PRIMARY KEY,
    domain      TEXT NOT NULL CHECK(domain != ''),
    path        TEXT CHECK(path != '')
) STRICT;


CREATE TABLE IF NOT EXISTS browsers (
    browser_id      INTEGER PRIMARY KEY,
    browser_name    TEXT CHECK(browser_name != ''),
    browser_version TEXT CHECK(browser_version != '')
) STRICT;


CREATE TABLE IF NOT EXISTS systems (
    os_id      INTEGER PRIMARY KEY,
    os_name    TEXT CHECK(os_name != ''),
    os_version TEXT CHECK(os_version != '')
) STRICT;


CREATE TABLE IF NOT EXISTS user_agents (
    user_agent_id INTEGER PRIMARY KEY,
    user_agent    TEXT NOT NULL UNIQUE,
    browser_id    INTEGER REFERENCES browsers(browser_id),
    os_id         INTEGER REFERENCES systems(os_id),
    bot           INTEGER NOT NULL
) STRICT;


CREATE TABLE IF NOT EXISTS languages (
    language_id INTEGER PRIMARY KEY,
    iso_639_3   TEXT NOT NULL UNIQUE CHECK(length(iso_639_3) = 3),
    name        TEXT NOT NULL
) STRICT;


CREATE TABLE IF NOT EXISTS displays (
    display_id    INTEGER PRIMARY KEY,
    screen_height INTEGER,
    screen_width  INTEGER,
    pixel_ratio   REAL,
    UNIQUE(screen_height, screen_width, pixel_ratio)
) STRICT;


CREATE TABLE IF NOT EXISTS locations (
    location_id INTEGER PRIMARY KEY,
    parent_id   INTEGER REFERENCES locations(location_id),
    country     TEXT CHECK(country != ''),
    subdivision TEXT CHECK(subdivision != ''),
    city        TEXT CHECK(city != ''),
    postal      TEXT CHECK(postal != ''),
    name        TEXT GENERATED ALWAYS AS (
        CASE
            WHEN country IS NOT NULL THEN country
            WHEN subdivision IS NOT NULL THEN subdivision
            WHEN city IS NOT NULL THEN city
            WHEN postal IS NOT NULL THEN postal
            ELSE NULL
        END
    ) VIRTUAL,

    CHECK(location_id != parent_id),
    
    -- A country has no parent but every other location must have a parent
    CHECK(CAST(parent_id IS NOT NULL AS INTEGER) + CAST(country IS NOT NULL AS INTEGER) = 1),

    -- Every location can only be one of a country, subdivision, city or postal
    CHECK(CAST(country IS NOT NULL AS INTEGER) + CAST(subdivision IS NOT NULL AS INTEGER) + CAST(city IS NOT NULL AS INTEGER) + CAST(postal IS NOT NULL AS INTEGER) = 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_country ON locations (country) WHERE country IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_subdivision ON locations (parent_id, subdivision) WHERE subdivision IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_city ON locations (parent_id, city) WHERE city IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_postal ON locations (parent_id, postal) WHERE postal IS NOT NULL;

CREATE TRIGGER IF NOT EXISTS valid_location_parent
AFTER INSERT ON locations
BEGIN
    SELECT CASE
        WHEN
            NEW.subdivision IS NOT NULL AND (SELECT country IS NULL FROM locations WHERE location_id = NEW.parent_id)
        THEN
            RAISE(ABORT, 'subdivision without a country parent')
        
        WHEN
            NEW.city IS NOT NULL AND (SELECT country IS NULL AND subdivision IS NULL FROM locations WHERE location_id = NEW.parent_id)
        THEN
            RAISE(ABORT, 'city without a country or subdivision parent')

        WHEN
            NEW.postal IS NOT NULL AND (SELECT city IS NULL FROM locations WHERE location_id = NEW.parent_id)
        THEN
            RAISE(ABORT, 'postal without a city parent')
    END;
END;


CREATE TABLE IF NOT EXISTS hits (
    hit_id        INTEGER PRIMARY KEY,
    timestamp     INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),

    event         TEXT NOT NULL,
    user_id       INTEGER NOT NULL REFERENCES users(user_id),
    user_agent_id INTEGER NOT NULL REFERENCES user_agents(user_agent_id),
    bot           INTEGER,  -- E.g. a botty IP address range or selenium
    location_id   INTEGER REFERENCES locations(location_id),
    language_id   INTEGER REFERENCES languages(language_id),
    
    path_id       INTEGER NOT NULL REFERENCES paths(path_id),
    referrer_id   INTEGER REFERENCES referrers(referrer_id),
    display_id    INTEGER REFERENCES displays(display_id)
) STRICT;
