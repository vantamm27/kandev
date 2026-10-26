package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

const goodFingerprint = "SHA256:AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AbCdE"

type fakeKandev struct {
	executors        []kandev.CreateExecutorInput
	profiles         []kandev.CreateExecutorProfileInput
	deleted          []string
	executorErr      error
	profileErr       error
	onCreateExecutor func()
}

func (f *fakeKandev) CreateExecutor(_ context.Context, in kandev.CreateExecutorInput) (kandev.Executor, error) {
	f.executors = append(f.executors, in)
	if f.onCreateExecutor != nil {
		f.onCreateExecutor()
	}
	if f.executorErr != nil {
		return kandev.Executor{}, f.executorErr
	}
	return kandev.Executor{ID: "exec-1", Type: in.Type}, nil
}

func (f *fakeKandev) CreateExecutorProfile(_ context.Context, executorID string, in kandev.CreateExecutorProfileInput) (kandev.ExecutorProfile, error) {
	f.profiles = append(f.profiles, in)
	if f.profileErr != nil {
		return kandev.ExecutorProfile{}, f.profileErr
	}
	return kandev.ExecutorProfile{ID: "prof-1", ExecutorID: executorID}, nil
}

func (f *fakeKandev) DeleteExecutor(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T, k *fakeKandev, identity SSHIdentity) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, k, identity, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now }
	if err := st.CreateToken(context.Background(), "tok-1", store.RegistrationToken{
		OwnerUserID: "user-a", Labels: []string{"team:core"}, ExpiresAt: now.Add(time.Hour),
	}, now); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return svc, st
}

func validRequest() Request {
	return Request{
		Token: " tok-1 ", SSHHost: "10.0.0.5", SSHUser: "seat-a",
		HostKeyFingerprint: goodFingerprint, Labels: []string{"macos"}, AgentVersion: "0.1.0",
	}
}

func TestRegisterCreatesFixedExecutorAndSeat(t *testing.T) {
	k := &fakeKandev{}
	svc, st := newService(t, k, SSHIdentity{Source: "file", File: "/etc/kandev/fleet_ed25519"})
	req := validRequest()
	req.Labels = []string{"macos"}

	res, err := svc.Register(context.Background(), req)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !strings.HasPrefix(res.SeatID, "seat_") || len(res.HMACSecret) != 64 || res.OwnerUserID != "user-a" ||
		res.KandevExecutorID != "exec-1" || res.KandevExecutorProfileID != "prof-1" {
		t.Fatalf("result = %+v", res)
	}
	if len(k.executors) != 1 {
		t.Fatalf("executors created = %d", len(k.executors))
	}
	in := k.executors[0]
	want := map[string]string{
		"ssh_host": "10.0.0.5", "ssh_port": "22", "ssh_user": "seat-a", "ssh_host_fingerprint": goodFingerprint,
		"ssh_identity_source": "file", "ssh_identity_file": "/etc/kandev/fleet_ed25519",
	}
	for key, value := range want {
		if in.Config[key] != value {
			t.Fatalf("config[%s] = %q, want %q (config %+v)", key, in.Config[key], value, in.Config)
		}
	}
	if in.Type != "ssh" || in.OwnerUserID != "user-a" || in.Resumable == nil || !*in.Resumable || in.Name != "fleet seat-a@10.0.0.5" {
		t.Fatalf("executor input = %+v", in)
	}
	if len(k.profiles) != 1 || k.profiles[0].Name != res.SeatID || k.profiles[0].OwnerUserID != "user-a" {
		t.Fatalf("profiles = %+v", k.profiles)
	}
	seat, err := st.GetSeat(context.Background(), res.SeatID)
	if err != nil || seat.HMACSecret != res.HMACSecret || seat.Capacity != 1 || seat.State != store.SeatOnline {
		t.Fatalf("stored seat = %+v, %v", seat, err)
	}
	if strings.Join(seat.Labels, ",") != "team:core,macos" {
		t.Fatalf("labels = %v, want admin labels first then node labels", seat.Labels)
	}
	if _, err := svc.Register(context.Background(), validRequest()); !errors.Is(err, store.ErrTokenInvalid) {
		t.Fatalf("second use of the token = %v", err)
	}
}

func TestRegisterValidation(t *testing.T) {
	cases := map[string]func(*Request){
		"missing token":      func(r *Request) { r.Token = " " },
		"bad host":           func(r *Request) { r.SSHHost = "mini 1; rm -rf" },
		"bad port":           func(r *Request) { r.SSHPort = 70000 },
		"bad user":           func(r *Request) { r.SSHUser = "Seat A" },
		"bad fingerprint":    func(r *Request) { r.HostKeyFingerprint = "MD5:aa:bb" },
		"capacity too large": func(r *Request) { r.Capacity = 9 },
		"bad label":          func(r *Request) { r.Labels = []string{"Has Space"} },
		"admin-only label":   func(r *Request) { r.Labels = []string{"team:payments"} },
		"owner label":        func(r *Request) { r.Labels = []string{"owner:user-b"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			k := &fakeKandev{}
			svc, _ := newService(t, k, SSHIdentity{})
			req := validRequest()
			req.Labels = nil
			mutate(&req)
			_, err := svc.Register(context.Background(), req)
			var v *ValidationError
			if !errors.As(err, &v) || !IsClientError(err) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if len(k.executors) != 0 {
				t.Fatal("an invalid request must not reach Kandev")
			}
		})
	}
}

func TestRegisterRejectsBadTokenAndTakenEndpointBeforeKandev(t *testing.T) {
	k := &fakeKandev{}
	svc, _ := newService(t, k, SSHIdentity{})
	req := validRequest()
	req.Token = "wrong"
	if _, err := svc.Register(context.Background(), req); !errors.Is(err, store.ErrTokenInvalid) {
		t.Fatalf("bad token = %v", err)
	}
	req = validRequest()
	req.Labels = nil
	if _, err := svc.Register(context.Background(), req); err != nil {
		t.Fatalf("first register: %v", err)
	}
	k.executors = nil
	if err := svcStoreToken(svc, "tok-2"); err != nil {
		t.Fatal(err)
	}
	req.Token = "tok-2"
	if _, err := svc.Register(context.Background(), req); !errors.Is(err, store.ErrSeatExists) {
		t.Fatalf("taken endpoint = %v", err)
	}
	if len(k.executors) != 0 {
		t.Fatal("a taken endpoint must not reach Kandev")
	}
}

func svcStoreToken(svc *Service, raw string) error {
	return svc.store.CreateToken(context.Background(), raw, store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now)
}

func TestRegisterKandevFailures(t *testing.T) {
	t.Run("executor create fails", func(t *testing.T) {
		k := &fakeKandev{executorErr: errors.New("HTTP 403: admin required")}
		svc, st := newService(t, k, SSHIdentity{})
		req := validRequest()
		req.Labels = nil
		_, err := svc.Register(context.Background(), req)
		var kerr *KandevError
		if !errors.As(err, &kerr) || IsClientError(err) {
			t.Fatalf("err = %v, want KandevError", err)
		}
		if _, err := st.LookupToken(context.Background(), "tok-1", now); err != nil {
			t.Fatalf("token must stay usable after a Kandev failure: %v", err)
		}
	})
	t.Run("profile create fails deletes executor", func(t *testing.T) {
		k := &fakeKandev{profileErr: errors.New("HTTP 500")}
		svc, _ := newService(t, k, SSHIdentity{})
		req := validRequest()
		req.Labels = nil
		if _, err := svc.Register(context.Background(), req); err == nil {
			t.Fatal("expected an error")
		}
		if len(k.deleted) != 1 || k.deleted[0] != "exec-1" {
			t.Fatalf("deleted = %v, want the created executor", k.deleted)
		}
	})
	t.Run("token consumed concurrently deletes executor", func(t *testing.T) {
		k := &fakeKandev{}
		svc, st := newService(t, k, SSHIdentity{})
		other := store.Seat{ID: "seat-other", OwnerUserID: "user-a", SSHHost: "mini-9", SSHPort: 22, SSHUser: "seat-z",
			HostKeyFingerprint: goodFingerprint, HMACSecret: "x", State: store.SeatOnline, Capacity: 1}
		k.onCreateExecutor = func() {
			if err := st.RegisterSeat(context.Background(), "tok-1", other, now); err != nil {
				t.Errorf("concurrent register: %v", err)
			}
		}
		req := validRequest()
		req.Labels = nil
		if _, err := svc.Register(context.Background(), req); !errors.Is(err, store.ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
		if len(k.deleted) != 1 {
			t.Fatalf("the losing registration must delete its executor, deleted = %v", k.deleted)
		}
	})
}
