package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/blake2b"
)

const blakeSize128 = 16

type Identifier [16]byte
type JsHash [16]byte

// We want the ETag to be 384 bits or 48 bytes
// BLAKE2b-128 MAC   128 bits = 16 bytes
// Identifier        128 bits = 16 bytes
// Trunc. JS hash    128 bits = 16 bytes

func decodeETag(etag string, key []byte) (ident Identifier, jsHash JsHash, err error) {
	// Check ETag has quotation marks and remove them if so
	et := []byte(etag)
	if !(bytes.HasPrefix(et, []byte{'"'}) && bytes.HasSuffix(et, []byte{'"'})) {
		err = fmt.Errorf("etag is not quoted")
		return
	}
	et = et[1 : len(et)-1]

	if len(et) != hex.EncodedLen(48) {
		err = fmt.Errorf("invalid length %d", len(et))
		return
	}

	var decoded [48]byte
	_, err = hex.Decode(decoded[:], et)
	if err != nil {
		return
	}

	if !validHMAC(decoded[16:], decoded[:16], key) {
		err = fmt.Errorf("HMAC failed")
		return
	}

	copy(ident[:], decoded[16:16+16])
	copy(jsHash[:], decoded[16+16:])

	return
}

func encodeETag(key []byte, ident Identifier, jsHash JsHash) string {
	var etag [48]byte

	copy(etag[16:16+16], ident[:])
	copy(etag[16+16:], jsHash[:])

	hasher, err := blake2b.New(blakeSize128, key)
	if err != nil {
		panic(err)
	}
	hasher.Write(etag[16:])
	mac := hasher.Sum(nil)

	copy(etag[:16], mac)

	return fmt.Sprintf(`"%s"`, hex.EncodeToString(etag[:]))
}

// The auth token is structured as follows:
// BLAKE2b-128 MAC   128 bits = 16 bytes
// Identifier        128 bits = 16 bytes
// -------------------------------------
// Total             256 bits = 32 bytes

func encodeToken(key []byte, ident Identifier) string {
	var token [32]byte

	hasher, err := blake2b.New(blakeSize128, key)
	if err != nil {
		panic(err)
	}
	hasher.Write(ident[:])
	mac := hasher.Sum(nil)

	copy(token[:16], mac)
	copy(token[16:], ident[:])

	return base64.StdEncoding.EncodeToString(token[:])
}

func decodeToken(token string, key []byte) (ident Identifier, err error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return
	}

	if len(decoded) != 32 {
		err = fmt.Errorf("invalid length %d", len(decoded))
		return
	}

	if !validHMAC(decoded[16:], decoded[:16], key) {
		err = fmt.Errorf("HMAC failed")
		return
	}

	copy(ident[:], decoded[16:])

	return
}

func validHMAC(message, messageMAC, key []byte) bool {
	hasher, err := blake2b.New(blakeSize128, key)
	if err != nil {
		panic(err)
	}
	hasher.Write(message)
	expectedMAC := hasher.Sum(nil)

	return subtle.ConstantTimeCompare(messageMAC, expectedMAC) == 1
}
