// Package plan — integrity_c.go
//
// C ecosystem. Validates `#include "x.h"` / `#include <x.h>` by
// checking for the header in the project tree or the system include
// path. No universal manifest (C projects vary: Makefile, CMakeLists,
// meson, etc.), so the gate focuses on header-resolution + gcc
// -fsyntax-only for compile regression. No barrel concept.
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
	RegisterEcosystem(&cEcosystem{})
}

type cEcosystem struct{}

func (cEcosystem) Name() string { return "c" }

func (cEcosystem) Owns(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".c" || ext == ".h" || ext == ".cpp" || ext == ".cc" || ext == ".cxx" || ext == ".hpp" || ext == ".hh"
}

var cIncludeRE = regexp.MustCompile(`(?m)^\s*#\s*include\s*(?:"([^"]+)"|<([^>]+)>)`)

func (cEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	// Project-local headers: index every .h/.hpp in tree.
	localHeaders := cCollectHeaders(projectRoot)
	systemHeaders := cSystemHeaderSet() // conservative C stdlib whitelist
	manifest := cFindManifest(projectRoot)
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range cIncludeRE.FindAllStringSubmatch(string(body), -1) {
			header := m[1]
			if header == "" {
				header = m[2]
			}
			if header == "" {
				continue
			}
			base := filepath.Base(header)
			if _, ok := systemHeaders[base]; ok {
				continue
			}
			if _, ok := systemHeaders[header]; ok {
				continue
			}
			// Local project header resolution: any file in tree
			// matching the basename or full relative path.
			if _, ok := localHeaders[base]; ok {
				continue
			}
			if _, ok := localHeaders[header]; ok {
				continue
			}
			key := f + "::" + header
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			mani := manifest
			if mani == "" {
				mani = "(no build manifest detected)"
			} else {
				mani, _ = filepath.Rel(projectRoot, mani)
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: header,
				Manifest:   mani,
				AddCommand: fmt.Sprintf("install system dev package providing %s (e.g., apt-get install libX-dev) or add include path / vendor the header", header),
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

func (cEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// gccErrRE matches:
//   file.c:12:3: error: 'foo' undeclared
var gccErrRE = regexp.MustCompile(`^(.+?\.(?:c|h|cpp|cc|cxx|hpp|hh)):(\d+):(?:(\d+):)?\s*(?:fatal\s+)?error:\s*(.*)$`)

func (cEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if len(files) == 0 {
		return nil, nil
	}
	// Prefer clang, then gcc.
	var bin string
	if _, err := exec.LookPath("clang"); err == nil {
		bin = "clang"
	} else if _, err := exec.LookPath("gcc"); err == nil {
		bin = "gcc"
	} else if _, err := exec.LookPath("cc"); err == nil {
		bin = "cc"
	} else {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var all []CompileErr
	for _, f := range files {
		args := []string{"-fsyntax-only", f}
		cmd := exec.CommandContext(c, bin, args...)
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			m := gccErrRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			file := m[1]
			if !filepath.IsAbs(file) {
				file = filepath.Join(projectRoot, file)
			}
			rel, _ := filepath.Rel(projectRoot, file)
			var lno, col int
			fmt.Sscanf(m[2], "%d", &lno)
			if m[3] != "" {
				fmt.Sscanf(m[3], "%d", &col)
			}
			all = append(all, CompileErr{File: rel, Line: lno, Column: col, Code: bin, Message: m[4]})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all, nil
}

func cCollectHeaders(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "build" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".h" && ext != ".hpp" && ext != ".hh" {
			return nil
		}
		out[d.Name()] = struct{}{}
		rel, err := filepath.Rel(projectRoot, path)
		if err == nil {
			out[rel] = struct{}{}
		}
		return nil
	})
	return out
}

func cFindManifest(projectRoot string) string {
	for _, name := range []string{"CMakeLists.txt", "Makefile", "meson.build", "GNUmakefile", "configure.ac", "configure.in"} {
		p := filepath.Join(projectRoot, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// cSystemHeaderSet is a conservative set of C / POSIX / libc /
// libstdc++ common headers that we should never report as missing.
func cSystemHeaderSet() map[string]struct{} {
	names := []string{
		// C stdlib
		"assert.h", "complex.h", "ctype.h", "errno.h", "fenv.h", "float.h",
		"inttypes.h", "iso646.h", "limits.h", "locale.h", "math.h", "setjmp.h",
		"signal.h", "stdalign.h", "stdarg.h", "stdatomic.h", "stdbool.h", "stddef.h",
		"stdint.h", "stdio.h", "stdlib.h", "stdnoreturn.h", "string.h", "tgmath.h",
		"threads.h", "time.h", "uchar.h", "wchar.h", "wctype.h",
		// POSIX
		"unistd.h", "fcntl.h", "sys/types.h", "sys/stat.h", "sys/socket.h",
		"sys/wait.h", "sys/mman.h", "sys/select.h", "sys/ioctl.h", "sys/time.h",
		"sys/resource.h", "sys/utsname.h", "netinet/in.h", "netinet/tcp.h",
		"arpa/inet.h", "netdb.h", "pthread.h", "dlfcn.h", "dirent.h", "grp.h",
		"pwd.h", "poll.h", "syslog.h", "termios.h", "utime.h", "sched.h",
		"regex.h", "wordexp.h", "glob.h", "ftw.h", "fnmatch.h",
		// C++ stdlib
		"algorithm", "array", "atomic", "bitset", "chrono", "complex", "condition_variable",
		"deque", "exception", "filesystem", "forward_list", "fstream", "functional",
		"future", "initializer_list", "iomanip", "ios", "iostream", "istream",
		"iterator", "limits", "list", "locale", "map", "memory", "mutex", "new",
		"numeric", "optional", "ostream", "queue", "random", "ratio", "regex",
		"scoped_allocator", "set", "shared_mutex", "sstream", "stack", "stdexcept",
		"streambuf", "string", "string_view", "system_error", "thread", "tuple",
		"type_traits", "typeindex", "typeinfo", "unordered_map", "unordered_set",
		"utility", "valarray", "variant", "vector",
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}
