package ctl

import (
	"flag"

	"iormesh/internal/routemap"
)

// The route map in the build (PLAN.md M7, decision #15): the static
// map, ~/.config/iormesh/routes.yaml beside the registry and under its
// ownership and mode rules, compiled into the table the view model's
// routed edges are joined from. No map is no routed edges, never an
// error; a map that fails its checks fails the build.

// addRoutesFlag registers -routes on build's and refresh's flag sets.
func addRoutesFlag(fs *flag.FlagSet, f *buildFlags) {
	fs.StringVar(&f.routes, "routes", "", "static route map (default ~/.config/iormesh/routes.yaml)")
}

// loadRoutes reads the static route map and compiles it; nil when there
// is none.
func loadRoutes(e env, f buildFlags) (*routemap.Table, error) {
	path := f.routes
	if path == "" {
		if e.registry == nil {
			return nil, nil
		}
		reg, err := e.registry()
		if err != nil {
			return nil, err
		}
		path = routemap.DefaultPath(reg)
	}
	static, err := routemap.Load(path)
	if err != nil || static == nil {
		return nil, err
	}
	return routemap.Compile(static, nil, nil), nil
}
