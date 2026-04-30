package daemon

import (
	"errors"
	"os"
)

// homeDir returns the user's home directory, falling back to /tmp on hostile
// environments.
func homeDir() (string, error) {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h, nil
	}
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	return "/tmp", errors.New("could not determine HOME; fell back to /tmp")
}
