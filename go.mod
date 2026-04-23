module github.com/ericmacdougall/stoke

go 1.25.5

require (
	github.com/99designs/keyring v1.2.2
	github.com/charmbracelet/bubbletea v0.25.0
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/go-rod/rod v0.116.2
	github.com/google/uuid v1.6.0
	github.com/mark3labs/mcp-go v0.49.0
	github.com/mattn/go-isatty v0.0.20
	github.com/mattn/go-sqlite3 v1.14.37
	github.com/oklog/ulid/v2 v2.1.1
	github.com/smacker/go-tree-sitter v0.0.0-20240827094217-dd81d9e9be82
	golang.org/x/crypto v0.50.0
	golang.org/x/sync v0.20.0
	golang.org/x/term v0.42.0
	gopkg.in/yaml.v3 v3.0.1
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

require (
	github.com/99designs/go-keychain v0.0.0-20191008050251-8e49817e8af4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/ansi v0.8.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13-0.20250311204145-2c3ea96c31dd // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/containerd/console v1.0.4-0.20230313162750-1ae8d489ac81 // indirect
	github.com/danieljoos/wincred v1.1.2 // indirect
	github.com/dvsekhvalnov/jose2go v1.5.0 // indirect
	github.com/godbus/dbus v0.0.0-20190726142602-4481cbc300e2 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/gsterjov/go-libsecret v0.0.0-20161001094733-a6f4afe4910c // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/mtibben/percent v0.2.1 // indirect
	github.com/muesli/ansi v0.0.0-20211018074035-2e021307bc4b // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/ysmood/fetchup v0.2.3 // indirect
	github.com/ysmood/goob v0.4.0 // indirect
	github.com/ysmood/got v0.40.0 // indirect
	github.com/ysmood/gson v0.7.3 // indirect
	github.com/ysmood/leakless v0.9.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
)
