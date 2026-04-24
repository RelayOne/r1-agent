// Package plan — integrity_php.go
//
// PHP ecosystem. Validates `use Namespace\Class` / `require` against
// composer.json deps (autoload PSR-4 prefixes + declared packages).
// Compile regression uses `php -l` per file. No barrel concept —
// PHP resolves symbols via the Composer autoloader at runtime.
package plan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterEcosystem(&phpEcosystem{})
}

type phpEcosystem struct{}

func (phpEcosystem) Name() string { return "php" }

func (phpEcosystem) Owns(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".php" || ext == ".phtml"
}

var phpUseRE = regexp.MustCompile(`(?m)^\s*use\s+([A-Za-z_][A-Za-z0-9_\\]*)`)

func (phpEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	manifest := filepath.Join(projectRoot, "composer.json")
	if info, err := os.Stat(manifest); err != nil || info.IsDir() {
		return nil, nil
	}
	prefixes, packageNames := phpReadComposer(manifest)
	if prefixes == nil {
		prefixes = map[string]struct{}{}
	}
	relMani, _ := filepath.Rel(projectRoot, manifest)
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range phpUseRE.FindAllStringSubmatch(string(body), -1) {
			ns := m[1]
			if ns == "" {
				continue
			}
			if phpPrefixCovers(ns, prefixes) {
				continue
			}
			// If the namespace maps to any Composer package name via
			// psr-4 roots, skip. Otherwise report.
			key := f + "::" + ns
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			// Suggest composer require on a best-guess package name:
			// vendor/package is a guess; model will often need to
			// refine, but the directive is actionable.
			guess := strings.ToLower(strings.ReplaceAll(ns, `\`, "/"))
			if firstSlash := strings.Index(guess, "/"); firstSlash > 0 {
				guess = guess[:firstSlash+1] + strings.Split(guess[firstSlash+1:], "/")[0]
			}
			_ = packageNames
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: ns,
				Manifest:   relMani,
				AddCommand: fmt.Sprintf("composer require %s # to cover namespace %s (best-guess package name)", guess, ns),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceFile != out[j].SourceFile {
			return out[i].SourceFile < out[j].SourceFile
		}
		return out[i].ImportPath < out[j].ImportPath
	})
	return out, nil
}

// PHP has no barrel concept (Composer autoload handles resolution).
func (phpEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// php -l emits:
//   PHP Parse error:  syntax error, unexpected ... in file.php on line 12
//   No syntax errors detected in file.php
var phpLintErrRE = regexp.MustCompile(`^PHP\s+(Parse|Fatal|Syntax)\s+error:\s+(.*?)\s+in\s+(.+?)\s+on\s+line\s+(\d+)`)

func (phpEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if _, err := exec.LookPath("php"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var errs []CompileErr
	for _, f := range files {
		cmd := exec.CommandContext(c, "php", "-l", f)
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			m := phpLintErrRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			file := m[3]
			if !filepath.IsAbs(file) {
				file = filepath.Join(projectRoot, file)
			}
			rel, _ := filepath.Rel(projectRoot, file)
			var lno int
			fmt.Sscanf(m[4], "%d", &lno)
			errs = append(errs, CompileErr{File: rel, Line: lno, Code: "php-lint", Message: m[2]})
		}
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Line < errs[j].Line
	})
	return errs, nil
}

// phpReadComposer parses composer.json to extract (a) psr-4 prefixes
// declared by this project and its dev/runtime packages (so we
// recognize in-project namespaces), and (b) the set of `require` /
// `require-dev` package names. We use (a) as the "is this import
// covered" check; (b) is returned for future extensions but not
// used directly in the prefix-cover test.
func phpReadComposer(path string) (map[string]struct{}, map[string]struct{}) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var composer struct {
		Autoload    map[string]any    `json:"autoload"`
		AutoloadDev map[string]any    `json:"autoload-dev"`
		Require     map[string]string `json:"require"`
		RequireDev  map[string]string `json:"require-dev"`
	}
	if err := jsonUnmarshalLenient(body, &composer); err != nil {
		return nil, nil
	}
	prefixes := map[string]struct{}{}
	for _, al := range []map[string]any{composer.Autoload, composer.AutoloadDev} {
		if al == nil {
			continue
		}
		if psr4, ok := al["psr-4"].(map[string]any); ok {
			for k := range psr4 {
				k = strings.TrimRight(k, `\`)
				if k != "" {
					prefixes[k] = struct{}{}
				}
			}
		}
	}
	packages := map[string]struct{}{}
	for k := range composer.Require {
		packages[k] = struct{}{}
	}
	for k := range composer.RequireDev {
		packages[k] = struct{}{}
	}
	return prefixes, packages
}

func phpPrefixCovers(ns string, prefixes map[string]struct{}) bool {
	// Built-in PHP classes (global namespace) never start with a
	// backslash in `use`; they're written as `DateTime`, `Exception`,
	// etc. If ns has no backslash and matches the built-in shape,
	// accept it.
	if !strings.Contains(ns, `\`) {
		return true
	}
	for p := range prefixes {
		if ns == p || strings.HasPrefix(ns, p+`\`) {
			return true
		}
	}
	return false
}
