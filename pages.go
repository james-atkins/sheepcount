package main

import (
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/securecookie"
	"golang.org/x/crypto/argon2"
)

const authCookieName = "auth"

type authCookie struct {
	LoggedIn        bool `json:"l"`
	InvalidPassword bool `json:"msg_invalid_password,omitempty"`
	JustLoggedOut   bool `json:"msg_logged_out,omitempty"`
}

func getAuthCookie(r *http.Request, key string) authCookie {
	var value authCookie

	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return value
	}

	sc := securecookie.New([]byte(key), nil)
	sc.SetSerializer(securecookie.JSONEncoder{})

	if err := sc.Decode(authCookieName, cookie.Value, &value); err != nil {
		return value
	}

	return value
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

	token := getAuthCookie(r, sheepcount.CookieKey)

	w.Header().Add("Content-Type", "text/html; charset=UTF-8")

	if token.LoggedIn {
		if err := sheepcount.tmpl.ExecuteTemplate(w, "app.html.tmpl", nil); err != nil {
			log.Print(err)
		}
		return
	}

	// Rudimentary flash message - just show once
	if token.InvalidPassword || token.JustLoggedOut {
		var token authCookie

		sc := securecookie.New([]byte(sheepcount.CookieKey), nil)
		sc.SetSerializer(securecookie.JSONEncoder{})

		encoded, err := sc.Encode(authCookieName, token)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		cookie := http.Cookie{
			Name:     authCookieName,
			Value:    encoded,
			Path:     "/",
			HttpOnly: true,
		}

		http.SetCookie(w, &cookie)
	}

	params := struct {
		ShowAbout       bool
		InvalidPassword bool
		JustLoggedOut   bool
	}{
		ShowAbout:       true,
		InvalidPassword: token.InvalidPassword,
		JustLoggedOut:   token.JustLoggedOut,
	}
	if err := sheepcount.tmpl.ExecuteTemplate(w, "home.html.tmpl", params); err != nil {
		log.Print(err)
		return
	}
}

func handleLogin(sheepcount *SheepCount, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// CSRF mitigation by checking origin

	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "Invalid origin")
		return
	}

	if origin.Host != sheepcount.getHost(r) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "Invalid origin")
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	password := r.Form.Get("password")
	key := hex.EncodeToString(argon2.IDKey([]byte(password), []byte(sheepcount.CookieKey), 1, 64*1024, 4, 32))

	var value authCookie

	if subtle.ConstantTimeCompare([]byte(key), []byte(sheepcount.Password)) == 1 {
		value.LoggedIn = true
	} else {
		value.InvalidPassword = true
	}

	sc := securecookie.New([]byte(sheepcount.CookieKey), nil)
	sc.SetSerializer(securecookie.JSONEncoder{})

	encoded, err := sc.Encode(authCookieName, value)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	cookie := http.Cookie{
		Name:     authCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
	}

	http.SetCookie(w, &cookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

func handleLogout(sheepcount *SheepCount, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/logout" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	token := getAuthCookie(r, sheepcount.CookieKey)

	if token.LoggedIn {
		sc := securecookie.New([]byte(sheepcount.CookieKey), nil)
		sc.SetSerializer(securecookie.JSONEncoder{})

		authCookie := authCookie{JustLoggedOut: true}

		encoded, err := sc.Encode(authCookieName, authCookie)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		cookie := http.Cookie{
			Name:     authCookieName,
			Value:    encoded,
			Path:     "/",
			HttpOnly: true,
		}
		http.SetCookie(w, &cookie)
	}

	http.Redirect(w, r, "/", http.StatusFound)
}
