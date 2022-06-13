package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oschwald/geoip2-golang"
	"golang.org/x/text/language"
	"zgo.at/isbot"
)

type EventType string

const (
	PageLoad EventType = "l"
	PageView EventType = "v"
	PageHide EventType = "h"
)

func (e *EventType) UnmarshalJSON(src []byte) error {
	var event string
	if err := json.Unmarshal(src, &event); err != nil {
		return err
	}
	if len(event) != 1 {
		return fmt.Errorf("invalid event: %s", event)
	}

	switch event {
	case string(PageLoad):
		*e = PageLoad
	case string(PageView):
		*e = PageView
	case string(PageHide):
		*e = PageHide
	default:
		return fmt.Errorf("unknown event: %v", event)
	}

	return nil
}

type Event struct {
	Token        string    `json:"t"`
	Event        EventType `json:"e"`
	Url          string    `json:"u"`
	Referrer     string    `json:"r"`
	JsBot        int       `json:"b"`
	ScreenHeight int32     `json:"h"`
	ScreenWidth  int32     `json:"w"`
	PixelRatio   float64   `json:"p"`
}

// Unnormalised data
type Hit struct {
	Timestamp          int64
	IdentifierCurrent  []byte
	IdentifierPrevious []byte
	UserAgent          string
	Bot                sql.NullInt16

	Event EventType

	Language string

	Location

	Domain         string
	Path           string
	ReferrerDomain sql.NullString
	ReferrerPath   sql.NullString

	ScreenHeight sql.NullInt32
	ScreenWidth  sql.NullInt32
	PixelRatio   sql.NullFloat64
}

type Location struct {
	Country     sql.NullString // The ISO code of the country
	Subdivision sql.NullString
	City        sql.NullString
	Postal      sql.NullString
}

func NewHit(sheepcount *SheepCount, r *http.Request) (Hit, Error) {
	var hit Hit
	hit.Timestamp = time.Now().Unix()

	var event Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		return hit, BadInput(err)
	}

	identCurrent, identPrevious, err := sheepcount.fingerprintRequest(r)
	if err != nil {
		return hit, err
	}
	hit.IdentifierCurrent = identCurrent
	hit.IdentifierPrevious = identPrevious

	if err := hit.fromRequest(sheepcount, r); err != nil {
		return hit, err
	}

	if err := hit.fromEvent(sheepcount, &event); err != nil {
		return hit, err
	}

	return hit, nil
}

func (hit *Hit) fromRequest(sheepcount *SheepCount, r *http.Request) Error {
	hit.UserAgent = r.Header.Get("User-Agent")

	// Language
	tags, _, _ := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if len(tags) > 0 {
		base, c := tags[0].Base()
		if c == language.Exact || c == language.High {
			hit.Language = base.ISO3()
		}
	}

	// Is this considered a bot because of the IP range?
	if bot := isbot.IPRange(r.RemoteAddr); isbot.Is(bot) {
		hit.Bot = sql.NullInt16{Int16: int16(bot), Valid: true}
	}

	if err := hit.setLocation(sheepcount.geo, net.ParseIP(r.RemoteAddr)); err != nil {
		return err
	}

	return nil
}

func (hit *Hit) fromEvent(sheepcount *SheepCount, event *Event) Error {
	// Event
	hit.Event = event.Event

	// Page and referrer URL
	if err := hit.setPageAndReferrer(sheepcount, event.Url, event.Referrer); err != nil {
		return err
	}

	// JS bot
	if bot := event.JsBot; bot >= 150 {
		if !hit.Bot.Valid || (hit.Bot.Valid && isbot.IsNot(isbot.Result(bot))) {
			hit.Bot = sql.NullInt16{Int16: int16(bot), Valid: true}
		}
	}

	// Display
	if event.ScreenHeight > 0 {
		hit.ScreenHeight = sql.NullInt32{Int32: event.ScreenHeight, Valid: true}
	} else {
		return BadInput(fmt.Errorf("invalid screen height: %d", event.ScreenHeight))
	}

	if event.ScreenWidth > 0 {
		hit.ScreenWidth = sql.NullInt32{Int32: event.ScreenWidth, Valid: true}
	} else {
		return BadInput(fmt.Errorf("invalid screen width: %d", event.ScreenWidth))
	}

	if event.PixelRatio > 0 {
		hit.PixelRatio = sql.NullFloat64{Float64: event.PixelRatio, Valid: true}
	} else {
		return BadInput(fmt.Errorf("invalid pixel ratio: %f", event.PixelRatio))
	}

	return nil
}

func (hit *Hit) setLocation(db *geoip2.Reader, ip net.IP) Error {
	record, err := db.City(ip)
	if err != nil {
		return NewInternalError(fmt.Errorf("geoip2 error: %w", err))
	}

	if country := record.Country.IsoCode; country != "" {
		hit.Country = sql.NullString{String: country, Valid: true}
	} else {
		// Can't have subdivisions, city and postal without country
		return nil
	}

	// Maxmind can provide multiple levels of country subdivision, for example for the UK where it
	// might provide England and then the shire county. But I don't think this is available using
	// the free GeoLite2 databases. So just grab the first subdivision if it is available.
	if len(record.Subdivisions) > 0 {
		if subdivision := record.Subdivisions[0].IsoCode; subdivision != "" {
			hit.Subdivision = sql.NullString{String: subdivision, Valid: true}
		}
	}

	if city := record.City.Names["en"]; city != "" {
		hit.City = sql.NullString{String: city, Valid: true}
	} else {
		// Can't have postal without city
		return nil
	}

	if postal := record.Postal.Code; postal != "" {
		hit.Postal = sql.NullString{String: postal, Valid: true}
	}

	return nil
}

func (hit *Hit) setPageAndReferrer(sheepcount *SheepCount, pageUrl string, referrerUrl string) Error {
	pu, err := url.Parse(pageUrl)
	if err != nil {
		return BadInput(err)
	}

	domain := strings.ToLower(pu.Hostname())

	if sheepcount.AllowLocalhost {
		if domain == "localhost" || domain == "127.0.0.1" {
			hit.Domain = domain
		}
	} else {
		for _, allowedDomain := range sheepcount.Domains {
			if domain == allowedDomain {
				hit.Domain = domain
				break
			}
		}
	}
	if hit.Domain == "" {
		return BadInput(fmt.Errorf("invalid domain: %s", domain))
	}

	if pu.Path == "" {
		return BadInput(fmt.Errorf("invalid path"))
	}
	hit.Path = pu.Path

	if referrerUrl == "" {
		return nil
	}

	ru, err := url.Parse(referrerUrl)
	if err != nil {
		return BadInput(err)
	}

	if referrerDomain := strings.ToLower(ru.Hostname()); referrerDomain == "" {
		return BadInput(fmt.Errorf("invalid referrer: no domain"))
	} else {
		hit.ReferrerDomain = sql.NullString{String: referrerDomain, Valid: true}
	}

	if ru.Path == "" {
		return BadInput(fmt.Errorf("invalid referrer: no path"))
	}

	// Cross-domain referrers are generally anonomised by browsers. But if we see a referrer with a
	// path or with query parameters, then we know this is not the case.
	// Assume that own-domain referrers are not anonomised.
	if hit.ReferrerDomain.String == hit.Domain || ru.Path != "/" || ru.RawQuery != "" {
		path := url.URL{
			Path: ru.Path,
		}

		if ru.RawQuery != "" {
			q := ru.Query()
			stripTrackingTags(q)
			path.RawQuery = q.Encode()
		}

		hit.ReferrerPath = sql.NullString{String: path.String(), Valid: true}
	}

	return nil
}
