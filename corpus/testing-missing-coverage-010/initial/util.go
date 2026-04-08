package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses a human-readable duration string and returns a
// time.Duration. Supported suffixes are "s" (seconds), "m" (minutes),
// and "h" (hours). Examples: "30s", "5m", "2h".
//
// Returns an error if the input is empty, has an unrecognized suffix,
// or contains a non-numeric value.
func ParseDuration(input string) (time.Duration, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	suffix := input[len(input)-1:]
	numStr := input[:len(input)-1]

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value %q: %w", numStr, err)
	}

	if val < 0 {
		return 0, fmt.Errorf("negative duration not allowed: %s", input)
	}

	switch suffix {
	case "s":
		return time.Duration(val * float64(time.Second)), nil
	case "m":
		return time.Duration(val * float64(time.Minute)), nil
	case "h":
		return time.Duration(val * float64(time.Hour)), nil
	default:
		return 0, fmt.Errorf("unsupported duration suffix %q in %q", suffix, input)
	}
}

func main() {
	examples := []string{"30s", "5m", "2h", "0s", "1.5h"}
	for _, ex := range examples {
		d, err := ParseDuration(ex)
		if err != nil {
			fmt.Printf("ParseDuration(%q) error: %v\n", ex, err)
			continue
		}
		fmt.Printf("ParseDuration(%q) = %v\n", ex, d)
	}
}
