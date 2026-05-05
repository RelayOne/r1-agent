// Package serviceunit wraps github.com/kardianos/service to provide a
// uniform Install / Uninstall / Start / Stop / Status surface for the
// `r1 serve --install` operator flow (specs/r1d-server.md TASK-37).
//
// Why kardianos/service. It already understands systemd (Linux),
// launchd (macOS), and Windows SCM. We need exactly that "write the
// unit, register with the platform service manager, start it"
// surface — re-implementing it per OS is boilerplate that already
// exists upstream and is battle-tested.
//
// What this package adds on top of upstream:
//
//   - A small, named Install/Uninstall/Start/Stop/Status façade that
//     never returns the upstream service.Service type so callers from
//     cmd/r1/serve_cmd.go don't have to import the upstream package
//     directly.
//   - Cross-platform unit-name + display-name conventions baked into
//     Defaults() so the operator gets a consistent service name on
//     every OS.
//   - Per-OS Option overrides: systemd-user vs systemd-system, launchd
//     LaunchAgent (per-user) vs LaunchDaemon (system), and the
//     loginctl-enable-linger note (TASK-39) surfaced in the error
//     message when a non-root install fails on a headless box.
//
// The Service runs as a no-op program — kardianos/service's Run()
// loops blocking until the service manager signals stop. Production
// wiring in cmd/r1/serve_cmd.go subclasses with the real serveLoop
// closure; this package owns only the install plumbing.
package serviceunit

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/kardianos/service"
)

// DefaultName is the service identifier used by every platform's
// service manager. Lower-case, dotless: matches launchd's reverse-DNS
// convention without forcing a domain prefix that's not ours to use.
const DefaultName = "r1-serve"

// DefaultDisplayName is the human-friendly name shown in service
// listings (systemctl, launchctl, services.msc).
const DefaultDisplayName = "R1 Serve Daemon"

// DefaultDescription is the short human-readable description.
const DefaultDescription = "Per-user R1 long-running daemon (queue, WAL, agent-serve)."

// Status is the current state of the installed service. Mirrors
// service.Status but is exposed here as a string so callers don't
// import the upstream package.
type Status string

const (
	// StatusUnknown means kardianos/service couldn't determine the
	// status (typically because the service is not installed or the
	// platform's service manager refused the query).
	StatusUnknown Status = "unknown"
	// StatusRunning means the service is installed and running.
	StatusRunning Status = "running"
	// StatusStopped means the service is installed and stopped.
	StatusStopped Status = "stopped"
	// StatusNotInstalled means the service is not registered.
	StatusNotInstalled Status = "not-installed"
)

// Config plumbs unit-name + executable arguments into Service.New.
// Fields that are zero get sensible defaults from Defaults().
type Config struct {
	// Name is the service identifier (default DefaultName).
	Name string
	// DisplayName is the human-readable label (default DefaultDisplayName).
	DisplayName string
	// Description is the long-form summary (default DefaultDescription).
	Description string
	// Arguments are the CLI args appended after the resolved
	// executable path. Production wiring passes ["serve",
	// "--addr=", "--enable-agent-routes", ...] so the installed unit
	// re-launches r1 with the same flags the operator used to install.
	Arguments []string
	// Executable, when non-empty, overrides the auto-detected
	// os.Executable() path. Tests use this; production leaves it
	// empty so the unit always points at the installing binary.
	Executable string
	// UserMode, when true, requests a per-user service unit
	// (systemd --user, LaunchAgent on macOS). When false, attempts a
	// system-wide install (systemctl, LaunchDaemon, Windows SCM with
	// admin). Default: true (per-user is the sane default for an
	// interactive workstation tool).
	UserMode bool
	// EnvVars are environment variables propagated into the service
	// unit. Useful for HOME / R1_HOME / PATH overrides on systemd-user.
	EnvVars map[string]string
}

// Defaults returns a Config with the default name / display / description
// and UserMode=true. Callers fill in Arguments + EnvVars per their
// own flag parsing.
func Defaults() Config {
	return Config{
		Name:        DefaultName,
		DisplayName: DefaultDisplayName,
		Description: DefaultDescription,
		UserMode:    true,
	}
}

// Service is the façade. It owns a kardianos/service.Service for the
// life of the program and exposes the Install/Uninstall/Start/Stop/Status
// methods the CLI needs (TASK-37 spec scope).
//
// Note on lifecycle. This package owns the install-plumbing surface.
// When the platform service manager actually launches `r1 serve` from
// the installed unit, the binary runs `runServeLoop` directly (in
// cmd/r1/serve_cmd.go) — the listener is the long-running blocking
// call and platform-level stop is delivered as a signal handled by
// the existing signalContext path. This means the kardianos
// program-Interface callbacks below are not the run-loop; they exist
// because kardianos requires a non-nil program to construct a
// service.Service. The Start callback returns nil immediately and
// the Stop callback is unused. The actual run loop is the HTTP
// server, supervised by the platform via process exit.
type Service struct {
	cfg Config
	svc service.Service
	// program is the kardianos/service.Interface registered with the
	// upstream library. It is intentionally a no-op: see the lifecycle
	// note on Service for why the run-loop is owned by serve_cmd.go
	// rather than by service.Run.
	program *unitProgram
}

// unitProgram satisfies kardianos/service.Interface. Start returns
// nil so the binary's main goroutine continues into runServeLoop;
// Stop returns nil so a service-manager-issued Stop signal is
// graceful (the OS still delivers SIGTERM to the process which the
// signalContext handler in serve_cmd.go observes).
type unitProgram struct{}

func (*unitProgram) Start(service.Service) error { return nil }
func (*unitProgram) Stop(service.Service) error  { return nil }

// New constructs a Service ready to drive Install / Uninstall.
// Returns an error when kardianos/service can't synthesize a config
// for the host platform (extremely rare; usually means an unsupported
// init system).
func New(cfg Config) (*Service, error) {
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = DefaultDisplayName
	}
	if cfg.Description == "" {
		cfg.Description = DefaultDescription
	}
	exe := cfg.Executable
	if exe == "" {
		got, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("serviceunit: resolve executable: %w", err)
		}
		exe = got
	}

	uOption := service.KeyValue{}
	if cfg.UserMode {
		uOption["UserService"] = true
	}
	for k, v := range cfg.EnvVars {
		uOption[k] = v
	}

	scfg := &service.Config{
		Name:        cfg.Name,
		DisplayName: cfg.DisplayName,
		Description: cfg.Description,
		Arguments:   cfg.Arguments,
		Executable:  exe,
		Option:      uOption,
	}

	prog := &unitProgram{}
	svc, err := service.New(prog, scfg)
	if err != nil {
		return nil, fmt.Errorf("serviceunit: kardianos/service.New: %w", err)
	}
	return &Service{cfg: cfg, svc: svc, program: prog}, nil
}

// Install registers the service with the platform's service manager.
// On success the unit is in the "stopped" state — call Start to begin
// running. Returns a wrapped error that mentions
// `loginctl enable-linger` when the install path looks like a
// headless systemd-user box (TASK-39) so operators get a clue without
// re-grepping the spec.
func (s *Service) Install() error {
	if s == nil || s.svc == nil {
		return errors.New("serviceunit: nil service")
	}
	if err := s.svc.Install(); err != nil {
		return s.wrapInstallError(err)
	}
	return nil
}

// Uninstall reverses Install. Stops the service first (best-effort);
// removes the unit file from the platform service manager.
func (s *Service) Uninstall() error {
	if s == nil || s.svc == nil {
		return errors.New("serviceunit: nil service")
	}
	// Best-effort stop. Uninstall on a running unit is platform-defined
	// (some implementations stop first, some refuse). We belt-and-
	// suspenders by stopping explicitly.
	_ = s.svc.Stop()
	if err := s.svc.Uninstall(); err != nil {
		return fmt.Errorf("serviceunit: uninstall: %w", err)
	}
	return nil
}

// Start signals the platform service manager to start the registered
// service. Returns an error when the service is not installed or the
// manager refuses (e.g. permissions).
func (s *Service) Start() error {
	if s == nil || s.svc == nil {
		return errors.New("serviceunit: nil service")
	}
	return s.svc.Start()
}

// Stop signals the platform service manager to stop the registered
// service. Idempotent: stopping an already-stopped service is not an
// error on most platforms.
func (s *Service) Stop() error {
	if s == nil || s.svc == nil {
		return errors.New("serviceunit: nil service")
	}
	return s.svc.Stop()
}

// Status returns the current state. Translates kardianos/service's
// numeric status into our typed string so callers don't import the
// upstream package.
func (s *Service) Status() (Status, error) {
	if s == nil || s.svc == nil {
		return StatusUnknown, errors.New("serviceunit: nil service")
	}
	st, err := s.svc.Status()
	if err != nil {
		// kardianos/service returns ErrNotInstalled with a typed
		// sentinel — surface it as our typed StatusNotInstalled.
		if errors.Is(err, service.ErrNotInstalled) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, err
	}
	switch st {
	case service.StatusRunning:
		return StatusRunning, nil
	case service.StatusStopped:
		return StatusStopped, nil
	default:
		return StatusUnknown, nil
	}
}

// Platform returns the kardianos/service-detected platform name
// (e.g. "linux-systemd", "darwin-launchd", "windows-service"). Useful
// for diagnostic logging.
func (s *Service) Platform() string {
	if s == nil || s.svc == nil {
		return runtime.GOOS
	}
	return s.svc.Platform()
}

// Name returns the configured service identifier.
func (s *Service) Name() string {
	return s.cfg.Name
}

// wrapInstallError annotates well-known install failures with the
// loginctl-enable-linger hint (TASK-39). On systemd-user Linux a
// headless box rejects "systemctl --user enable" because the user's
// systemd manager isn't running outside an active login session;
// `loginctl enable-linger $USER` fixes it. Fail-soft: if the wrap
// can't tell, return the original error untouched.
func (s *Service) wrapInstallError(orig error) error {
	if orig == nil {
		return nil
	}
	if runtime.GOOS != "linux" || !s.cfg.UserMode {
		return fmt.Errorf("serviceunit: install: %w", orig)
	}
	return fmt.Errorf(
		"serviceunit: install: %w\n\n"+
			"hint (systemd-user on headless/SSH-only Linux): if the user's\n"+
			"systemd manager isn't running, run `loginctl enable-linger $USER`\n"+
			"as root, log out, log back in, and retry. See specs/r1d-server.md TASK-39.",
		orig,
	)
}
