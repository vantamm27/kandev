package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RegisterRequest mirrors fleetd's registry.Request.
type RegisterRequest struct {
	Token              string   `json:"token"`
	SSHHost            string   `json:"ssh_host"`
	SSHPort            int      `json:"ssh_port,omitempty"`
	SSHUser            string   `json:"ssh_user"`
	HostKeyFingerprint string   `json:"host_key_fingerprint"`
	Labels             []string `json:"labels,omitempty"`
	Capacity           int      `json:"capacity,omitempty"`
	AgentVersion       string   `json:"agent_version"`
}

// RegisterResult mirrors fleetd's registry.Result.
type RegisterResult struct {
	SeatID                  string `json:"seat_id"`
	OwnerUserID             string `json:"owner_user_id"`
	HMACSecret              string `json:"hmac_secret"`
	KandevExecutorID        string `json:"kandev_executor_id"`
	KandevExecutorProfileID string `json:"kandev_executor_profile_id"`
}

// Register calls fleetd's POST /v1/register.
func Register(ctx context.Context, client *http.Client, fleetdURL string, req RegisterRequest) (RegisterResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return RegisterResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(fleetdURL, "/")+"/v1/register", bytes.NewReader(body))
	if err != nil {
		return RegisterResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("register: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated {
		return RegisterResult{}, fmt.Errorf("register: HTTP %d: %s", resp.StatusCode, errorMessage(raw))
	}
	var out RegisterResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return RegisterResult{}, fmt.Errorf("register: decode response: %w", err)
	}
	return out, nil
}

func errorMessage(raw []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return body.Error
	}
	return strings.TrimSpace(string(raw))
}

// defaultHTTPTimeout bounds every fleetd call.
const defaultHTTPTimeout = 15 * time.Second

// NewHTTPClient returns the client fleet-agent uses for fleetd.
func NewHTTPClient() *http.Client { return &http.Client{Timeout: defaultHTTPTimeout} }
