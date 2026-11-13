package app

import (
	"context"
	"net/http"

	"github.com/Smrnw/UnbrakeTV/internal/tv"
)

// Optional features live in their own files and plug into the server through
// these hooks, so the core handlers stay short.

type apiFunc func(path, method string, h func(w http.ResponseWriter, r *http.Request))

// appAction does something to one app and returns the journal line.
type appAction func(s *Server, d *tv.Device, pkg string) (string, error)

var (
	// featureRoutes register additional API endpoints.
	featureRoutes []func(s *Server, api apiFunc)
	// onStart runs when the server is created.
	onStart []func(s *Server)
	// afterUpdateCheck runs after every look at the project's GitHub page.
	afterUpdateCheck []func(s *Server, ctx context.Context)
	// stateExtra adds fields to /api/state; it is called with s.mu held.
	stateExtra []func(s *Server, m map[string]any)
	// packagesExtra adds fields to /api/packages.
	packagesExtra []func(s *Server, d *tv.Device, m map[string]any)
	// appActions are the actions of /api/app besides "stop".
	appActions = map[string]appAction{}

	// removePackages performs a removal that has already passed all checks.
	removePackages = func(s *Server, d *tv.Device, all []tv.Package, req changeReq) []tv.Result {
		if req.Method == "disable" {
			return d.Disable(req.Packages)
		}
		return d.Remove(req.Packages)
	}
	// restorePackages brings removed or disabled packages back.
	restorePackages = func(s *Server, d *tv.Device, pkgs []string) []tv.Result { return d.Restore(pkgs) }
)
