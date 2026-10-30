package runstore

// The schema stores each reference-store value in its own table. Identity
// values (run subjects, invocations, early terminal outcomes) use the
// generated goa-ai codecs; times use time.Time's binary encoding, which keeps
// the full range and the zone offset. Unique keys and
// partial unique indexes enforce the single-winner rules that the reference
// store checks under its mutex.

// schema lists the statements that create version 1 of the database.
var schema = []string{
	// sessions holds host-owned sessions. ended_at is NULL while active.
	`CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY,
		status TEXT NOT NULL CHECK (status IN ('active', 'ended')),
		created_at BLOB NOT NULL,
		ended_at BLOB
	) STRICT`,
	// purged_sessions seals session IDs that were permanently removed.
	`CREATE TABLE purged_sessions (
		session_id TEXT PRIMARY KEY
	) STRICT`,
	// runs holds RunMeta, the keys of the lifecycle records that made each
	// state visible, the engine request digest, and the selected terminal
	// result. resolution_kind is '' while a suspension is unresolved.
	`CREATE TABLE runs (
		run_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		parent_run_id TEXT NOT NULL,
		subject BLOB NOT NULL,
		origin BLOB NOT NULL,
		seed_end_id TEXT NOT NULL,
		status TEXT NOT NULL,
		start_outcome TEXT NOT NULL,
		started_at BLOB NOT NULL,
		updated_at BLOB NOT NULL,
		labels BLOB,
		cancellation_reason TEXT NOT NULL,
		resolution_kind TEXT NOT NULL CHECK (resolution_kind IN ('', 'continued', 'relinquished')),
		resolution_run_id TEXT NOT NULL,
		resolution_reason TEXT NOT NULL,
		resolution_at BLOB NOT NULL,
		start_kind INTEGER NOT NULL,
		request_digest BLOB NOT NULL,
		start_key TEXT NOT NULL,
		parent_link_key TEXT NOT NULL,
		cancellation_key TEXT NOT NULL,
		suspension_key TEXT NOT NULL,
		terminal_key TEXT NOT NULL,
		terminal_result BLOB
	) STRICT`,
	`CREATE INDEX runs_by_session ON runs (session_id)`,
	// suspensions holds each suspended run's opaque checkpoint bytes.
	`CREATE TABLE suspensions (
		run_id TEXT PRIMARY KEY,
		suspension_id TEXT NOT NULL,
		data BLOB NOT NULL
	) STRICT`,
	// records holds the ordered run log. seq is the run-scoped record ID and
	// session_seq the session-scoped commit position (NULL for sessionless
	// runs). The unique keys make record identity and positions single-winner.
	`CREATE TABLE records (
		run_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		event_key TEXT NOT NULL,
		session_id TEXT NOT NULL,
		session_seq INTEGER,
		subject BLOB NOT NULL,
		turn_id TEXT NOT NULL,
		type TEXT NOT NULL,
		payload BLOB NOT NULL,
		timestamp BLOB NOT NULL,
		PRIMARY KEY (run_id, seq),
		UNIQUE (run_id, event_key)
	) STRICT`,
	`CREATE UNIQUE INDEX records_by_session ON records (session_id, session_seq) WHERE session_seq IS NOT NULL`,
	// preparation_operations binds each destination run ID to its original
	// command and invocation before any upload.
	`CREATE TABLE preparation_operations (
		run_id TEXT PRIMARY KEY,
		subject BLOB NOT NULL,
		session_id TEXT NOT NULL,
		command_id TEXT NOT NULL,
		origin BLOB NOT NULL
	) STRICT`,
	// preparation_attempts holds one upload candidate per attempt: its frozen
	// declaration, the store-resolved source, the accepted end positions and
	// whether it was published or abandoned. At most one attempt per run may
	// be published.
	`CREATE TABLE preparation_attempts (
		run_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		subject BLOB NOT NULL,
		session_id TEXT NOT NULL,
		command_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		predecessor_run_id TEXT NOT NULL,
		source_run_id TEXT NOT NULL,
		source_end_id TEXT NOT NULL,
		exclude_reasoning INTEGER NOT NULL,
		rendered_prompts BLOB,
		resolved_source INTEGER NOT NULL,
		resolved_run_id TEXT NOT NULL,
		resolved_end_id TEXT NOT NULL,
		resolved_exclude_system INTEGER NOT NULL,
		resolved_exclude_reasoning INTEGER NOT NULL,
		seed_end_id TEXT NOT NULL,
		end_id TEXT NOT NULL,
		record_count INTEGER NOT NULL,
		prepared_bytes INTEGER NOT NULL,
		has_source INTEGER NOT NULL,
		published INTEGER NOT NULL,
		abandoned INTEGER NOT NULL,
		PRIMARY KEY (run_id, attempt_id)
	) STRICT`,
	`CREATE UNIQUE INDEX preparation_one_publication ON preparation_attempts (run_id) WHERE published = 1`,
	`CREATE INDEX preparation_attempts_by_session ON preparation_attempts (session_id)`,
	// seed_records holds each attempt's ordered initial-history and
	// compiled-start records. Exactly one content column group is set.
	`CREATE TABLE seed_records (
		run_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		position INTEGER NOT NULL,
		record_key TEXT NOT NULL,
		previous_id TEXT NOT NULL,
		messages BLOB,
		literal_data BLOB,
		literal_final INTEGER,
		prefix_run_id TEXT,
		prefix_end_id TEXT,
		prefix_exclude_system INTEGER,
		prefix_exclude_reasoning INTEGER,
		prepared BLOB,
		PRIMARY KEY (run_id, attempt_id, position),
		UNIQUE (run_id, attempt_id, record_key),
		FOREIGN KEY (run_id, attempt_id) REFERENCES preparation_attempts (run_id, attempt_id) ON DELETE CASCADE
	) STRICT`,
	// unstarted_terminals holds the first failure or cancellation of an
	// execution that never stored a Run, encoded with the generated codec.
	`CREATE TABLE unstarted_terminals (
		run_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		encoded BLOB NOT NULL
	) STRICT`,
}
