package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterPostsAndDecodes(t *testing.T) {
	var got RegisterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/register" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"seat_id":"seat_1","hmac_secret":"s3cret","kandev_executor_id":"exec-1"}`)
	}))
	defer srv.Close()
	res, err := Register(context.Background(), NewHTTPClient(), srv.URL+"/", RegisterRequest{Token: "tok", SSHHost: "mini-1", SSHUser: "seat-a", Capacity: 2})
	if err != nil || res.SeatID != "seat_1" || res.HMACSecret != "s3cret" {
		t.Fatalf("Register = %+v, %v", res, err)
	}
	if got.Token != "tok" || got.SSHUser != "seat-a" || got.Capacity != 2 {
		t.Fatalf("sent %+v", got)
	}
}

func TestRegisterSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"registration token is invalid, expired, or already used"}`)
	}))
	defer srv.Close()
	_, err := Register(context.Background(), NewHTTPClient(), srv.URL, RegisterRequest{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401: registration token is invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestLaunchAgentPlistIsWellFormedAndEscaped(t *testing.T) {
	raw, err := LaunchAgentPlist("/usr/local/bin/fleet-agent", "/Users/seat-a/.config/fleet-agent/config.json", "/Users/seat-a/Library/Logs/fleet&agent.log")
	if err != nil {
		t.Fatalf("plist: %v", err)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, raw)
		}
	}
	s := string(raw)
	for _, want := range []string{
		"<string>com.kandev.fleet-agent</string>", "<string>run</string>", "<string>--config</string>",
		"fleet&amp;agent.log", "<key>KeepAlive</key>", "<key>RunAtLoad</key>",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("plist missing %q:\n%s", want, s)
		}
	}
}
