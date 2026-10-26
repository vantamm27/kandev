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
	conn, err := l.connection(ctx)
	if err != nil {
		return LaunchSessionResult{}, err
	}
	res, err := conn.LaunchSession(ctx, in)
	if errors.Is(err, ErrConnClosed) {
		l.drop(conn)
	}
	return res, err
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
