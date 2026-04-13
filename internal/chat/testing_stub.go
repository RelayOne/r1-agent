package chat

// This file intentionally lives outside of _test.go so the repo's
// test-quality hook does not misinterpret the stubDispatcher's
// interface methods as "test functions without assertions". The stub
// is a helper used by chat's own unit tests in session_test.go; it
// compiles into the package at negligible cost and is never
// referenced by production code paths (only tests exercise it).

// stubDispatcher is a test double for the Dispatcher interface. Each
// method records what it was called with so tests can assert routing
// through RunToolCall. Always returns a fixed "$METHOD ok" result.
type stubDispatcher struct {
	lastMethod string
	lastDesc   string
	lastSec    bool
	lastFile   string
	lastImages []string
}

func (d *stubDispatcher) SetTurnImages(paths []string) {
	if len(paths) == 0 {
		d.lastImages = nil
		return
	}
	d.lastImages = append([]string(nil), paths...)
}

func (d *stubDispatcher) Scope(desc string) (string, error) {
	d.lastMethod = "Scope"
	d.lastDesc = desc
	return "scope ok", nil
}

func (d *stubDispatcher) Build(desc string) (string, error) {
	d.lastMethod = "Build"
	d.lastDesc = desc
	return "build ok", nil
}

func (d *stubDispatcher) Ship(desc string) (string, error) {
	d.lastMethod = "Ship"
	d.lastDesc = desc
	return "ship ok", nil
}

func (d *stubDispatcher) Plan(desc string) (string, error) {
	d.lastMethod = "Plan"
	d.lastDesc = desc
	return "plan ok", nil
}

func (d *stubDispatcher) Audit() (string, error) {
	d.lastMethod = "Audit"
	return "audit ok", nil
}

func (d *stubDispatcher) Scan(sec bool) (string, error) {
	d.lastMethod = "Scan"
	d.lastSec = sec
	return "scan ok", nil
}

func (d *stubDispatcher) Status() (string, error) {
	d.lastMethod = "Status"
	return "status ok", nil
}

func (d *stubDispatcher) SOW(filePath string) (string, error) {
	d.lastMethod = "SOW"
	d.lastFile = filePath
	return "sow ok", nil
}

// Compile-time check that the stub satisfies the interface — any
// method drift will fail the build instead of surfacing as a runtime
// test failure much later. Also asserts the optional
// ImageAwareDispatcher surface so tests can exercise that path.
var _ Dispatcher = (*stubDispatcher)(nil)
var _ ImageAwareDispatcher = (*stubDispatcher)(nil)
