package kandev

import (
	"context"
	"errors"
	"sync"
)

// Launcher sends session.launch over one WebSocket connection that it dials
// on first use and redials after the connection drops.
type Launcher struct {
	baseURL string
	token   string
	mu      sync.Mutex
	conn    *Conn
}

// NewLauncher returns a launcher for baseURL.
func NewLauncher(baseURL, token string) *Launcher {
	return &Launcher{baseURL: baseURL, token: token}
}

// LaunchSession starts a session, dialing a connection when needed.
func (l *Launcher) LaunchSession(ctx context.Context, in LaunchSessionInput) (LaunchSessionResult, error) {
	var res LaunchSessionResult
	err := l.with(ctx, func(conn *Conn) error {
		var err error
		res, err = conn.LaunchSession(ctx, in)
		return err
	})
	return res, err
}

// AddMessage sends message.add on the shared connection.
func (l *Launcher) AddMessage(ctx context.Context, taskID, sessionID, content string) error {
	return l.with(ctx, func(conn *Conn) error { return conn.AddMessage(ctx, taskID, sessionID, content) })
}

// SetPrimarySession sends session.set_primary on the shared connection.
func (l *Launcher) SetPrimarySession(ctx context.Context, sessionID string) error {
	return l.with(ctx, func(conn *Conn) error { return conn.SetPrimarySession(ctx, sessionID) })
}

// StopSession sends session.stop on the shared connection.
func (l *Launcher) StopSession(ctx context.Context, sessionID, reason string) error {
	return l.with(ctx, func(conn *Conn) error { return conn.StopSession(ctx, sessionID, reason) })
}

func (l *Launcher) with(ctx context.Context, call func(*Conn) error) error {
	conn, err := l.connection(ctx)
	if err != nil {
		return err
	}
	err = call(conn)
	if errors.Is(err, ErrConnClosed) {
		l.drop(conn)
	}
	return err
}

// Close closes the current connection, if any.
func (l *Launcher) Close() error {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func (l *Launcher) connection(ctx context.Context) (*Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		select {
		case <-l.conn.Done():
			l.conn = nil
		default:
			return l.conn, nil
		}
	}
	conn, err := DialWS(ctx, l.baseURL, l.token)
	if err != nil {
		return nil, err
	}
	l.conn = conn
	return conn, nil
}

func (l *Launcher) drop(conn *Conn) {
	l.mu.Lock()
	if l.conn == conn {
		l.conn = nil
	}
	l.mu.Unlock()
	_ = conn.Close()
}
