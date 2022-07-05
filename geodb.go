package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-isatty"
	"github.com/oschwald/geoip2-golang"
	"github.com/schollz/progressbar/v3"
)

const geoLite2DownloadUrl = "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"

func newClient() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	return client
}

type GeoIP struct {
	sync.RWMutex
	reader *geoip2.Reader
	path   string
	etag   string
}

func (geoip *GeoIP) Load() error {
	return geoip.Update()
}

// Update GeoLite2 databases from https://github.com/P3TERX/GeoLite.mmdb
func (geoip *GeoIP) Update() error {
	client := newClient()

	req, err := retryablehttp.NewRequest("GET", geoLite2DownloadUrl, nil)
	if err != nil {
		return err
	}

	if geoip.etag != "" {
		req.Header.Set("If-None-Match", geoip.etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	if geoip.etag != "" && resp.StatusCode == http.StatusNotModified {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error: %s", resp.Status)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		return fmt.Errorf("GeoIp update: no etag")
	}

	f, err := os.CreateTemp(os.TempDir(), "*.mmdb")
	if err != nil {
		return err
	}

	defer f.Close()
	defer resp.Body.Close()

	cleanupTmpFile := func() {
		if err := f.Close(); err != nil {
			log.Printf("cannot close temporary file: %s", err)
		}
		if err := os.Remove(f.Name()); err != nil {
			log.Printf("cannot remove temporary file: %s", err)
		}
	}

	log.Print("Downloading GeoIP database")

	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		bar := progressbar.DefaultBytes(resp.ContentLength, "")
		_, err = io.Copy(io.MultiWriter(f, bar), resp.Body)
	} else {
		_, err = io.Copy(f, resp.Body)
	}

	if err != nil {
		cleanupTmpFile()
		return fmt.Errorf("download failed: %s", err)
	}

	err = f.Close()
	if err != nil {
		cleanupTmpFile()
		return err
	}

	reader, err := geoip2.Open(f.Name())
	if err != nil {
		cleanupTmpFile()
		return err
	}

	// Switch GeoIp database
	geoip.Lock()
	previousReader := geoip.reader
	previousPath := geoip.path
	geoip.reader = reader
	geoip.path = f.Name()
	geoip.etag = etag
	geoip.Unlock()

	// Remove previous GeoIp database if it exists
	if previousReader != nil {
		err = previousReader.Close()
		if err != nil {
			return err
		}
	}

	err = os.Remove(previousPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func (geoip *GeoIP) City(ipAddress net.IP) (*geoip2.City, error) {
	geoip.RLock()
	defer geoip.RUnlock()

	return geoip.reader.City(ipAddress)
}

func (geoip *GeoIP) MarshalJSON() ([]byte, error) {
	geoip.RLock()
	defer geoip.RUnlock()

	tmp := struct {
		Path string `json:"path"`
		ETag string `json:"etag"`
	}{
		Path: geoip.path,
		ETag: geoip.etag,
	}

	return json.Marshal(tmp)
}

func (geoip *GeoIP) UnmarshalJSON(b []byte) error {
	var tmp struct {
		Path string `json:"path"`
		ETag string `json:"etag"`
	}

	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}

	geoip.path = tmp.Path
	geoip.etag = tmp.ETag

	return nil
}

func (geoip *GeoIP) Close() error {
	geoip.Lock()
	defer geoip.Unlock()
	return geoip.reader.Close()
}
