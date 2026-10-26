// Package registry registers seats: it validates a one-time token, creates
// the seat's fixed Kandev SSH executor and profile, and records the seat.
package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Kandev is the part of the Kandev client registration needs.
type Kandev interface {
	CreateExecutor(ctx context.Context, in kandev.CreateExecutorInput) (kandev.Executor, error)
	CreateExecutorProfile(ctx context.Context, executorID string, in kandev.CreateExecutorProfileInput) (kandev.ExecutorProfile, error)
	DeleteExecutor(ctx context.Context, id string) error
}

// SSHIdentity is how Kandev authenticates to every seat it creates.
type SSHIdentity struct {
	// Source is "agent" (the Kandev host's ssh-agent) or "file".
	Source string
	// File is the private key path on the Kandev host when Source is "file".
	File string
}

// Service registers seats.
type Service struct {
	store    *store.Store
	kandev   Kandev
	identity SSHIdentity
	log      *slog.Logger
	now      func() time.Time
}

// New returns a registration service.
func New(st *store.Store, k Kandev, identity SSHIdentity, log *slog.Logger) *Service {
	return &Service{store: st, kandev: k, identity: identity, log: log, now: time.Now}
}

// Request is what a node sends to register one seat.
type Request struct {
	Token              string   `json:"token"`
	SSHHost            string   `json:"ssh_host"`
	SSHPort            int      `json:"ssh_port"`
	SSHUser            string   `json:"ssh_user"`
	HostKeyFingerprint string   `json:"host_key_fingerprint"`
	Labels             []string `json:"labels"`
	Capacity           int      `json:"capacity"`
	AgentVersion       string   `json:"agent_version"`
}

// Result is returned once; the HMAC secret is never shown again.
type Result struct {
	SeatID                  string `json:"seat_id"`
	OwnerUserID             string `json:"owner_user_id"`
	HMACSecret              string `json:"hmac_secret"`
	KandevExecutorID        string `json:"kandev_executor_id"`
	KandevExecutorProfileID string `json:"kandev_executor_profile_id"`
}

// ValidationError is a request the node can fix.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// KandevError wraps a failed Kandev call.
type KandevError struct{ Err error }

func (e *KandevError) Error() string { return "kandev: " + e.Err.Error() }
func (e *KandevError) Unwrap() error { return e.Err }

const (
	defaultSSHPort = 22
	maxCapacity    = 8
	maxLabels      = 16
)

var (
	fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	sshUserPattern     = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	hostPattern        = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$|^[0-9A-Fa-f:]{2,39}$`)
	labelPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,62}$`)
)

// adminLabelPrefixes are reserved for labels an admin puts on the token; a
// node cannot claim them for itself.
var adminLabelPrefixes = []string{"team:", "owner:"}

// Register validates the token and request, creates the Kandev executor and
// profile, then consumes the token and stores the seat atomically. If the
// final step fails the created executor is deleted, so a failed registration
// leaves nothing behind and the token stays usable.
func (s *Service) Register(ctx context.Context, req Request) (Result, error) {
	if err := normalize(&req); err != nil {
		return Result{}, err
	}
	now := s.now()
	tok, err := s.store.LookupToken(ctx, req.Token, now)
	if err != nil {
		return Result{}, err
	}
	taken, err := s.store.SeatEndpointTaken(ctx, req.SSHHost, req.SSHPort, req.SSHUser)
	if err != nil {
		return Result{}, err
	}
	if taken {
		return Result{}, store.ErrSeatExists
	}
	seatID, err := randomHex("seat_", 8)
	if err != nil {
		return Result{}, err
	}
	secret, err := randomHex("", 32)
	if err != nil {
		return Result{}, err
	}
	executor, profile, err := s.createKandevResources(ctx, seatID, tok.OwnerUserID, req)
	if err != nil {
		return Result{}, err
	}
	seat := store.Seat{
		ID: seatID, OwnerUserID: tok.OwnerUserID, SSHHost: req.SSHHost, SSHPort: req.SSHPort,
		SSHUser: req.SSHUser, HostKeyFingerprint: req.HostKeyFingerprint, Labels: mergeLabels(tok.Labels, req.Labels),
		Capacity: req.Capacity, KandevExecutorID: executor.ID, KandevExecutorProfileID: profile.ID,
		HMACSecret: secret, State: store.SeatOnline, AgentVersion: req.AgentVersion,
	}
	if err := s.store.RegisterSeat(ctx, req.Token, seat, now); err != nil {
		s.deleteExecutor(executor.ID, seatID)
		return Result{}, err
	}
	s.log.Info("seat registered", "seat_id", seatID, "owner_user_id", tok.OwnerUserID,
		"ssh_user", req.SSHUser, "ssh_host", req.SSHHost, "kandev_executor_id", executor.ID)
	return Result{
		SeatID: seatID, OwnerUserID: tok.OwnerUserID, HMACSecret: secret,
		KandevExecutorID: executor.ID, KandevExecutorProfileID: profile.ID,
	}, nil
}

func (s *Service) createKandevResources(ctx context.Context, seatID, owner string, req Request) (kandev.Executor, kandev.ExecutorProfile, error) {
	config := map[string]string{
		kandev.ConfigSSHHost:            req.SSHHost,
		kandev.ConfigSSHPort:            strconv.Itoa(req.SSHPort),
		kandev.ConfigSSHUser:            req.SSHUser,
		kandev.ConfigSSHHostFingerprint: req.HostKeyFingerprint,
	}
	if s.identity.Source != "" {
		config[kandev.ConfigSSHIdentitySource] = s.identity.Source
	}
	if s.identity.File != "" {
		config[kandev.ConfigSSHIdentityFile] = s.identity.File
	}
	resumable := true
	executor, err := s.kandev.CreateExecutor(ctx, kandev.CreateExecutorInput{
		OwnerUserID: owner, Name: executorName(req), Type: kandev.ExecutorTypeSSH,
		Resumable: &resumable, Config: config,
	})
	if err != nil {
		return kandev.Executor{}, kandev.ExecutorProfile{}, &KandevError{Err: err}
	}
	profile, err := s.kandev.CreateExecutorProfile(ctx, executor.ID, kandev.CreateExecutorProfileInput{
		OwnerUserID: owner, Name: seatID,
	})
	if err != nil {
		s.deleteExecutor(executor.ID, seatID)
		return kandev.Executor{}, kandev.ExecutorProfile{}, &KandevError{Err: err}
	}
	return executor, profile, nil
}

// deleteExecutor compensates a failed registration. It uses a fresh context
// so a cancelled request still cleans up.
func (s *Service) deleteExecutor(executorID, seatID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.kandev.DeleteExecutor(ctx, executorID); err != nil {
		s.log.Error("failed to delete executor after a failed registration; delete it in Kandev",
			"kandev_executor_id", executorID, "seat_id", seatID, "error", err)
	}
}

func executorName(req Request) string {
	return fmt.Sprintf("fleet %s@%s", req.SSHUser, req.SSHHost)
}

func normalize(req *Request) error {
	req.Token = strings.TrimSpace(req.Token)
	req.SSHHost = strings.TrimSpace(req.SSHHost)
	req.SSHUser = strings.TrimSpace(req.SSHUser)
	req.HostKeyFingerprint = strings.TrimSpace(req.HostKeyFingerprint)
	if req.SSHPort == 0 {
		req.SSHPort = defaultSSHPort
	}
	if req.Capacity == 0 {
		req.Capacity = 1
	}
	switch {
	case req.Token == "":
		return &ValidationError{"token is required"}
	case !hostPattern.MatchString(req.SSHHost):
		return &ValidationError{"ssh_host must be a hostname or IP address"}
	case req.SSHPort < 1 || req.SSHPort > 65535:
		return &ValidationError{"ssh_port must be between 1 and 65535"}
	case !sshUserPattern.MatchString(req.SSHUser):
		return &ValidationError{"ssh_user must be a POSIX user name"}
	case !fingerprintPattern.MatchString(req.HostKeyFingerprint):
		return &ValidationError{"host_key_fingerprint must look like SHA256:<43 base64 characters>"}
	case req.Capacity < 1 || req.Capacity > maxCapacity:
		return &ValidationError{fmt.Sprintf("capacity must be between 1 and %d", maxCapacity)}
	}
	return validateNodeLabels(req.Labels)
}

func validateNodeLabels(labels []string) error {
	if len(labels) > maxLabels {
		return &ValidationError{fmt.Sprintf("at most %d labels", maxLabels)}
	}
	for _, label := range labels {
		if !labelPattern.MatchString(label) {
			return &ValidationError{fmt.Sprintf("invalid label %q", label)}
		}
		for _, prefix := range adminLabelPrefixes {
			if strings.HasPrefix(label, prefix) {
				return &ValidationError{fmt.Sprintf("label %q is admin-only; put it on the registration token", label)}
			}
		}
	}
	return nil
}

// mergeLabels keeps the token's labels (set by an admin) and adds the node's
// own, without duplicates. Admin labels cannot be removed by the node.
func mergeLabels(admin, node []string) []string {
	seen := make(map[string]bool, len(admin)+len(node))
	out := make([]string, 0, len(admin)+len(node))
	for _, label := range append(append([]string{}, admin...), node...) {
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

// NewToken returns a new raw registration token.
func NewToken() (string, error) { return randomHex("fleet_reg_", 24) }

func randomHex(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// IsClientError reports whether err is the caller's fault rather than fleetd's.
func IsClientError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v) || errors.Is(err, store.ErrTokenInvalid) || errors.Is(err, store.ErrSeatExists)
}
