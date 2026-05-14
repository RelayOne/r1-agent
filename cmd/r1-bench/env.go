// env.go — environment-variable lookup, isolated so vendor.go's
// getenvFirst seam can be overridden in tests without dragging the
// real `os` package in.
package main

import "os"

func osGetenv(name string) string { return os.Getenv(name) }
