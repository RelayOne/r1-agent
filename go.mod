module github.com/RelayOne/r1

go 1.25.5

// S2-1 (work-r1-rename) — module renamed from
// github.com/ericmacdougall/stoke to github.com/RelayOne/r1 to fix
// the portfolio-org gap (the repo moved from an individual account
// to the RelayOne org). Pre-rename versions under the legacy path
// are retracted below; importers should migrate to the new path.
retract [v0.0.0, v0.99.0] // pre-rename individual-account path (github.com/ericmacdougall/stoke)

require (
	charm.land/bubbles/v2 v2.1.0
	charm.land/bubbletea/v2 v2.0.2
	charm.land/lipgloss/v2 v2.0.3
	github.com/99designs/keyring v1.2.2
	github.com/charmbracelet/bubbletea v0.25.0
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/go-rod/rod v0.116.2
	github.com/go-vgo/robotgo v1.0.2
	github.com/google/uuid v1.6.0
	github.com/mark3labs/mcp-go v0.49.0
	github.com/mattn/go-isatty v0.0.20
	github.com/mattn/go-sqlite3 v1.14.37
	github.com/oklog/ulid/v2 v2.1.1
	github.com/smacker/go-tree-sitter v0.0.0-20240827094217-dd81d9e9be82
	github.com/winder/bubblelayout v0.0.1
	golang.org/x/crypto v0.50.0
	golang.org/x/sync v0.20.0
	golang.org/x/term v0.42.0
	golang.org/x/tools v0.44.0
	gopkg.in/yaml.v3 v3.0.1
	heroa.dev/sdk-go v0.0.0
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/charmbracelet/harmonica v0.2.0 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260205113103-524a6607adb8 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/gofrs/flock v0.12.1 // indirect
	golang.org/x/mod v0.35.0 // indirect
)

// TASK 7 (work-stoke) — swap mattn/go-sqlite3 for the sqlite3mc
// fork so `encryption.BuildEncryptedDSN` can produce URIs the
// driver actually honours. The fork is ABI-compatible (same
// package path, same init tags), so callers that still use the
// plaintext `?_journal_mode=WAL` DSN keep working. The version
// is pinned so a future upstream change cannot silently rotate
// the on-disk cipher default. Wire-up gated at runtime by
// `STOKE_DB_ENCRYPTION=1` in internal/wisdom and cmd/r1-server.
replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.17-0.20240122133042-fb824c8e339e

replace heroa.dev/sdk-go => /home/eric/repos/heroa/sdk/go

require (
	github.com/99designs/go-keychain v0.0.0-20191008050251-8e49817e8af4 // indirect
	github.com/RelayOne/coderadar/sdks/go/coderadar v0.0.0-20260430164404-aaefa9e2740a
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/containerd/console v1.0.4-0.20230313162750-1ae8d489ac81 // indirect
	github.com/danieljoos/wincred v1.1.2 // indirect
	github.com/dblohm7/wingoes v0.0.0-20250822163801-6d8e6105c62d // indirect
	github.com/dvsekhvalnov/jose2go v1.5.0 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/gen2brain/shm v0.2.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus v0.0.0-20190726142602-4481cbc300e2 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/go-github/v62 v62.0.0
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/gsterjov/go-libsecret v0.0.0-20161001094733-a6f4afe4910c // indirect
	github.com/jezek/xgb v1.3.0 // indirect
	github.com/jezek/xgbutil v0.0.0-20260124183602-9fd151d6a51a // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20260324052639-156f7da3f749 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/mtibben/percent v0.2.1 // indirect
	github.com/muesli/ansi v0.0.0-20211018074035-2e021307bc4b // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/otiai10/gosseract/v2 v2.4.1 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/shirou/gopsutil/v4 v4.26.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tailscale/win v0.0.0-20250627215312-f4da2b8ee071 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/vcaesar/gops v0.41.0 // indirect
	github.com/vcaesar/imgo v0.41.0 // indirect
	github.com/vcaesar/keycode v0.10.1 // indirect
	github.com/vcaesar/screenshot v0.11.1 // indirect
	github.com/vcaesar/tt v0.20.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/ysmood/fetchup v0.2.3 // indirect
	github.com/ysmood/goob v0.4.0 // indirect
	github.com/ysmood/got v0.40.0 // indirect
	github.com/ysmood/gson v0.7.3 // indirect
	github.com/ysmood/leakless v0.9.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/exp v0.0.0-20260312153236-7ab1446f8b90 // indirect
	golang.org/x/image v0.38.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
)

replace github.com/RelayOne/coderadar/sdks/go/coderadar => /home/eric/repos/CodeRadar/sdks/go/coderadar
