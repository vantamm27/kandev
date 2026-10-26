// Package hmacauth signs and verifies fleet-agent requests with the per-seat
// secret issued at registration.
//
// The signature is hex(HMAC-SHA256(secret, canonical)) where canonical is
//
//	<unix seconds>\n<METHOD>\n<path>\n<hex sha256 of body>
//
// sent with the seat ID and timestamp headers below.
package hmacauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Request headers.
const (
	HeaderSeat      = "X-Fleet-Seat"
	HeaderTimestamp = "X-Fleet-Timestamp"
	HeaderSignature = "X-Fleet-Signature"
)

// MaxSkew is how far a request timestamp may differ from the server clock.
const MaxSkew = 5 * time.Minute

// Verification errors.
var (
	ErrMissingHeaders = errors.New("missing fleet signature headers")
	ErrBadTimestamp   = errors.New("request timestamp is malformed or outside the allowed clock skew")
	ErrBadSignature   = errors.New("request signature does not match")
)

// Sign returns the signature for one request.
func Sign(secret []byte, unixSeconds int64, method, path string, body []byte) string {
	bodySum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(unixSeconds, 10) + "\n" + method + "\n" + path + "\n" + hex.EncodeToString(bodySum[:])))
	return hex.EncodeToString(mac.Sum(nil))
}

// SetHeaders signs req (whose body is body) for seatID at now.
func SetHeaders(req *http.Request, seatID string, secret, body []byte, now time.Time) {
	ts := now.Unix()
	req.Header.Set(HeaderSeat, seatID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(secret, ts, req.Method, req.URL.Path, body))
}

// Signed is a request's claimed identity, read before the secret is known.
type Signed struct {
	SeatID    string
	Timestamp int64
	Signature string
}

// Parse reads the signature headers.
func Parse(h http.Header) (Signed, error) {
	s := Signed{SeatID: h.Get(HeaderSeat), Signature: h.Get(HeaderSignature)}
	raw := h.Get(HeaderTimestamp)
	if s.SeatID == "" || s.Signature == "" || raw == "" {
		return Signed{}, ErrMissingHeaders
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return Signed{}, ErrBadTimestamp
	}
	s.Timestamp = ts
	return s, nil
}

// Verify checks the timestamp window and the signature in constant time.
func (s Signed) Verify(secret []byte, method, path string, body []byte, now time.Time) error {
	skew := now.Sub(time.Unix(s.Timestamp, 0))
	if skew > MaxSkew || skew < -MaxSkew {
		return ErrBadTimestamp
	}
	want, err := hex.DecodeString(Sign(secret, s.Timestamp, method, path, body))
	if err != nil {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(s.Signature)
	if err != nil || !hmac.Equal(got, want) {
		return ErrBadSignature
	}
	return nil
}
