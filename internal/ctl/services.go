package ctl

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"text/tabwriter"

	"iormesh/internal/servicedb"
)

// servicesCommand is `iormeshctl services [VERB] ...` (PLAN.md M4
// "Service-interaction database"): humans name services here, never by
// editing a file. The verbs:
//
//	list    (default) observed endpoints, unnamed first
//	edges   who talks to whom; -from NAME: what NAME talks to
//	name    -port N GLOB SERVICE: add a naming rule
//	rules   the naming rules with their ids
//	unname  ID: remove a naming rule
//
// The build fills the database; services only reads it, apart from the
// rules, which only services writes.
func servicesCommand(args []string, e env) error {
	verb := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	switch verb {
	case "list":
		return servicesList(args, e)
	case "edges":
		return servicesEdges(args, e)
	case "name":
		return servicesName(args, e)
	case "rules":
		return servicesRules(args, e)
	case "unname":
		return servicesUnname(args, e)
	}
	return fmt.Errorf("%w: services: unknown verb %q", errUsage, verb)
}

// servicesFlags is the flag set every verb shares: the database path.
func servicesFlags(verb string, e env) (*flag.FlagSet, *string) {
	fs := newFlagSet("services "+verb, e.stderr)
	path := fs.String("db", "", "service database (default ~/.local/share/iormesh/services.db)")
	return fs, path
}

// How a verb opens the database.
const (
	dbRead   = iota // reads: a missing database is a note, not an error
	dbEdit          // edits existing rows: a missing database is an error
	dbCreate        // adds a rule: creates the database when missing
)

// withDB opens the database (flag path, else the default) for fn. Only
// dbCreate may create it: listing must not leave an empty database and
// its directory behind.
func withDB(path string, e env, mode int, fn func(context.Context, *servicedb.DB) error) error {
	if path == "" {
		var err error
		if path, err = e.servicedb(); err != nil {
			return err
		}
	}
	ctx := e.root()
	open := servicedb.OpenExisting
	if mode == dbCreate {
		open = servicedb.Open
	}
	d, err := open(ctx, path)
	if mode == dbRead && errors.Is(err, servicedb.ErrNoDatabase) {
		_, err = fmt.Fprintf(e.stderr, "iormeshctl: no service database at %s yet (a build creates it)\n", safe(path))
		return err
	}
	if err != nil {
		return err
	}
	err = fn(ctx, d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// parse parses a verb's flags and requires exactly n positional args.
func parse(fs *flag.FlagSet, args []string, n int, what string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != n {
		return fmt.Errorf("%w: %s needs %s", errUsage, fs.Name(), what)
	}
	return nil
}

func servicesList(args []string, e env) error {
	fs, path := servicesFlags("list", e)
	project := fs.String("project", "", "only connections traced in this project")
	paths := fs.Bool("paths", false, "also list each endpoint's HTTP requests")
	if err := parse(fs, args, 0, "no arguments"); err != nil {
		return err
	}
	return withDB(*path, e, dbRead, func(ctx context.Context, d *servicedb.DB) error {
		eps, err := d.Endpoints(ctx, servicedb.Filter{Project: *project})
		if err != nil {
			return err
		}
		return writeEndpoints(e.stdout, eps, *paths)
	})
}

// writeEndpoints prints the endpoint table; an unnamed endpoint shows
// "-" as its service, the cue to name it.
func writeEndpoints(w io.Writer, eps []servicedb.Endpoint, paths bool) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tENDPOINT\tCONNS\tUNRESOLVED\tTLS\tCLIENTS")
	for _, ep := range eps {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", safe(cmp.Or(ep.Service, "-")), safe(endpointText(ep)),
			ep.Conns, ep.Unresolved, safe(cmp.Or(ep.TLS, "?")), safe(strings.Join(ep.Clients, ",")))
		if !paths {
			continue
		}
		for _, r := range ep.Requests {
			_, _ = fmt.Fprintf(tw, "\t  %s %s\t%d\t\t\t\n", safe(r.Method), safe(r.Path), r.Count)
		}
	}
	// tabwriter buffers every row; Flush reports the first write error.
	return tw.Flush()
}

// endpointText is "hostname (ip:port)" or "ip:port"; a loopback or
// link-local endpoint's address ends in "@host", the traced host it is
// local to, so two hosts' 127.0.0.1:5432 rows tell apart. Each part
// from the database is quoted on its own (an IP that does not parse is
// printed as stored), so a hostile one stays visibly apart from the
// rest; the print wraps the whole in safe once more, which changes
// nothing then.
func endpointText(ep servicedb.Endpoint) string {
	addr := safe(ep.IP) + ":" + strconv.Itoa(int(ep.Port))
	if a, err := netip.ParseAddr(ep.IP); err == nil {
		addr = netip.AddrPortFrom(a, ep.Port).String()
	}
	if ep.Host != "" {
		addr += "@" + safe(ep.Host)
	}
	if ep.Hostname == "" {
		return addr
	}
	return safe(ep.Hostname) + " (" + addr + ")"
}

func servicesEdges(args []string, e env) error {
	fs, path := servicesFlags("edges", e)
	project := fs.String("project", "", "only connections traced in this project")
	from := fs.String("from", "", "only what this client (service or app) talks to")
	if err := parse(fs, args, 0, "no arguments"); err != nil {
		return err
	}
	return withDB(*path, e, dbRead, func(ctx context.Context, d *servicedb.DB) error {
		edges, err := d.Interactions(ctx, servicedb.Filter{Project: *project})
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "FROM\t\tTO\tCONNS\tUNRESOLVED")
		for _, in := range edges {
			if *from == "" || in.From == *from {
				_, _ = fmt.Fprintf(tw, "%s\t->\t%s\t%d\t%d\n", safe(in.From), safe(in.To),
					in.Conns, in.Unresolved)
			}
		}
		return tw.Flush()
	})
}

func servicesName(args []string, e env) error {
	fs, path := servicesFlags("name", e)
	port := fs.Uint("port", 0, "match only this port (default: any)")
	if err := parse(fs, args, 2, "GLOB and SERVICE"); err != nil {
		return err
	}
	if *port > 65535 {
		return fmt.Errorf("%w: -port %d: out of range", errUsage, *port)
	}
	r := servicedb.Rule{HostGlob: fs.Arg(0), Port: uint16(*port), Service: fs.Arg(1)}
	return withDB(*path, e, dbCreate, func(ctx context.Context, d *servicedb.DB) error {
		id, err := d.AddRule(ctx, r)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(e.stdout, "rule %d: %s port %s -> %s\n", id,
			safe(strings.ToLower(r.HostGlob)), safe(portText(r.Port)), safe(r.Service))
		return err
	})
}

func servicesRules(args []string, e env) error {
	fs, path := servicesFlags("rules", e)
	if err := parse(fs, args, 0, "no arguments"); err != nil {
		return err
	}
	return withDB(*path, e, dbRead, func(ctx context.Context, d *servicedb.DB) error {
		rules, err := d.Rules(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tHOST GLOB\tPORT\tSERVICE")
		for _, r := range rules {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", r.ID, safe(r.HostGlob), safe(portText(r.Port)), safe(r.Service))
		}
		return tw.Flush()
	})
}

func servicesUnname(args []string, e env) error {
	fs, path := servicesFlags("unname", e)
	if err := parse(fs, args, 1, "a rule ID"); err != nil {
		return err
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: rule id %q: %v", errUsage, fs.Arg(0), err)
	}
	return withDB(*path, e, dbEdit, func(ctx context.Context, d *servicedb.DB) error {
		if err := d.RemoveRule(ctx, id); err != nil {
			return err
		}
		_, err := fmt.Fprintf(e.stdout, "removed rule %d\n", id)
		return err
	})
}

func portText(p uint16) string {
	if p == 0 {
		return "any"
	}
	return strconv.Itoa(int(p))
}
