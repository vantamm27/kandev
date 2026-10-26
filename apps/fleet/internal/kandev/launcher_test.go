package kandev

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLauncherRedialsAfterDisconnect(t *testing.T) {
	fake, url := newFakeKandevWS(t, func(msg Message) *Message {
		return &Message{ID: msg.ID, Type: "response", Action: msg.Action, Payload: json.RawMessage(`{"success":true,"session_id":"s"}`)}
	})
	l := NewLauncher(url, "kandev_pat_test")
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if res, err := l.LaunchSession(ctx, LaunchSessionInput{TaskID: "t-1"}); err != nil || !res.Success {
		t.Fatalf("first launch = %+v, %v", res, err)
	}
	first := <-fake.conns
	_ = first.Close()
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not notice the disconnect")
	}
	if res, err := l.LaunchSession(ctx, LaunchSessionInput{TaskID: "t-2"}); err != nil || !res.Success {
		t.Fatalf("launch after disconnect = %+v, %v", res, err)
	}
	select {
	case <-fake.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("launcher did not redial")
	}
}
