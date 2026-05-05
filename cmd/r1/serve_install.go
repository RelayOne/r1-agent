package main

// serve_install.go — TASK-38: `r1 serve --install / --uninstall /
// --status` via internal/serviceunit.
//
// This file pre-stages the install/uninstall/status handlers as
// standalone functions so TASK-40's serve_cmd.go can wire them in
// alongside the new --addr / --no-tcp / --no-unix / etc. flag surface.
// They're intentionally callable in isolation — `r1 serve --install`
// is a one-shot lifecycle action that does NOT start the daemon
// inline; the daemon comes up later via the platform service manager
// (systemctl start, launchctl load, services.msc).
//
// Why split the install path from the run path:
//
//   - --install / --uninstall / --status need to succeed even when
//     the run path's flags (--enable-agent-routes, --config) are
//     malformed — operators install first, then tweak. Having them
//     in a separate function isolates the validation.
//   - The unit content captures whatever flags the operator passed
//     alongside --install (e.g. `r1 serve --install --enable-agent-
//     routes --addr 127.0.0.1:9091`). Those flags are forwarded into
//     serviceunit.Config.Arguments so the installed unit's ExecStart
//     re-launches r1 with the same configuration.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/serviceunit"
)

// serveInstallAction is the operator-requested lifecycle action.
// Returned from parseServeFlags so the caller can route to the
// appropriate handler (install / uninstall / status / run).
type serveInstallAction string

const (
	serveActionRun       serveInstallAction = "run"
	serveActionInstall   serveInstallAction = "install"
	serveActionUninstall serveInstallAction = "uninstall"
	serveActionStatus    serveInstallAction = "status"
)

// runServeInstall registers the service unit with the platform's
// service manager and starts it. forwardArgs are the CLI args (minus
// --install / --uninstall / --status) baked into the unit's ExecStart
// so the running unit re-launches r1 with the operator's chosen
// configuration. stdout/stderr are passed in for testability.
func runServeInstall(forwardArgs []string, stdout, stderr io.Writer) int {
	cfg := serviceunit.Defaults()
	// The arguments stored in the unit must call back into the same
	// `serve` verb so the platform service manager re-enters this
	// codepath without --install. We prepend "serve" so the resulting
	// ExecStart looks like `<r1> serve <forwardArgs...>`.
	cfg.Arguments = append([]string{"serve"}, stripInstallFlags(forwardArgs)...)
	cfg.EnvVars = inheritEnvForUnit()

	svc, err := serviceunit.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "serve --install: %v\n", err)
		return 1
	}
	if err := svc.Install(); err != nil {
		fmt.Fprintf(stderr, "serve --install: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed: %s (%s)\n", svc.Name(), svc.Platform())

	if err := svc.Start(); err != nil {
		fmt.Fprintf(stderr, "serve --install: started but Start() failed: %v\n", err)
		// Install succeeded; Start failure is not a hard failure
		// (the operator can `systemctl --user start r1-serve`
		// manually). Exit 1 to flag that the unit isn't running yet.
		return 1
	}
	fmt.Fprintf(stdout, "started: %s\n", svc.Name())
	return 0
}

// runServeUninstall stops + uninstalls the service unit.
func runServeUninstall(stdout, stderr io.Writer) int {
	cfg := serviceunit.Defaults()
	svc, err := serviceunit.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "serve --uninstall: %v\n", err)
		return 1
	}
	// Stop first (best-effort — kardianos/service.Uninstall handles
	// running units inconsistently across platforms).
	_ = svc.Stop()
	if err := svc.Uninstall(); err != nil {
		fmt.Fprintf(stderr, "serve --uninstall: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "uninstalled: %s\n", svc.Name())
	return 0
}

// runServeStatus reports the unit's current state.
func runServeStatus(stdout, stderr io.Writer) int {
	cfg := serviceunit.Defaults()
	svc, err := serviceunit.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "serve --status: %v\n", err)
		return 1
	}
	st, err := svc.Status()
	if err != nil {
		fmt.Fprintf(stderr, "serve --status: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s (%s)\n", svc.Name(), st, svc.Platform())
	switch st {
	case serviceunit.StatusRunning:
		return 0
	case serviceunit.StatusStopped, serviceunit.StatusNotInstalled:
		// Stopped + NotInstalled are non-error reports; exit 3 + 4
		// give scripts an actionable distinction without relying on
		// stdout-parsing.
		if st == serviceunit.StatusStopped {
			return 3
		}
		return 4
	default:
		return 5
	}
}

// stripInstallFlags removes --install / --uninstall / --status from
// the args list before baking them into the unit's ExecStart. The
// installed unit must NOT re-trigger install on every start.
func stripInstallFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch {
		case a == "--install", a == "--uninstall", a == "--status":
			continue
		case strings.HasPrefix(a, "--install="),
			strings.HasPrefix(a, "--uninstall="),
			strings.HasPrefix(a, "--status="):
			continue
		}
		out = append(out, a)
	}
	return out
}

// inheritEnvForUnit captures a small allow-list of environment
// variables that the installed service unit needs to function. We
// deliberately do NOT propagate the full env (a service unit picking
// up an interactive shell's PATH or HISTFILE is a foot-gun); the
// allow-list is HOME / R1_HOME / PATH so the daemon resolves its
// discovery file and finds support binaries.
func inheritEnvForUnit() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"HOME", "R1_HOME", "PATH", "XDG_RUNTIME_DIR"} {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// classifyServeAction inspects the args slice and returns the chosen
// lifecycle action. It does NOT consume args — callers re-parse the
// remaining flags via the run-path's flag.FlagSet. Default action is
// serveActionRun.
//
// Conflicts (e.g. --install + --uninstall in the same invocation) are
// detected and reported via the returned error so TASK-40 can produce
// a clear usage message.
func classifyServeAction(args []string) (serveInstallAction, error) {
	saw := map[serveInstallAction]bool{}
	for _, a := range args {
		switch {
		case a == "--install":
			saw[serveActionInstall] = true
		case a == "--uninstall":
			saw[serveActionUninstall] = true
		case a == "--status":
			saw[serveActionStatus] = true
		}
	}
	if len(saw) > 1 {
		return "", fmt.Errorf("serve: --install / --uninstall / --status are mutually exclusive")
	}
	for k := range saw {
		return k, nil
	}
	return serveActionRun, nil
}
