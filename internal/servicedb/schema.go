package servicedb

// migrations are the schema's versions, applied in order: migrations[i]
// takes a database from user_version i to i+1. Released entries are
// never edited — a change is a new entry — so every database reaches the
// same schema by the same steps.
//
// Text columns use the empty string for "not known", not NULL, so the
// import's keep-if-unknown merge is one COALESCE(NULLIF(new, ...)) and reads
// scan into plain strings. tls is the one tri-state (NULL: no HTTP
// request said).
var migrations = []string{
	`CREATE TABLE conn (
		host           TEXT    NOT NULL,
		conn_id        TEXT    NOT NULL,
		role           TEXT    NOT NULL CHECK (role IN ('client', 'server', 'unknown')),
		state          TEXT    NOT NULL CHECK (state IN ('fused', 'unresolved')),
		reason         TEXT    NOT NULL DEFAULT '',
		merged_id      TEXT    NOT NULL DEFAULT '',
		project        TEXT    NOT NULL,
		client_host    TEXT    NOT NULL DEFAULT '',
		client_project TEXT    NOT NULL DEFAULT '',
		client_app     TEXT    NOT NULL DEFAULT '',
		client_comm    TEXT    NOT NULL DEFAULT '',
		server_host    TEXT    NOT NULL DEFAULT '',
		server_conn    TEXT    NOT NULL DEFAULT '',
		server_project TEXT    NOT NULL DEFAULT '',
		server_app     TEXT    NOT NULL DEFAULT '',
		server_comm    TEXT    NOT NULL DEFAULT '',
		ip             TEXT    NOT NULL DEFAULT '',
		port           INTEGER NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
		hostname       TEXT    NOT NULL DEFAULT '',
		tls            INTEGER CHECK (tls IN (0, 1)),
		first_seen     INTEGER NOT NULL,
		last_seen      INTEGER NOT NULL CHECK (last_seen >= first_seen),
		PRIMARY KEY (host, conn_id));
	CREATE INDEX conn_server ON conn (server_host, server_conn);
	CREATE INDEX conn_endpoint ON conn (hostname, ip, port);
	CREATE TABLE http_request (
		host       TEXT    NOT NULL,
		conn_id    TEXT    NOT NULL,
		method     TEXT    NOT NULL,
		scheme     TEXT    NOT NULL,
		http_host  TEXT    NOT NULL,
		path       TEXT    NOT NULL,
		requests   INTEGER NOT NULL CHECK (requests > 0),
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL CHECK (last_seen >= first_seen),
		PRIMARY KEY (host, conn_id, method, scheme, http_host, path),
		FOREIGN KEY (host, conn_id) REFERENCES conn (host, conn_id) ON DELETE CASCADE);
	CREATE TABLE naming_rule (
		id        INTEGER PRIMARY KEY,
		host_glob TEXT    NOT NULL CHECK (host_glob <> ''),
		port      INTEGER NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
		service   TEXT    NOT NULL CHECK (service <> ''),
		UNIQUE (host_glob, port));`,
}
