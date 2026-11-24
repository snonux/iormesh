package servicedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// FileName is the database's name under the data directory.
const FileName = "services.db"

// ErrUnsafeFile marks a database file or directory another user could
// read or replace: refused, never repaired, as the registry does
// (internal/federate) — repairing would bless whatever is already there.
var ErrUnsafeFile = errors.New("servicedb: unsafe file")

// ErrNewerSchema marks a database a newer iormeshctl migrated past the
// versions this one knows: reading it could misread columns, and writing
// it could break the newer tool.
var ErrNewerSchema = errors.New("servicedb: database schema is newer than this iormeshctl")

// ErrNoDatabase marks a database OpenExisting did not find: no build
// has created it yet.
var ErrNoDatabase = errors.New("servicedb: no service database yet")

// DB is an open service-interaction database.
type DB struct {
	db *sql.DB
}

// DefaultPath is ~/.local/share/iormesh/services.db: the directory the
// collected traces live under (docs/milestones.md M4 "Collect"), that path on
// every platform, so a laptop's state is always in one place.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("servicedb path: %w", err)
	}
	return filepath.Join(home, ".local", "share", "iormesh", FileName), nil
}

// Open opens the database at path, creating it (mode 0600, its directory
// 0700) when missing, and migrates it to the current schema. A file that
// is not a sqlite database, or one migrated by a newer iormeshctl, is an
// error; so is a file or directory other users could read or write.
func Open(ctx context.Context, path string) (*DB, error) {
	return open(ctx, path, true)
}

// OpenExisting is Open without the creating: a missing database is
// ErrNoDatabase. Reading commands use it, so listing never leaves an
// empty database (or its directory) behind as a side effect.
func OpenExisting(ctx context.Context, path string) (*DB, error) {
	return open(ctx, path, false)
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func open(ctx context.Context, path string, create bool) (*DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("servicedb: %w", err)
	}
	// The driver opens names as URIs (a "file:" prefix) and reads its
	// options after '?': an absolute path cannot start with "file:" or be
	// ":memory:", and one with '?' or '#' would open another file than
	// the one checked here.
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("servicedb: path %q: '?' and '#' are not supported", path)
	}
	if err := prepareFile(path, create); err != nil {
		return nil, err
	}
	// One connection: sqlite serializes writers anyway, and the
	// per-connection pragmas then hold for every statement. Transactions
	// take the write lock up front (immediate), so two iormeshctl runs
	// migrating or importing at once queue instead of failing mid-way.
	sqldb, err := sql.Open("sqlite", path+
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("servicedb: open %s: %w", path, err)
	}
	sqldb.SetMaxOpenConns(1)
	d := &DB{db: sqldb}
	if err := d.migrate(ctx); err != nil {
		_ = sqldb.Close() // the migrate error is the one to report
		return nil, fmt.Errorf("servicedb: %s: %w", path, err)
	}
	return d, nil
}

// prepareFile makes sure path's directory is private enough and that
// path is a regular file of this user's with no group or other bits.
// With create, the directory (0700) and the file (empty, 0600) are made
// when missing, before sqlite would create the file under the umask;
// sqlite gives its journal the database's mode, so that is private too.
//
// The checks run before sqlite opens the path, which leaves a gap: the
// file could be swapped between the Lstat and the open. Only someone who
// can write the directory can do that, and such a directory is refused
// first — so the gap is open to this user alone.
func prepareFile(path string, create bool) error {
	dir := filepath.Dir(path)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("servicedb: %w", err)
		}
	}
	if err := checkDir(dir, path, create); err != nil {
		return err
	}
	if create {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f.Close()
		}
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("servicedb: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNoDatabase, path)
	}
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, path)
	}
	return checkOwned(info, path, 0o077)
}

// checkDir requires the database's directory to be this user's and
// writable by nobody else: whoever can write it can rename their own
// file over the database. Reading it is the file's business.
func checkDir(dir, path string, create bool) error {
	info, err := os.Stat(dir)
	if !create && errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNoDatabase, path)
	}
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	return checkOwned(info, dir, 0o022)
}

// checkOwned requires info (of path) to belong to this user and to have
// none of mask's permission bits.
func checkOwned(info fs.FileInfo, path string, mask fs.FileMode) error {
	if perm := info.Mode().Perm(); perm&mask != 0 {
		return fmt.Errorf("%w: %s has mode %v (no %v bits allowed)", ErrUnsafeFile, path, perm, mask)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d", ErrUnsafeFile, path, st.Uid)
	}
	return nil
}

// migrate brings the schema to len(migrations), one transaction per
// step, so a failed step leaves the database at the previous version.
// Each step reads user_version inside its (immediate, so write-locked)
// transaction: a second iormeshctl migrating the same file meanwhile
// can neither run a step twice nor skip one. Reading user_version is
// also the check that the file is a sqlite database at all.
func (d *DB) migrate(ctx context.Context) error {
	for {
		done := false
		err := d.inTx(ctx, func(tx *sql.Tx) error {
			var v int
			if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
				return fmt.Errorf("read schema version: %w", err)
			}
			switch {
			case v > len(migrations):
				return fmt.Errorf("%w: version %d, this one knows %d", ErrNewerSchema, v, len(migrations))
			case v == len(migrations):
				done = true
				return nil
			}
			if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
				return fmt.Errorf("migrate to version %d: %w", v+1, err)
			}
			// PRAGMA takes no bound parameters; v+1 is our own integer.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1))
			return err
		})
		if err != nil || done {
			return err
		}
	}
}

// inTx runs fn in a transaction, committing when fn succeeds and rolling
// back otherwise.
func (d *DB) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback() // fn's error is the one to report
		return err
	}
	return tx.Commit()
}
