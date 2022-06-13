package main

import (
	"log"
	"net"
	"net/http"
)

var xRealIPHeader = http.CanonicalHeaderKey("X-Real-IP")

// Middleware to set RemoteAddr to the IP address of whoever sent the request or reply with 500 error.
func ipAddress(reverseProxy bool, next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		var ip net.IP
		if reverseProxy {
			if xrip := r.Header.Get(xRealIPHeader); xrip != "" {
				ip = net.ParseIP(xrip)
				if ip == nil {
					log.Printf("X-Real-IP' %s' is not valid", xrip)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
		} else {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				log.Printf("cannot get IP address from %s", r.RemoteAddr)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			ip = net.ParseIP(host)
			if ip == nil {
				log.Printf("remote address '%s' is not valid", host)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}

		r.RemoteAddr = ip.String()
		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}

// Middleware to log and recover any panics.
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
