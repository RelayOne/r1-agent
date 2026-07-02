package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// resolveVersion returns the human-facing version string. It prefers the
// ldflags-injected value; otherwise it reconstructs one from the embedded
// build info (VCS revision + dirty flag) so binaries built with a plain
// `go build`/`go install` still identify their commit.
func resolveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	rev, dirty := vcsInfo()
	if rev == "" {
		return "dev"
	}
	short := rev
	if len(short) > 12 {
		short = short[:12]
	}
	if dirty {
		short += "-dirty"
	}
	return "dev+" + short
}

// versionDetail returns a one-line, human-readable build identity suitable
// for `r1 version` (as opposed to the bare string for `--version`).
func versionDetail() string {
	rev, dirty := vcsInfo()
	parts := []string{"r1 " + resolveVersion()}
	if rev != "" && (version == "" || version == "dev") {
		// resolveVersion already embeds the short rev in the "dev+" form;
		// only add the full rev when a clean ldflags version hides it.
	} else if rev != "" {
		r := rev
		if len(r) > 12 {
			r = r[:12]
		}
		if dirty {
			r += "-dirty"
		}
		parts = append(parts, "("+r+")")
	}
	parts = append(parts, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
	return strings.Join(parts, " ")
}

// vcsInfo extracts the VCS revision and dirty flag from the embedded build
// info. Returns ("", false) when build info or VCS settings are absent
// (e.g. tests, or a build with -buildvcs=false).
func vcsInfo() (revision string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return revision, dirty
}
