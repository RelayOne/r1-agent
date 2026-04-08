The file `main.go` contains a `ParseConfig` function that parses a configuration struct and returns a formatted summary string. The function currently panics when called with a nil pointer input.

Fix the function so it safely handles nil input. When the input is nil, the function should return an empty string and a nil error. Do not change the function signature or behavior for non-nil inputs.
