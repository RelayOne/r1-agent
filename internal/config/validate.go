package config

import (
	"fmt"
	"strings"
)

// ValidationError is a non-fatal issue found during config validation.
type ValidationError struct {
	Field   string
	Message string
	Fatal   bool
}

func (e ValidationError) Error() string {
	prefix := "warning"
	if e.Fatal { prefix = "error" }
	return fmt.Sprintf("[%s] %s: %s", prefix, e.Field, e.Message)
}

// ValidatePolicy checks a loaded policy for structural problems.
func ValidatePolicy(p Policy) []ValidationError {
	var errs []ValidationError

	requiredPhases := []string{"plan", "execute", "verify"}
	for _, name := range requiredPhases {
		phase, ok := p.Phases[name]
		if !ok {
			errs = append(errs, ValidationError{
				Field: "phases." + name, Message: "required phase missing", Fatal: true,
			})
			continue
		}
		if len(phase.BuiltinTools) == 0 {
			errs = append(errs, ValidationError{
				Field: "phases." + name + ".builtin_tools", Message: "no tools configured",
			})
		}
	}

	// Plan phase should be read-only
	if plan, ok := p.Phases["plan"]; ok {
		for _, tool := range plan.BuiltinTools {
			if tool == "Edit" || tool == "Write" {
				errs = append(errs, ValidationError{
					Field: "phases.plan.builtin_tools", Message: "plan phase should be read-only (has " + tool + ")",
				})
			}
		}
		if plan.MCPEnabled {
			errs = append(errs, ValidationError{
				Field: "phases.plan.mcp_enabled", Message: "plan phase should not enable MCP",
			})
		}
	}

	// Execute phase should have sandbox-aware deny rules
	if exec, ok := p.Phases["execute"]; ok {
		hasDeny := false
		for _, rule := range exec.DeniedRules {
			if strings.Contains(rule, "git push") || strings.Contains(rule, "rm -rf") {
				hasDeny = true
			}
		}
		if !hasDeny {
			errs = append(errs, ValidationError{
				Field: "phases.execute.denied_rules", Message: "missing safety rules (git push, rm -rf)",
			})
		}
	}

	// Verify phase should not have write tools
	if v, ok := p.Phases["verify"]; ok {
		for _, tool := range v.BuiltinTools {
			if tool == "Edit" || tool == "Write" {
				errs = append(errs, ValidationError{
					Field: "phases.verify.builtin_tools", Message: "verify phase should not have write tools",
				})
			}
		}
	}

	if len(p.Files.Protected) == 0 {
		errs = append(errs, ValidationError{
			Field: "files.protected", Message: "no protected files configured",
		})
	}

	return errs
}

// ValidateCommands checks if build/test/lint commands are configured.
func ValidateCommands(build, test, lint string) []ValidationError {
	var errs []ValidationError
	if strings.TrimSpace(build) == "" {
		errs = append(errs, ValidationError{Field: "build-cmd", Message: "no build command (use --build-cmd or add go.mod/package.json)"})
	}
	if strings.TrimSpace(test) == "" {
		errs = append(errs, ValidationError{Field: "test-cmd", Message: "no test command (use --test-cmd or add go.mod/package.json)"})
	}
	if strings.TrimSpace(lint) == "" {
		errs = append(errs, ValidationError{Field: "lint-cmd", Message: "no lint command (use --lint-cmd or add go.mod/package.json)"})
	}
	return errs
}
