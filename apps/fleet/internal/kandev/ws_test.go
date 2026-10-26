package kandev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeKandevWS answers requests through respond and can push notifications.
type fakeKandevWS struct {
	auth    chan string
	respond func(Message) *Message
	conns   chan *websocket.Conn
}

func newFakeKandevWS(t *testing.T, respond func(Message) *Message) (*fakeKandevWS, string) {
	t.Helper()
	f := &fakeKandevWS{auth: make(chan string, 1), respond: respond, conns: make(chan *websocket.Conn, 1)}
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		f.auth <- r.Header.Get("Authorization")
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.conns <- ws
		for {
			var msg Message
			if err := ws.ReadJSON(&msg); err != nil {
				return
			}
			if reply := f.respond(msg); reply != nil {
				_ = ws.WriteJSON(reply)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func dialTest(t *testing.T, baseURL string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialWS(ctx, baseURL, "kandev_pat_test")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLaunchSessionRoundTrip(t *testing.T) {
	var got Message
	fake, url := newFakeKandevWS(t, func(msg Message) *Message {
		got = msg
		return &Message{ID: msg.ID, Type: "response", Action: msg.Action,
			Payload: json.RawMessage(`{"success":true,"task_id":"task-1","session_id":"s-1","state":"STARTING"}`)}
	})
	conn := dialTest(t, url)
	if auth := <-fake.auth; auth != "Bearer kandev_pat_test" {
		t.Fatalf("Authorization = %q", auth)
	}

	result, err := conn.LaunchSession(testCtx(t), LaunchSessionInput{
		TaskID: "task-1", Intent: IntentStart, ExecutorProfileID: "prof-1", Priority: "high",
	})
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	if !result.Success || result.SessionID != "s-1" || result.State != "STARTING" {
		t.Fatalf("result = %+v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got.Type != "request" || got.Action != "session.launch" || got.ID == "" ||
		payload["task_id"] != "task-1" || payload["intent"] != "start" || payload["executor_profile_id"] != "prof-1" {
		t.Fatalf("sent %+v payload %+v", got, payload)
	}
	if _, present := payload["session_id"]; present {
		t.Fatal("empty optional fields must be omitted")
	}
}

func TestRequestReturnsWSError(t *testing.T) {
	_, url := newFakeKandevWS(t, func(msg Message) *Message {
		return &Message{ID: msg.ID, Type: "error", Action: msg.Action,
			Payload: json.RawMessage(`{"code":"VALIDATION_ERROR","message":"task_id is required"}`)}
	})
	conn := dialTest(t, url)
	err := conn.SubscribeTask(testCtx(t), "")
	var wsErr *WSError
	if !errors.As(err, &wsErr) || wsErr.Code != "VALIDATION_ERROR" || wsErr.Action != "task.subscribe" {
		t.Fatalf("err = %v", err)
	}
}

func TestNotificationsAreDeliveredAndClosedWithConnection(t *testing.T) {
	fake, url := newFakeKandevWS(t, func(Message) *Message { return nil })
	conn := dialTest(t, url)
	server := <-fake.conns
	if err := server.WriteJSON(Message{Type: "notification", Action: "task.updated", Payload: json.RawMessage(`{"id":"task-1"}`)}); err != nil {
		t.Fatalf("push: %v", err)
	}
	select {
	case n := <-conn.Notifications():
		if n.Action != "task.updated" {
			t.Fatalf("notification = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notification not delivered")
	}

	_ = server.Close()
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("read loop did not stop after the server closed")
	}
	if _, open := <-conn.Notifications(); open {
		t.Fatal("notifications channel must close with the connection")
	}
	if err := conn.SubscribeTask(testCtx(t), "task-1"); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("request after close = %v, want ErrConnClosed", err)
	}
}

func TestPendingRequestFailsWhenConnectionDrops(t *testing.T) {
	fake, url := newFakeKandevWS(t, func(Message) *Message { return nil })
	conn := dialTest(t, url)
	server := <-fake.conns
	errCh := make(chan error, 1)
	go func() { errCh <- conn.SubscribeTask(testCtx(t), "task-1") }()
	time.Sleep(100 * time.Millisecond)
	_ = server.Close()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("err = %v, want ErrConnClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending request did not fail")
	}
}

func TestRequestHonorsContextCancellation(t *testing.T) {
	_, url := newFakeKandevWS(t, func(Message) *Message { return nil })
	conn := dialTest(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := conn.SubscribeTask(ctx, "task-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestDialRejectsUnsupportedScheme(t *testing.T) {
	if _, err := DialWS(context.Background(), "ftp://kandev", ""); err == nil {
		t.Fatal("expected an error")
	}
}
