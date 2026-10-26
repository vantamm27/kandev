package hmacauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("seat-secret")
	now    = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
)

func signedRequest(t *testing.T, body string, at time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/heartbeat", strings.NewReader(body))
	SetHeaders(req, "seat_1", secret, []byte(body), at)
	return req
}

func TestRoundTrip(t *testing.T) {
	req := signedRequest(t, `{"free_slots":1}`, now)
	signed, err := Parse(req.Header)
	if err != nil || signed.SeatID != "seat_1" || signed.Timestamp != now.Unix() {
		t.Fatalf("parse = %+v, %v", signed, err)
	}
	if err := signed.Verify(secret, http.MethodPost, "/v1/heartbeat", []byte(`{"free_slots":1}`), now.Add(time.Minute)); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	signed, err := Parse(signedRequest(t, `{"free_slots":1}`, now).Header)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		secret []byte
		method string
		path   string
		body   string
		at     time.Time
		want   error
	}{
		"other body":   {secret, http.MethodPost, "/v1/heartbeat", `{"free_slots":9}`, now, ErrBadSignature},
		"other path":   {secret, http.MethodPost, "/v1/register", `{"free_slots":1}`, now, ErrBadSignature},
		"other method": {secret, http.MethodPut, "/v1/heartbeat", `{"free_slots":1}`, now, ErrBadSignature},
		"other secret": {[]byte("nope"), http.MethodPost, "/v1/heartbeat", `{"free_slots":1}`, now, ErrBadSignature},
		"too old":      {secret, http.MethodPost, "/v1/heartbeat", `{"free_slots":1}`, now.Add(MaxSkew + time.Second), ErrBadTimestamp},
		"from future":  {secret, http.MethodPost, "/v1/heartbeat", `{"free_slots":1}`, now.Add(-MaxSkew - time.Second), ErrBadTimestamp},
	} {
		t.Run(name, func(t *testing.T) {
			if err := signed.Verify(tc.secret, tc.method, tc.path, []byte(tc.body), tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	signed.Signature = "not-hex"
	if err := signed.Verify(secret, http.MethodPost, "/v1/heartbeat", []byte(`{"free_slots":1}`), now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("non-hex signature = %v", err)
	}
}

func TestParseRequiresHeaders(t *testing.T) {
	h := http.Header{}
	if _, err := Parse(h); !errors.Is(err, ErrMissingHeaders) {
		t.Fatalf("empty = %v", err)
	}
	h.Set(HeaderSeat, "seat_1")
	h.Set(HeaderSignature, "ab")
	h.Set(HeaderTimestamp, "yesterday")
	if _, err := Parse(h); !errors.Is(err, ErrBadTimestamp) {
		t.Fatalf("bad timestamp = %v", err)
	}
}
