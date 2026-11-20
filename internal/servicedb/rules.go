package servicedb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
)

// ErrRule marks a naming rule that cannot be stored: a bad glob, an
// empty or unprintable service name, or a duplicate of an existing rule.
var ErrRule = errors.New("servicedb: invalid naming rule")

// ErrNoRule marks a rule id that does not exist.
var ErrNoRule = errors.New("servicedb: no such naming rule")

// maxService bounds a service name: it is a label in listings and
// graphs, not a place for prose.
const maxService = 64

// Rule names a service (decision #24's default: glob rules): every
// endpoint whose hostname or IP address matches HostGlob, on Port (0:
// any port), is shown as Service. The IP counts too because many
// connections never resolved a name (IP-literal configs, no agent).
type Rule struct {
	ID       int64
	HostGlob string // path.Match syntax, matched case-insensitively
	Port     uint16
	Service  string
}

// AddRule stores a new rule and returns its id. The glob is lower-cased
// (hostnames are case-insensitive) and validated; a rule with the same
// glob and port as an existing one is refused rather than replacing it,
// so a human's earlier decision is never silently overwritten — remove
// it first.
func (d *DB) AddRule(ctx context.Context, r Rule) (int64, error) {
	r.HostGlob = strings.ToLower(strings.TrimSpace(r.HostGlob))
	if err := validRule(r); err != nil {
		return 0, err
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM naming_rule
		WHERE host_glob = ? AND port = ?`, r.HostGlob, int64(r.Port)).Scan(&n); err != nil {
		return 0, fmt.Errorf("servicedb: %w", err)
	}
	if n > 0 {
		return 0, fmt.Errorf("%w: a rule for %s port %d exists already", ErrRule, r.HostGlob, r.Port)
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO naming_rule (host_glob, port, service)
		VALUES (?, ?, ?)`, r.HostGlob, int64(r.Port), r.Service)
	if err != nil {
		return 0, fmt.Errorf("servicedb: %w", err)
	}
	return res.LastInsertId()
}

// RemoveRule deletes the rule with id.
func (d *DB) RemoveRule(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM naming_rule WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("%w: %d", ErrNoRule, id)
	}
	return nil
}

// Rules returns every rule by id.
func (d *DB) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, host_glob, port, service
		FROM naming_rule ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("servicedb: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only: rows.Err reports failures
	var out []Rule
	for rows.Next() {
		var r Rule
		var port int64
		if err := rows.Scan(&r.ID, &r.HostGlob, &port, &r.Service); err != nil {
			return nil, fmt.Errorf("servicedb: %w", err)
		}
		r.Port = uint16(port) // the table's CHECK keeps it in range
		out = append(out, r)
	}
	return out, rows.Err()
}

// validRule checks a rule before it is stored.
func validRule(r Rule) error {
	if r.HostGlob == "" {
		return fmt.Errorf("%w: empty host glob", ErrRule)
	}
	if _, err := path.Match(r.HostGlob, ""); err != nil {
		return fmt.Errorf("%w: host glob %q: %v", ErrRule, r.HostGlob, err)
	}
	if r.Service == "" || len(r.Service) > maxService {
		return fmt.Errorf("%w: service name must be 1 to %d bytes", ErrRule, maxService)
	}
	if i := strings.IndexFunc(r.Service, func(c rune) bool {
		return !unicode.IsPrint(c) || unicode.IsSpace(c)
	}); i >= 0 {
		// A name lands in terminal listings and graph labels: control
		// characters could rewrite the operator's screen.
		return fmt.Errorf("%w: service name %q: space or control character", ErrRule, r.Service)
	}
	return nil
}

// resolve names an endpoint by the rules. Several may match: a rule for
// the endpoint's port beats an any-port rule, then the longer (more
// specific) glob wins, then the older rule — so the answer never depends
// on the order rules come back in.
func resolve(rules []Rule, hostname, ip string, port uint16) string {
	var match []Rule
	for _, r := range rules {
		if r.Port != 0 && r.Port != port {
			continue
		}
		if globMatch(r.HostGlob, strings.ToLower(hostname)) || globMatch(r.HostGlob, ip) {
			match = append(match, r)
		}
	}
	if len(match) == 0 {
		return ""
	}
	best := slices.MinFunc(match, func(a, b Rule) int {
		return cmp.Or(-cmp.Compare(a.Port, b.Port), -cmp.Compare(len(a.HostGlob), len(b.HostGlob)),
			cmp.Compare(a.ID, b.ID))
	})
	return best.Service
}

// globMatch matches a stored (hence valid) glob; an empty name matches
// nothing, so "*" never names an endpoint by its missing hostname.
func globMatch(glob, name string) bool {
	if name == "" {
		return false
	}
	ok, _ := path.Match(glob, name) // validated on AddRule
	return ok
}
