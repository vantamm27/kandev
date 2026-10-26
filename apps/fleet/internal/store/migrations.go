package store

// migrations are applied in order; append only, never edit a released entry.
var migrations = []string{
	`
	CREATE TABLE seats (
		id TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL,
		ssh_host TEXT NOT NULL,
		ssh_port INTEGER NOT NULL DEFAULT 22,
		ssh_user TEXT NOT NULL,
		host_key_fingerprint TEXT NOT NULL,
		labels TEXT NOT NULL DEFAULT '[]',
		capacity INTEGER NOT NULL DEFAULT 1,
		kandev_executor_id TEXT NOT NULL DEFAULT '',
		kandev_executor_profile_id TEXT NOT NULL DEFAULT '',
		hmac_secret TEXT NOT NULL,
		state TEXT NOT NULL,
		quota_5h_pct REAL,
		quota_week_pct REAL,
		reset_5h_at TIMESTAMP,
		reset_week_at TIMESTAMP,
		free_slots INTEGER NOT NULL DEFAULT 0,
		agent_version TEXT NOT NULL DEFAULT '',
		last_heartbeat_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE (ssh_host, ssh_port, ssh_user)
	);
	CREATE INDEX idx_seats_owner ON seats(owner_user_id);
	CREATE INDEX idx_seats_state ON seats(state);

	CREATE TABLE registration_tokens (
		token_hash TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL,
		labels TEXT NOT NULL DEFAULT '[]',
		expires_at TIMESTAMP NOT NULL,
		used_at TIMESTAMP,
		used_by_seat_id TEXT,
		created_at TIMESTAMP NOT NULL
	);

	CREATE TABLE dispatch_claims (
		task_id TEXT PRIMARY KEY,
		seat_id TEXT NOT NULL REFERENCES seats(id),
		attempt INTEGER NOT NULL DEFAULT 1,
		state TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		claimed_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX idx_dispatch_claims_seat ON dispatch_claims(seat_id, state);

	CREATE TABLE handoffs (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		from_seat_id TEXT NOT NULL REFERENCES seats(id),
		to_seat_id TEXT REFERENCES seats(id),
		state TEXT NOT NULL,
		path TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX idx_handoffs_task ON handoffs(task_id, attempt);
	`,
	`
	ALTER TABLE seats ADD COLUMN limit_reason TEXT NOT NULL DEFAULT '';
	ALTER TABLE seats ADD COLUMN last_signed_at INTEGER NOT NULL DEFAULT 0;
	`,
}
