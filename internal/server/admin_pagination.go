package server

// admin_pagination.go — A5 admin panel pagination helpers.
//
// The five read-only admin routes paginate via ?offset= + ?limit=.
// Helpers here parse the params, clamp them to safe bounds, and
// produce the Page struct that templates render into prev/next links.
//
// Spec: specs/admin-panel.md §10 task 19.

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Pagination bounds. Defaults match the spec; constants are package-
// public so handler tests can assert against them directly.
const (
	DefaultPageLimit = 50
	MinPageLimit     = 1
	MaxPageLimit     = 200
)

// Page is the shape templates and JSON twins consume.
type Page struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
	Total  int `json:"total"`
}

// HasPrev returns true when an earlier page exists.
func (p Page) HasPrev() bool { return p.Offset > 0 }

// HasNext returns true when a later page exists.
func (p Page) HasNext() bool { return p.Offset+p.Limit < p.Total }

// PrevOffset clamps the previous-page offset to >= 0.
func (p Page) PrevOffset() int {
	o := p.Offset - p.Limit
	if o < 0 {
		return 0
	}
	return o
}

// NextOffset returns the next-page offset. Caller is responsible for
// checking HasNext first; this method does not bounds-check against
// Total so it stays trivial to inline in templates.
func (p Page) NextOffset() int { return p.Offset + p.Limit }

// PageNumber returns the 1-indexed page number (handy for templates
// that want to render "page N of M").
func (p Page) PageNumber() int {
	if p.Limit <= 0 {
		return 1
	}
	return (p.Offset / p.Limit) + 1
}

// PageCount returns the total number of pages, clamped to >= 1.
func (p Page) PageCount() int {
	if p.Limit <= 0 || p.Total <= 0 {
		return 1
	}
	n := p.Total / p.Limit
	if p.Total%p.Limit != 0 {
		n++
	}
	if n < 1 {
		return 1
	}
	return n
}

// PrevLink builds the URL-encoded query string for the previous page
// preserving every other parameter from the source request.
func (p Page) PrevLink(base string, q url.Values) string {
	return p.linkAt(base, q, p.PrevOffset())
}

// NextLink builds the URL-encoded query string for the next page
// preserving every other parameter from the source request.
func (p Page) NextLink(base string, q url.Values) string {
	return p.linkAt(base, q, p.NextOffset())
}

func (p Page) linkAt(base string, q url.Values, offset int) string {
	out := url.Values{}
	for k, v := range q {
		if k == "offset" || k == "limit" {
			continue
		}
		out[k] = v
	}
	out.Set("offset", strconv.Itoa(offset))
	out.Set("limit", strconv.Itoa(p.Limit))
	return base + "?" + out.Encode()
}

// ParsePage extracts offset + limit from the request's query string
// and clamps them to the documented bounds. Total is supplied by the
// caller after running the underlying query.
func ParsePage(r *http.Request, total int) Page {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if limit < MinPageLimit {
		limit = MinPageLimit
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	// Clamp offset against total so a deep-link to "?offset=1000000"
	// lands on the last full page instead of an empty result.
	if total >= 0 && offset > total {
		// Compute the offset of the final page boundary.
		if total == 0 {
			offset = 0
		} else {
			lastPage := ((total - 1) / limit) * limit
			offset = lastPage
		}
	}
	return Page{Offset: offset, Limit: limit, Total: total}
}

// Slice returns the [start,end) indices for paginating a slice of
// length n with the current Page. Useful for in-memory pagination.
func (p Page) Slice(n int) (int, int) {
	start := p.Offset
	if start > n {
		start = n
	}
	end := start + p.Limit
	if end > n {
		end = n
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	return start, end
}

// String renders a compact "[offset=N limit=M total=K]" diagnostic for
// log lines.
func (p Page) String() string {
	return fmt.Sprintf("[offset=%d limit=%d total=%d]", p.Offset, p.Limit, p.Total)
}
