// Package plan — integrity_java.go
//
// Java ecosystem. Validates `import foo.bar.Baz;` against declared
// Maven/Gradle dependencies and runs javac / mvn / gradle for
// compile-regression detection. No barrel concept — Java packages
// are directory-structured and imports are resolved by classpath.
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
	RegisterEcosystem(&javaEcosystem{})
}

type javaEcosystem struct{}

func (javaEcosystem) Name() string { return "java" }

func (javaEcosystem) Owns(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".java" || ext == ".kt" || ext == ".kts"
}

var javaImportRE = regexp.MustCompile(`(?m)^\s*import(?:\s+static)?\s+([a-zA-Z_][\w.]+)\s*;`)

func (javaEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	manifest, groupIDs := javaFindManifest(projectRoot)
	if manifest == "" {
		return nil, nil
	}
	stdRoots := javaStdlibRoots()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		localPkgs := javaLocalPackages(projectRoot)
		for _, m := range javaImportRE.FindAllStringSubmatch(string(body), -1) {
			imp := m[1]
			// Drop trailing class component: java.util.List → java.util
			// for stdlib matching, but the full path for dep matching.
			root := imp
			if i := strings.Index(imp, "."); i > 0 {
				root = imp[:i]
			}
			if _, ok := stdRoots[root]; ok {
				continue
			}
			if javaLocalCovers(imp, localPkgs) {
				continue
			}
			// Dep match: compare progressively shorter prefixes of the
			// import against the groupId set (e.g., import
			// com.fasterxml.jackson.databind.ObjectMapper matches
			// groupId com.fasterxml.jackson.core:jackson-databind ~
			// "com.fasterxml.jackson").
			if javaDepCovers(imp, groupIDs) {
				continue
			}
			key := f + "::" + imp
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			relMani, _ := filepath.Rel(projectRoot, manifest)
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: imp,
				Manifest:   relMani,
				AddCommand: fmt.Sprintf("add a dependency covering %q to %s", imp, relMani),
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

// Java has no barrel concept.
func (javaEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// javac emits errors as:
//   src/Foo.java:12: error: cannot find symbol
var javacErrRE = regexp.MustCompile(`^(.+?\.(?:java|kt|kts)):(\d+):\s*error:\s*(.*)$`)

func (javaEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if len(files) == 0 {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	// Prefer maven/gradle (honors classpath) then fall back to javac.
	if _, err := os.Stat(filepath.Join(projectRoot, "pom.xml")); err == nil {
		if _, err := exec.LookPath("mvn"); err == nil {
			cmd := exec.CommandContext(c, "mvn", "-q", "-o", "compile")
			cmd.Dir = projectRoot
			out, _ := cmd.CombinedOutput()
			return javaParseErrors(projectRoot, string(out)), nil
		}
	}
	gradleBuild := filepath.Join(projectRoot, "build.gradle")
	gradleKts := filepath.Join(projectRoot, "build.gradle.kts")
	if _, err := os.Stat(gradleBuild); err == nil {
	} else if _, err := os.Stat(gradleKts); err != nil {
		gradleBuild = ""
	}
	if gradleBuild != "" {
		wrapper := filepath.Join(projectRoot, "gradlew")
		bin := "gradle"
		if _, err := os.Stat(wrapper); err == nil {
			bin = wrapper
		}
		if _, err := exec.LookPath(bin); err == nil || bin == wrapper {
			cmd := exec.CommandContext(c, bin, "-q", "compileJava")
			cmd.Dir = projectRoot
			out, _ := cmd.CombinedOutput()
			return javaParseErrors(projectRoot, string(out)), nil
		}
	}
	if _, err := exec.LookPath("javac"); err == nil {
		args := append([]string{"-d", os.TempDir(), "-Xmaxerrs", "200"}, files...)
		cmd := exec.CommandContext(c, "javac", args...) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		return javaParseErrors(projectRoot, string(out)), nil
	}
	return nil, nil
}

func javaParseErrors(projectRoot, output string) []CompileErr {
	lines := strings.Split(output, "\n")
	errs := make([]CompileErr, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := javacErrRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(projectRoot, file)
		}
		rel, _ := filepath.Rel(projectRoot, file)
		var lno int
		fmt.Sscanf(m[2], "%d", &lno)
		errs = append(errs, CompileErr{File: rel, Line: lno, Code: "javac", Message: m[3]})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Line < errs[j].Line
	})
	return errs
}

// javaFindManifest returns the first of pom.xml / build.gradle /
// build.gradle.kts and extracts a groupId set (prefixes we'll treat
// as "has a dep covering this import").
func javaFindManifest(projectRoot string) (string, map[string]struct{}) {
	for _, name := range []string{"pom.xml", "build.gradle.kts", "build.gradle"} {
		p := filepath.Join(projectRoot, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, javaReadDepPrefixes(p)
		}
	}
	return "", nil
}

func javaReadDepPrefixes(path string) map[string]struct{} {
	out := map[string]struct{}{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	text := string(body)
	if strings.HasSuffix(path, ".xml") {
		// Maven: <groupId>x.y.z</groupId> — capture every occurrence.
		re := regexp.MustCompile(`<groupId>\s*([^<]+)\s*</groupId>`)
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out[strings.TrimSpace(m[1])] = struct{}{}
		}
	} else {
		// Gradle: "group:artifact:version" strings.
		re := regexp.MustCompile(`["']([a-zA-Z0-9_.\-]+:[a-zA-Z0-9_.\-]+(?::[^"']+)?)["']`)
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			parts := strings.SplitN(m[1], ":", 2)
			if len(parts) >= 1 {
				out[parts[0]] = struct{}{}
			}
		}
	}
	return out
}

func javaDepCovers(imp string, prefixes map[string]struct{}) bool {
	for p := range prefixes {
		if p == "" {
			continue
		}
		if imp == p || strings.HasPrefix(imp, p+".") {
			return true
		}
	}
	return false
}

// javaLocalPackages returns declared top-level packages from any
// Java/Kotlin source under projectRoot. Covers same-project imports
// without having to resolve a classpath.
func javaLocalPackages(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	re := regexp.MustCompile(`(?m)^\s*package\s+([a-zA-Z_][\w.]+)\s*;?`)
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "target" || name == "build" || name == ".git" || name == ".gradle" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".java" && ext != ".kt" && ext != ".kts" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if m := re.FindStringSubmatch(string(body)); m != nil {
			out[m[1]] = struct{}{}
		}
		return nil
	})
	return out
}

func javaLocalCovers(imp string, pkgs map[string]struct{}) bool {
	for p := range pkgs {
		if imp == p || strings.HasPrefix(imp, p+".") {
			return true
		}
	}
	return false
}

// javaStdlibRoots: JDK and Kotlin stdlib top-level roots.
func javaStdlibRoots() map[string]struct{} {
	names := []string{"java", "javax", "jdk", "com.sun", "sun", "kotlin", "kotlinx"}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		// Store the first segment only (kotlinx → kotlinx, com.sun → com).
		first := n
		if i := strings.Index(n, "."); i > 0 {
			first = n[:i]
		}
		out[first] = struct{}{}
	}
	return out
}
