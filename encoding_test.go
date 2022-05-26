package main

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestETagRoundTrip(t *testing.T) {
	key := []byte("VERY SECRET KEY")

	var identIn Identifier
	if _, err := rand.Read(identIn[:]); err != nil {
		t.Fatal(err)
	}

	var jsHashIn JsHash
	if _, err := rand.Read(jsHashIn[:]); err != nil {
		t.Fatal(err)
	}

	etag := encodeETag(key, identIn, jsHashIn)
	identOut, jsHashOut, err := decodeETag(etag, key)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, identIn, identOut)
	assert.Equal(t, jsHashIn, jsHashOut)
}

func TestTokenRoundTrip(t *testing.T) {
	key := []byte("VERY SECRET KEY")

	var identIn Identifier
	if _, err := rand.Read(identIn[:]); err != nil {
		t.Fatal(err)
	}

	token := encodeToken(key, identIn)
	identOut, err := decodeToken(token, key)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, identIn, identOut)
}
