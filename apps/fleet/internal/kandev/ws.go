package kandev

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WS message types, from pkg/websocket/message.go.
const (
	messageTypeRequest      = "request"
	messageTypeResponse     = "response"
	messageTypeNotification = "notification"
	messageTypeError        = "error"
)

// Message is the Kandev WebSocket envelope.
type Message struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// WSError is an error-typed WebSocket reply.
type WSError struct {
	Action  string
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *WSError) Error() string {
	return fmt.Sprintf("kandev: %s: %s: %s", e.Action, e.Code, e.Message)
}

// ErrConnClosed is returned for requests on, or pending at, a closed connection.
var ErrConnClosed = errors.New("kandev: websocket closed")

// notificationBuffer bounds queued notifications. When the reader falls this
// far behind, the oldest notification is dropped; callers must reconcile from
// REST rather than treat notifications as complete.
const notificationBuffer = 256

// Conn is one authenticated WebSocket connection. Its read loop starts on
// dial and stops on Close or a read error.
type Conn struct {
	ws            *websocket.Conn
	writeMu       sync.Mutex
	mu            sync.Mutex
	pending       map[string]chan Message
	notifications chan Message
	closed        chan struct{}
	closeOnce     sync.Once
	done          chan struct{}
	err           error
}

// DialWS opens /ws on baseURL with the token as a bearer credential.
func DialWS(ctx context.Context, baseURL, token string) (*Conn, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("kandev: parse base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("kandev: unsupported base URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("kandev: dial websocket: HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("kandev: dial websocket: %w", err)
	}
	c := &Conn{
		ws:            ws,
		pending:       make(map[string]chan Message),
		notifications: make(chan Message, notificationBuffer),
		closed:        make(chan struct{}),
		done:          make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Notifications delivers server notifications in arrival order. It is
// closed when the connection ends.
func (c *Conn) Notifications() <-chan Message { return c.notifications }

// Done is closed when the read loop ends; Err then reports why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the read loop's terminal error, valid after Done is closed.
func (c *Conn) Err() error { return c.err }

// Close ends the connection and waits for the read loop to stop.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	err := c.ws.Close()
	<-c.done
	return err
}

// Request sends action with payload and decodes the matching response
// payload into out (when out is non-nil).
func (c *Conn) Request(ctx context.Context, action string, payload, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("kandev: encode %s: %w", action, err)
	}
	id, err := newRequestID()
	if err != nil {
		return err
	}
	reply := make(chan Message, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return ErrConnClosed
	default:
	}
	c.pending[id] = reply
	c.mu.Unlock()
	defer c.forget(id)

	msg := Message{ID: id, Type: messageTypeRequest, Action: action, Payload: raw, Timestamp: time.Now().UTC()}
	if err := c.write(ctx, msg); err != nil {
		return err
	}
	select {
	case resp, ok := <-reply:
		if !ok {
			return ErrConnClosed
		}
		return decodeReply(action, resp, out)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LaunchSession sends session.launch.
func (c *Conn) LaunchSession(ctx context.Context, in LaunchSessionInput) (LaunchSessionResult, error) {
	var out LaunchSessionResult
	err := c.Request(ctx, "session.launch", in, &out)
	return out, err
}

// AddMessage sends message.add: a user message to a task session. Kandev
// queues it when the agent is mid-turn.
func (c *Conn) AddMessage(ctx context.Context, taskID, sessionID, content string) error {
	return c.Request(ctx, "message.add", map[string]string{
		"task_id": taskID, "session_id": sessionID, "content": content,
	}, nil)
}

// SubscribeTask sends task.subscribe so the connection receives the task's
// notifications.
func (c *Conn) SubscribeTask(ctx context.Context, taskID string) error {
	return c.Request(ctx, "task.subscribe", map[string]string{"task_id": taskID}, nil)
}

func decodeReply(action string, resp Message, out any) error {
	if resp.Type == messageTypeError {
		wsErr := &WSError{Action: action}
		if err := json.Unmarshal(resp.Payload, wsErr); err != nil {
			return fmt.Errorf("kandev: %s: undecodable error reply: %w", action, err)
		}
		return wsErr
	}
	if out == nil || len(resp.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Payload, out); err != nil {
		return fmt.Errorf("kandev: decode %s reply: %w", action, err)
	}
	return nil
}

func (c *Conn) write(ctx context.Context, msg Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.ws.SetWriteDeadline(deadline)
		defer func() { _ = c.ws.SetWriteDeadline(time.Time{}) }()
	}
	if err := c.ws.WriteJSON(msg); err != nil {
		return fmt.Errorf("kandev: write %s: %w", msg.Action, err)
	}
	return nil
}

func (c *Conn) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) readLoop() {
	defer c.finish()
	for {
		var msg Message
		if err := c.ws.ReadJSON(&msg); err != nil {
			c.err = err
			return
		}
		switch msg.Type {
		case messageTypeResponse, messageTypeError:
			c.deliverReply(msg)
		case messageTypeNotification:
			c.deliverNotification(msg)
		}
	}
}

func (c *Conn) deliverReply(msg Message) {
	c.mu.Lock()
	reply, ok := c.pending[msg.ID]
	c.mu.Unlock()
	if ok {
		reply <- msg
	}
}

func (c *Conn) deliverNotification(msg Message) {
	for {
		select {
		case c.notifications <- msg:
			return
		default:
		}
		select {
		case <-c.notifications:
		default:
		}
	}
}

func (c *Conn) finish() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	for id, reply := range c.pending {
		close(reply)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.notifications)
	close(c.done)
}

func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("kandev: request id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
