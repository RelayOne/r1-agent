// Package gitblame implements git blame integration for attribution-aware editing.
// Inspired by Aider's git integration and claw-code's change attribution:
//
// Understanding who wrote what enables:
// - Targeted review (route reviews to the original author)
// - Blame-aware refactoring (preserve authorship, attribute changes)
// - Impact assessment (how many authors' code is being changed)
// - Recency analysis (is this code recently touched or ancient)
//
// This is critical for multi-contributor repos where changing old code
// may require different review thoroughness than changing recent code.
package gitblame

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Line is a single line with blame attribution.
type Line struct {
	Number  int       `json:"number"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	Commit  string    `json:"commit"`
	Content string    `json:"content"`
}

// FileBlame holds blame data for an entire file.
type FileBlame struct {
	Path  string `json:"path"`
	Lines []Line `json:"lines"`
}

// Blame runs git blame on a file and returns parsed results.
func Blame(repoDir, filePath string) (*FileBlame, error) {
	cmd := exec.Command("git", "blame", "--porcelain", filePath)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git blame %s: %w", filePath, err)
	}

	return ParsePorcelain(filePath, string(out))
}

// ParsePorcelain parses git blame --porcelain output.
func ParsePorcelain(filePath, output string) (*FileBlame, error) {
	fb := &FileBlame{Path: filePath}
	lines := strings.Split(output, "\n")

	// Cache commit metadata for continuation lines
	type commitInfo struct {
		Author string
		Date   time.Time
	}
	cache := make(map[string]commitInfo)

	var current Line
	lineNum := 0

	for _, line := range lines {
		if m := headerRegex.FindStringSubmatch(line); m != nil {
			current.Commit = m[1]
			lineNum++
			current.Number = lineNum
			// Apply cached info for continuation lines
			if info, ok := cache[current.Commit]; ok {
				current.Author = info.Author
				current.Date = info.Date
			}
		} else if strings.HasPrefix(line, "author ") {
			current.Author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "author-time ") {
			ts := strings.TrimPrefix(line, "author-time ")
			var unix int64
			fmt.Sscanf(ts, "%d", &unix)
			current.Date = time.Unix(unix, 0)
		} else if strings.HasPrefix(line, "\t") {
			current.Content = line[1:]
			// Cache commit metadata
			cache[current.Commit] = commitInfo{Author: current.Author, Date: current.Date}
			fb.Lines = append(fb.Lines, current)
			current = Line{}
		}
	}

	return fb, nil
}

// Authors returns unique authors sorted by line count (descending).
func (fb *FileBlame) Authors() []AuthorStat {
	counts := make(map[string]int)
	for _, l := range fb.Lines {
		counts[l.Author]++
	}

	var stats []AuthorStat
	for author, count := range counts {
		stats = append(stats, AuthorStat{
			Author:     author,
			Lines:      count,
			Percentage: float64(count) / float64(len(fb.Lines)) * 100,
		})
	}

	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Lines > stats[j].Lines
	})
	return stats
}

// AuthorStat tracks an author's contribution to a file.
type AuthorStat struct {
	Author     string  `json:"author"`
	Lines      int     `json:"lines"`
	Percentage float64 `json:"percentage"`
}

// LinesBy returns all lines written by a specific author.
func (fb *FileBlame) LinesBy(author string) []Line {
	var result []Line
	for _, l := range fb.Lines {
		if l.Author == author {
			result = append(result, l)
		}
	}
	return result
}

// LineRange returns blame for a specific line range (1-indexed, inclusive).
func (fb *FileBlame) LineRange(start, end int) []Line {
	var result []Line
	for _, l := range fb.Lines {
		if l.Number >= start && l.Number <= end {
			result = append(result, l)
		}
	}
	return result
}

// AuthorsInRange returns unique authors who wrote lines in the given range.
func (fb *FileBlame) AuthorsInRange(start, end int) []string {
	seen := make(map[string]bool)
	for _, l := range fb.Lines {
		if l.Number >= start && l.Number <= end {
			seen[l.Author] = true
		}
	}
	var authors []string
	for a := range seen {
		authors = append(authors, a)
	}
	sort.Strings(authors)
	return authors
}

// Freshness classifies how recently code was last modified.
type Freshness string

const (
	FreshRecent  Freshness = "recent"  // within 30 days
	FreshModern  Freshness = "modern"  // within 1 year
	FreshStale   Freshness = "stale"   // 1-3 years
	FreshAncient Freshness = "ancient" // 3+ years
)

// ClassifyFreshness returns the freshness of a line based on its blame date.
func ClassifyFreshness(date time.Time) Freshness {
	age := time.Since(date)
	switch {
	case age < 30*24*time.Hour:
		return FreshRecent
	case age < 365*24*time.Hour:
		return FreshModern
	case age < 3*365*24*time.Hour:
		return FreshStale
	default:
		return FreshAncient
	}
}

// FreshnessDistribution returns what fraction of lines fall into each freshness category.
func (fb *FileBlame) FreshnessDistribution() map[Freshness]float64 {
	counts := make(map[Freshness]int)
	for _, l := range fb.Lines {
		f := ClassifyFreshness(l.Date)
		counts[f]++
	}

	total := len(fb.Lines)
	if total == 0 {
		return nil
	}

	dist := make(map[Freshness]float64)
	for f, c := range counts {
		dist[f] = float64(c) / float64(total)
	}
	return dist
}

// ImpactSummary describes the human impact of changing lines in a range.
func (fb *FileBlame) ImpactSummary(start, end int) string {
	authors := fb.AuthorsInRange(start, end)
	lines := fb.LineRange(start, end)

	if len(lines) == 0 {
		return "no lines in range"
	}

	var oldest time.Time
	for _, l := range lines {
		if oldest.IsZero() || l.Date.Before(oldest) {
			oldest = l.Date
		}
	}

	freshness := ClassifyFreshness(oldest)
	return fmt.Sprintf("%d lines, %d authors (%s), oldest code is %s",
		len(lines), len(authors), strings.Join(authors, ", "), freshness)
}

var headerRegex = regexp.MustCompile(`^([0-9a-f]{40})\s+\d+\s+\d+`)
