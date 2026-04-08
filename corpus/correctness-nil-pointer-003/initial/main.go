package main

import (
	"fmt"
	"strings"
)

// Config holds application configuration.
type Config struct {
	Host     string
	Port     int
	Database *DatabaseConfig
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	DSN         string
	MaxPoolSize int
}

// ParseConfig takes a Config pointer and returns a formatted summary.
// BUG: panics when cfg is nil.
func ParseConfig(cfg *Config) (string, error) {
	var parts []string

	parts = append(parts, fmt.Sprintf("host=%s", cfg.Host))
	parts = append(parts, fmt.Sprintf("port=%d", cfg.Port))

	if cfg.Database != nil {
		parts = append(parts, fmt.Sprintf("dsn=%s", cfg.Database.DSN))
		parts = append(parts, fmt.Sprintf("pool=%d", cfg.Database.MaxPoolSize))
	}

	return strings.Join(parts, ";"), nil
}

func main() {
	cfg := &Config{
		Host: "localhost",
		Port: 8080,
		Database: &DatabaseConfig{
			DSN:         "postgres://localhost/mydb",
			MaxPoolSize: 10,
		},
	}

	summary, err := ParseConfig(cfg)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(summary)
}
