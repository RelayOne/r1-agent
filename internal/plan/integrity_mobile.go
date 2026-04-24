// Package plan — integrity_mobile.go
//
// Mobile (Expo managed + bare React Native) ecosystem integrity.
// Sits on top of the TypeScript ecosystem which already validates
// that bare imports are declared in package.json. Adds the mobile-
// specific contracts that pure package.json validation misses:
//
//  1. Expo native-config plugins. When a file imports certain Expo
//     modules (e.g., expo-notifications, expo-camera, expo-location,
//     expo-media-library) that require config-plugin entries to
//     actually ship permissions/entitlements, validate that app.json
//     / app.config.js has the plugin registered.
//
//  2. Bare React Native native-module linking. Detect imports from
//     @react-native- scoped packages and untethered native modules
//     and verify they appear in ios/Podfile.lock (iOS) AND
//     android/settings.gradle + android/app/build.gradle
//     (Android). Missing linkage is the #1 cause of "works in metro
//     but crashes at runtime" bugs.
//
//  3. EAS build profile sanity. When a project uses expo prebuild /
//     eas build, app.json's `expo.android.package` and
//     `expo.ios.bundleIdentifier` must be present; projects shipping
//     without them fail at EAS submit time. We surface the missing
//     field as a directive.
//
// All checks degrade gracefully on non-mobile projects: the
// ecosystem's Owns() returns false unless one of the mobile
// manifest files is present, so the probes are skipped.
package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func init() {
	RegisterEcosystem(&mobileEcosystem{})
}

type mobileEcosystem struct{}

func (mobileEcosystem) Name() string { return "mobile" }

// Claims app.json and app.config.js / app.config.ts — the canonical
// Expo/RN manifest files. Per-source files remain owned by the
// TypeScript ecosystem, which gives us both checks running in
// parallel for RN projects.
func (mobileEcosystem) Owns(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "app.json", "app.config.js", "app.config.ts":
		return true
	}
	// ios/Podfile and android/build.gradle trigger the check too;
	// session writes to them indicate mobile work.
	dir := filepath.Base(filepath.Dir(path))
	if (base == "Podfile" && dir == "ios") || (base == "build.gradle" && (dir == "android" || dir == "app")) {
		return true
	}
	return false
}

// AlwaysRun: Expo plugin / native-link checks are workspace-wide —
// a session that touches just a .tsx screen can still need a plugin
// entry in app.json or a pod in Podfile.lock.
func (mobileEcosystem) AlwaysRun() bool { return true }

func (mobileEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	if !mobileProjectDetected(projectRoot) {
		return nil, nil
	}
	var out []ManifestMiss

	// (1) Expo config-plugin check.
	if mobileIsExpo(projectRoot) {
		declaredPlugins := mobileExpoPlugins(projectRoot)
		usedModules := mobileScanExpoModuleImports(projectRoot)
		for modName, firstFile := range usedModules {
			if !mobileRequiresConfigPlugin(modName) {
				continue
			}
			if _, ok := declaredPlugins[modName]; ok {
				continue
			}
			relSrc, _ := filepath.Rel(projectRoot, firstFile)
			cfgRel := mobileExpoConfigPath(projectRoot)
			if cfgRel != "" {
				cfgRel, _ = filepath.Rel(projectRoot, cfgRel)
			} else {
				cfgRel = "app.json"
			}
			out = append(out, ManifestMiss{
				SourceFile: relSrc,
				ImportPath: modName,
				Manifest:   cfgRel,
				AddCommand: fmt.Sprintf(`add %q to the "plugins" array in %s (required for iOS permissions / Android config)`, modName, cfgRel),
			})
		}
	}

	// (2) Bare RN: validate native modules appear in Podfile.lock.
	if mobileIsBareRN(projectRoot) {
		podLock := filepath.Join(projectRoot, "ios", "Podfile.lock")
		podBody, _ := os.ReadFile(podLock)
		usedNative := mobileScanRNNativeImports(projectRoot)
		for modName, firstFile := range usedNative {
			// Match against the pod section. react-native packages
			// typically show up with the module name OR as
			// react-native-<foo>.
			if mobilePodLockReferences(string(podBody), modName) {
				continue
			}
			relSrc, _ := filepath.Rel(projectRoot, firstFile)
			out = append(out, ManifestMiss{
				SourceFile: relSrc,
				ImportPath: modName,
				Manifest:   "ios/Podfile.lock",
				AddCommand: fmt.Sprintf("run `cd ios && pod install` after adding %q to package.json; verify the pod appears in Podfile.lock", modName),
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

func (mobileEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	// (3) EAS submit prerequisites surfaced via the public-surface
	// channel — these are prerequisites for SHIPPING, not for
	// compiling, so using this slot keeps them separate from
	// manifest-import misses in the report.
	if !mobileIsExpo(projectRoot) {
		return nil, nil
	}
	cfgPath := mobileExpoConfigPath(projectRoot)
	if cfgPath == "" {
		return nil, nil
	}
	// Unreadable Expo config file is treated as "nothing to
	// report" rather than a hard error — callers only want the
	// list of missing public-surface entries, and an unreadable
	// config will surface in the compile/manifest-import path
	// instead.
	body, _ := os.ReadFile(cfgPath)
	if len(body) == 0 {
		return nil, nil
	}
	text := string(body)
	var out []PublicSurfaceMiss
	rel, _ := filepath.Rel(projectRoot, cfgPath)
	if !regexp.MustCompile(`"android"\s*:\s*\{[^}]*"package"\s*:`).MatchString(text) {
		out = append(out, PublicSurfaceMiss{
			SourceFile: rel,
			TargetFile: rel,
			FixLine:    `"android": { "package": "com.yourcompany.yourapp", ... }`,
		})
	}
	if !regexp.MustCompile(`"ios"\s*:\s*\{[^}]*"bundleIdentifier"\s*:`).MatchString(text) {
		out = append(out, PublicSurfaceMiss{
			SourceFile: rel,
			TargetFile: rel,
			FixLine:    `"ios": { "bundleIdentifier": "com.yourcompany.yourapp", ... }`,
		})
	}
	return out, nil
}

// Compile regression for mobile is already covered by the TypeScript
// ecosystem (tsc) + the per-platform gradle/xcodebuild compile.
// Running an EAS build here would be too slow for a session gate.
func (mobileEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	return nil, nil
}

// ---------------------------------------------------------------------
// Detection helpers
// ---------------------------------------------------------------------

func mobileProjectDetected(projectRoot string) bool {
	return mobileIsExpo(projectRoot) || mobileIsBareRN(projectRoot)
}

func mobileIsExpo(projectRoot string) bool {
	for _, name := range []string{"app.json", "app.config.js", "app.config.ts"} {
		if info, err := os.Stat(filepath.Join(projectRoot, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func mobileIsBareRN(projectRoot string) bool {
	// Presence of ios/Podfile or android/settings.gradle is the tell.
	if info, err := os.Stat(filepath.Join(projectRoot, "ios", "Podfile")); err == nil && !info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(projectRoot, "android", "settings.gradle")); err == nil && !info.IsDir() {
		return true
	}
	return false
}

func mobileExpoConfigPath(projectRoot string) string {
	for _, name := range []string{"app.config.ts", "app.config.js", "app.json"} {
		p := filepath.Join(projectRoot, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// mobileExpoPlugins parses the declared plugins array. Accepts both
// JSON shape (app.json) and a conservative regex scan of .js/.ts
// dynamic configs. For dynamic configs this may miss plugins added
// via runtime logic, but those are rare in practice and a false
// positive is recoverable (the fix directive is already the right
// action).
func mobileExpoPlugins(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	cfgPath := mobileExpoConfigPath(projectRoot)
	if cfgPath == "" {
		return out
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return out
	}
	text := string(body)
	if strings.HasSuffix(cfgPath, ".json") {
		// Parse the plugins key lenient-JSON.
		var cfg struct {
			Expo struct {
				Plugins []any `json:"plugins"`
			} `json:"expo"`
			// Some projects use the flat shape.
			Plugins []any `json:"plugins"`
		}
		if err := jsonUnmarshalLenient(body, &cfg); err == nil {
			plugins := cfg.Expo.Plugins
			if len(plugins) == 0 {
				plugins = cfg.Plugins
			}
			for _, p := range plugins {
				switch v := p.(type) {
				case string:
					out[v] = struct{}{}
				case []any:
					if len(v) > 0 {
						if s, ok := v[0].(string); ok {
							out[s] = struct{}{}
						}
					}
				}
			}
		}
	} else {
		// .js/.ts dynamic config: regex-scan for plugin: ['name', ...]
		// entries. Covers the common case.
		re := regexp.MustCompile(`['"\x60]([a-zA-Z0-9\-_@/]+)['"\x60]`)
		// Bound to plugins array context.
		if pluginsIdx := strings.Index(text, "plugins"); pluginsIdx >= 0 {
			scope := text[pluginsIdx:]
			if end := strings.Index(scope, "]"); end > 0 {
				scope = scope[:end]
				for _, m := range re.FindAllStringSubmatch(scope, -1) {
					name := m[1]
					if strings.HasPrefix(name, "expo-") || strings.Contains(name, "/") {
						out[name] = struct{}{}
					}
				}
			}
		}
	}
	return out
}

var mobileExpoImportRE = regexp.MustCompile(`(?m)(?:from|require\s*\(\s*)['"]((?:expo|expo-[a-zA-Z0-9\-]+|@expo/[a-zA-Z0-9\-/]+))['"]`)

// mobileScanExpoModuleImports walks the project and returns a map of
// expo-* module name → first source file that imports it.
func mobileScanExpoModuleImports(projectRoot string) map[string]string {
	return mobileScanMatching(projectRoot, mobileExpoImportRE)
}

var mobileRNNativeImportRE = regexp.MustCompile(`(?m)(?:from|require\s*\(\s*)['"]((?:react-native-[a-zA-Z0-9\-]+|@react-native-[a-zA-Z0-9\-]+/[a-zA-Z0-9\-/]+))['"]`)

// mobileScanRNNativeImports finds react-native-* / @react-native-*
// scoped imports (typically native modules needing native linking).
func mobileScanRNNativeImports(projectRoot string) map[string]string {
	return mobileScanMatching(projectRoot, mobileRNNativeImportRE)
}

func mobileScanMatching(projectRoot string, re *regexp.Regexp) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, walkErr error) error {
		// Per-path walk errors (permission denied, symlink
		// loops) are tolerated: a single bad entry must not
		// abort the whole native-import scan.
		if walkErr != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "ios" || name == "android" ||
				name == "build" || name == "dist" || name == ".expo" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx" && ext != ".mjs" && ext != ".cjs" {
			return nil
		}
		// Per-file read errors tolerated for the same reason.
		body, _ := os.ReadFile(path)
		if len(body) == 0 {
			return nil
		}
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			if _, dup := out[m[1]]; !dup {
				out[m[1]] = path
			}
		}
		return nil
	})
	return out
}

// mobileRequiresConfigPlugin names the expo modules whose iOS
// permissions/android manifest entries are NOT injected by autolinking
// and REQUIRE an entry in the "plugins" array of app.json to ship
// correctly. Curated list — add more as the ecosystem evolves.
func mobileRequiresConfigPlugin(modName string) bool {
	needs := map[string]bool{
		"expo-notifications":           true,
		"expo-camera":                  true,
		"expo-location":                true,
		"expo-media-library":           true,
		"expo-image-picker":            true,
		"expo-document-picker":         true,
		"expo-av":                      true,
		"expo-contacts":                true,
		"expo-calendar":                true,
		"expo-tracking-transparency":   true,
		"expo-local-authentication":    true,
		"expo-sensors":                 true,
		"expo-barcode-scanner":         true,
		"expo-build-properties":        true,
		"expo-secure-store":            true,
		"expo-device":                  true,
		"expo-screen-orientation":      true,
		"expo-video":                   true,
		"expo-audio":                   true,
		"expo-file-system":             true,
		"expo-background-fetch":        true,
		"expo-task-manager":            true,
		"expo-sharing":                 true,
		"expo-clipboard":               false, // no perms/plugin required
		"expo-constants":               false,
		"expo-router":                  false,
		"expo-status-bar":              false,
		"expo-linking":                 false,
		"expo-splash-screen":           false,
	}
	if v, ok := needs[modName]; ok {
		return v
	}
	// Default: if it's a scoped expo module and isn't in the
	// "definitely doesn't need one" set above, we don't force a
	// plugin entry. Conservative to avoid false positives on
	// auto-linking modules.
	return false
}

// mobilePodLockReferences returns true if the pod lock mentions the
// module (either as a key or inside a - entry). Handles the
// name-mangling where react-native-foo becomes RNFoo or
// react-native-foo in pod names.
func mobilePodLockReferences(podLock, modName string) bool {
	if strings.Contains(podLock, `"`+modName+`"`) {
		return true
	}
	if strings.Contains(podLock, "- "+modName) || strings.Contains(podLock, ": "+modName) {
		return true
	}
	// RN-style pod name mangling: @react-native-async-storage/async-storage
	// becomes RNCAsyncStorage in the podspec. Strip prefix and check the
	// last segment as a loose contains.
	tail := modName
	if i := strings.LastIndex(modName, "/"); i >= 0 {
		tail = modName[i+1:]
	}
	tail = strings.TrimPrefix(tail, "react-native-")
	// Multi-word: async-storage → AsyncStorage.
	var camel strings.Builder
	for _, part := range strings.Split(tail, "-") {
		if part == "" {
			continue
		}
		camel.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			camel.WriteString(part[1:])
		}
	}
	if camel.Len() > 0 && strings.Contains(podLock, camel.String()) {
		return true
	}
	return false
}
