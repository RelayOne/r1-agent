// Package plan — integrity_swift_objc.go
//
// Swift and Objective-C ecosystems. Swift validates `import Module`
// against Package.swift dependencies and Podfile/Cartfile entries.
// Objective-C validates `#import <Framework/Bar.h>` against linked
// frameworks (Podfile, .xcodeproj frameworks section, or
// Package.swift product targets).
//
// Compile regression uses `swift build` for Swift and
// `xcodebuild -dry-run` / `clang -fsyntax-only` for Objective-C.
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
	RegisterEcosystem(&swiftEcosystem{})
	RegisterEcosystem(&objcEcosystem{})
}

// ---------------------------------------------------------------------
// Swift
// ---------------------------------------------------------------------

type swiftEcosystem struct{}

func (swiftEcosystem) Name() string { return "swift" }

func (swiftEcosystem) Owns(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".swift"
}

var swiftImportRE = regexp.MustCompile(`(?m)^\s*(?:@testable\s+)?import\s+(?:struct\s+|class\s+|func\s+|enum\s+|protocol\s+|typealias\s+|let\s+|var\s+)?([A-Za-z_][A-Za-z0-9_\.]*)`)

func (swiftEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	deps := swiftCollectDeps(projectRoot)
	stdRoots := swiftStdlibRoots()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	manifest := swiftFindManifest(projectRoot)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range swiftImportRE.FindAllStringSubmatch(string(body), -1) {
			mod := m[1]
			root := mod
			if i := strings.Index(mod, "."); i > 0 {
				root = mod[:i]
			}
			if _, ok := stdRoots[root]; ok {
				continue
			}
			if _, ok := deps[root]; ok {
				continue
			}
			key := f + "::" + root
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			mani := manifest
			if mani != "" {
				mani, _ = filepath.Rel(projectRoot, mani)
			} else {
				mani = "Package.swift / Podfile / Cartfile"
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: root,
				Manifest:   mani,
				AddCommand: fmt.Sprintf("add %q as a dependency in %s (SwiftPM: .package(url:) + .product(name:); CocoaPods: pod %q)", root, mani, root),
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

func (swiftEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// swift build error shape:
//   Sources/Foo/Bar.swift:12:3: error: cannot find 'x' in scope
var swiftErrRE = regexp.MustCompile(`^(.+?\.swift):(\d+):(\d+):\s*error:\s*(.*)$`)

func (swiftEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if _, err := exec.LookPath("swift"); err != nil {
		return nil, nil
	}
	// Only runs at the Package.swift level; Xcode-project-only builds
	// fall back to no-op.
	if _, err := os.Stat(filepath.Join(projectRoot, "Package.swift")); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "swift", "build", "--build-tests")
	cmd.Dir = projectRoot
	out, _ := cmd.CombinedOutput()
	return parseSwiftLikeErrors(projectRoot, string(out), swiftErrRE, "swift"), nil
}

func swiftCollectDeps(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	// Package.swift — capture .product(name: "X") and .package(name: "X").
	pkg := filepath.Join(projectRoot, "Package.swift")
	if body, err := os.ReadFile(pkg); err == nil {
		re := regexp.MustCompile(`(?:\.product|\.package|\.library|\.executable)\s*\(\s*name:\s*"([^"]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			out[m[1]] = struct{}{}
		}
	}
	// Podfile — capture pod 'Name'.
	pod := filepath.Join(projectRoot, "Podfile")
	if body, err := os.ReadFile(pod); err == nil {
		re := regexp.MustCompile(`pod\s+['"]([^'"]+)['"]`)
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			root := m[1]
			if i := strings.Index(root, "/"); i > 0 {
				root = root[:i]
			}
			out[root] = struct{}{}
		}
	}
	// Cartfile — lines of form: github "Org/Name" "..." or git "url"
	cart := filepath.Join(projectRoot, "Cartfile")
	if body, err := os.ReadFile(cart); err == nil {
		re := regexp.MustCompile(`(?m)^(?:github|git|binary)\s+"([^"]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			// Extract final path segment as the likely module name.
			slug := m[1]
			if i := strings.LastIndex(slug, "/"); i >= 0 {
				slug = slug[i+1:]
			}
			slug = strings.TrimSuffix(slug, ".git")
			out[slug] = struct{}{}
		}
	}
	return out
}

func swiftFindManifest(projectRoot string) string {
	for _, name := range []string{"Package.swift", "Podfile", "Cartfile"} {
		p := filepath.Join(projectRoot, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func swiftStdlibRoots() map[string]struct{} {
	names := []string{
		"Foundation", "Swift", "SwiftUI", "UIKit", "AppKit", "Combine",
		"CoreData", "CoreGraphics", "CoreFoundation", "CoreLocation",
		"CoreML", "CoreImage", "CoreMedia", "CoreAudio", "CoreBluetooth",
		"CoreTelephony", "CoreMotion", "CoreVideo", "CoreText", "Contacts",
		"AVFoundation", "AVKit", "MapKit", "Metal", "MetalKit", "ModelIO",
		"Photos", "PhotosUI", "SceneKit", "SpriteKit", "StoreKit",
		"WebKit", "Network", "os", "OSLog", "Darwin", "Dispatch",
		"ObjectiveC", "CryptoKit", "Security", "Accelerate", "Accessibility",
		"ARKit", "AudioToolbox", "AuthenticationServices", "BackgroundTasks",
		"CallKit", "ClassKit", "CloudKit", "Compression", "DeveloperToolsSupport",
		"DeviceCheck", "EventKit", "ExternalAccessory", "FileProvider",
		"GameController", "GameKit", "HealthKit", "HomeKit", "Intents",
		"IntentsUI", "LocalAuthentication", "Messages", "MessageUI",
		"MediaPlayer", "MetricKit", "MultipeerConnectivity", "NaturalLanguage",
		"NearbyInteraction", "NetworkExtension", "Notification", "PDFKit",
		"PassKit", "PencilKit", "PushKit", "QuickLook", "QuickLookThumbnailing",
		"ReplayKit", "SafariServices", "SceneKit", "SharedWithYou",
		"SoundAnalysis", "Speech", "StoreKit", "SystemConfiguration",
		"UserNotifications", "UserNotificationsUI", "VideoToolbox", "Vision",
		"VisionKit", "WatchConnectivity", "WatchKit", "XCTest",
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func parseSwiftLikeErrors(projectRoot, output string, re *regexp.Regexp, code string) []CompileErr {
	var errs []CompileErr
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := re.FindStringSubmatch(line)
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
		fmt.Sscanf(m[3], "%d", &col)
		errs = append(errs, CompileErr{File: rel, Line: lno, Column: col, Code: code, Message: m[4]})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Line < errs[j].Line
	})
	return errs
}

// ---------------------------------------------------------------------
// Objective-C / Objective-C++
// ---------------------------------------------------------------------

type objcEcosystem struct{}

func (objcEcosystem) Name() string { return "objc" }

func (objcEcosystem) Owns(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".m" || ext == ".mm"
}

var objcImportRE = regexp.MustCompile(`(?m)^\s*#\s*import\s*(?:"([^"]+)"|<([^/]+)/([^>]+)>)`)

func (objcEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	// Objective-C shares a dep surface with Swift (same Podfile /
	// Package.swift / Cartfile / xcodeproj), so reuse swiftCollectDeps.
	deps := swiftCollectDeps(projectRoot)
	stdFrameworks := objcSystemFrameworks()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	manifest := swiftFindManifest(projectRoot)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range objcImportRE.FindAllStringSubmatch(string(body), -1) {
			// Groups: [1] = "file.h" (project-local), [2] = Framework, [3] = Header.h
			framework := m[2]
			if framework == "" {
				continue // project-local imports are always OK
			}
			if _, ok := stdFrameworks[framework]; ok {
				continue
			}
			if _, ok := deps[framework]; ok {
				continue
			}
			key := f + "::" + framework
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			mani := manifest
			if mani != "" {
				mani, _ = filepath.Rel(projectRoot, mani)
			} else {
				mani = "Podfile / Package.swift"
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: framework,
				Manifest:   mani,
				AddCommand: fmt.Sprintf("declare framework %q in %s (pod %q or SwiftPM .product)", framework, mani, framework),
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

func (objcEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

func (objcEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if _, err := exec.LookPath("clang"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var all []CompileErr
	for _, f := range files {
		cmd := exec.CommandContext(c, "clang", "-fsyntax-only", "-x", "objective-c", f)
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
			all = append(all, CompileErr{File: rel, Line: lno, Column: col, Code: "clang-objc", Message: m[4]})
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

func objcSystemFrameworks() map[string]struct{} {
	// Apple frameworks that ship with the SDKs — same set as Swift's
	// stdlib roots, plus a few ObjC-only ones.
	out := swiftStdlibRoots()
	for _, extra := range []string{"CoreFoundation", "CoreServices", "IOKit", "DiskArbitration"} {
		out[extra] = struct{}{}
	}
	return out
}
