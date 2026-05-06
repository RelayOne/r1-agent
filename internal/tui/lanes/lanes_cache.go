package lanes

// renderCache is the per-lane string cache that satisfies the spec's
// §"Render-Cache Contract" invariants. It maps laneID → last rendered
// string + the cell width that string was rendered at, plus a dirty set
// for explicit invalidation.
//
// The cache replaces the forward-declared empty-struct shim in
// lanes_model.go. Per spec checklist item 21:
//
//	any setter that flips Dirty=true invalidates the cell render cache;
//	width change invalidates all; status change invalidates the affected
//	lane.
//
// The Lane.Dirty bit is the canonical signal. The cache also tracks
// per-lane width so a per-cell width change (e.g. one column shrinks
// when a 4th lane appears) invalidates exactly that lane.
type renderCache struct {
	s     map[string]string
	width map[string]int
	dirty map[string]struct{}
}

// newRenderCache constructs an empty cache. Callers must always
// non-nil-init via this constructor so Get/Put/Invalidate can assume
// the maps exist.
func newRenderCache() *renderCache {
	return &renderCache{
		s:     map[string]string{},
		width: map[string]int{},
		dirty: map[string]struct{}{},
	}
}

// Get returns the cached render string for laneID at the supplied cell
// width. Returns ("", false) on miss; treats width-mismatch and
// dirty-bit set as miss conditions per spec §"Render-Cache Contract"
// items 1, 2, and 6.
func (c *renderCache) Get(laneID string, width int) (string, bool) {
	if c == nil || c.s == nil {
		return "", false
	}
	if _, dirty := c.dirty[laneID]; dirty {
		return "", false
	}
	if w, ok := c.width[laneID]; !ok || w != width {
		return "", false
	}
	s, ok := c.s[laneID]
	return s, ok
}

// Put stores the rendered string for laneID at the supplied width and
// clears any pending dirty mark.
func (c *renderCache) Put(laneID string, width int, s string) {
	if c == nil {
		return
	}
	if c.s == nil {
		c.s = map[string]string{}
	}
	if c.width == nil {
		c.width = map[string]int{}
	}
	c.s[laneID] = s
	c.width[laneID] = width
	delete(c.dirty, laneID)
}

// Invalidate marks one lane's entry stale so the next Get misses.
// Used by the renderer when status, focus, or any Dirty-flagged field
// changes. Cheap (one map insert) so callers may invalidate liberally.
func (c *renderCache) Invalidate(laneID string) {
	if c == nil {
		return
	}
	if c.dirty == nil {
		c.dirty = map[string]struct{}{}
	}
	c.dirty[laneID] = struct{}{}
}

// Clear drops the entire cache. Called from Update on a
// tea.WindowSizeMsg that changes width or cols (spec §"Render-Cache
// Contract" item 6).
func (c *renderCache) Clear() {
	if c == nil {
		return
	}
	for k := range c.s {
		delete(c.s, k)
	}
	for k := range c.width {
		delete(c.width, k)
	}
	for k := range c.dirty {
		delete(c.dirty, k)
	}
}
