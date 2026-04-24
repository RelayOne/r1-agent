// Package plan — integrity_arduino.go
//
// Arduino ecosystem. .ino sketches plus their companion .h/.cpp
// files. Validates `#include <Library.h>` against declared libraries
// (library.properties depends= line, or a project-level
// libraries.txt). Compile regression uses arduino-cli compile when
// available. No barrel concept.
package plan

import (
	"bufio"
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
	RegisterEcosystem(&arduinoEcosystem{})
}

type arduinoEcosystem struct{}

func (arduinoEcosystem) Name() string { return "arduino" }

// .ino is unambiguous; .cpp/.h inside an Arduino project would
// already be owned by the C ecosystem (registered earlier), so we
// only claim .ino here. Projects that mix sketches with C files
// still get full coverage via two ecosystems running in parallel.
func (arduinoEcosystem) Owns(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".ino"
}

var arduinoIncludeRE = regexp.MustCompile(`(?m)^\s*#\s*include\s*<([^>]+)>`)

func (arduinoEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	declared := arduinoDeclaredLibraries(projectRoot)
	coreHeaders := arduinoCoreHeaderSet()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range arduinoIncludeRE.FindAllStringSubmatch(string(body), -1) {
			header := m[1]
			base := filepath.Base(header)
			if _, ok := coreHeaders[base]; ok {
				continue
			}
			// Library name often equals header without .h — try both.
			libName := strings.TrimSuffix(base, ".h")
			if _, ok := declared[libName]; ok {
				continue
			}
			if _, ok := declared[base]; ok {
				continue
			}
			key := f + "::" + header
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			manifest := arduinoFindManifest(projectRoot)
			mani := manifest
			if mani == "" {
				mani = "library.properties or libraries.txt"
			} else {
				mani, _ = filepath.Rel(projectRoot, mani)
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: header,
				Manifest:   mani,
				AddCommand: fmt.Sprintf("arduino-cli lib install %q and add %q to depends= in %s", libName, libName, mani),
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

func (arduinoEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

var arduinoErrRE = regexp.MustCompile(`^(.+?\.ino):(\d+):(?:(\d+):)?\s*error:\s*(.*)$`)

func (arduinoEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if _, err := exec.LookPath("arduino-cli"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	sketches := arduinoSketchDirs(files)
	if len(sketches) == 0 {
		return nil, nil
	}
	var all []CompileErr
	for _, dir := range sketches {
		// FQBN is required by arduino-cli; we default to esp32:esp32:esp32
		// (Sentinel's declared target) but the gate is best-effort: if
		// the user's default FQBN is set in arduino-cli config, this
		// is ignored; otherwise the user can set ARDUINO_FQBN.
		fqbn := os.Getenv("ARDUINO_FQBN")
		if fqbn == "" {
			fqbn = "esp32:esp32:esp32"
		}
		cmd := exec.CommandContext(c, "arduino-cli", "compile", "--fqbn", fqbn, "--warnings", "default", "--dry-run", dir) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			m := arduinoErrRE.FindStringSubmatch(line)
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
			all = append(all, CompileErr{File: rel, Line: lno, Column: col, Code: "arduino-cli", Message: m[4]})
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

func arduinoSketchDirs(files []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(files))
	for _, f := range files {
		dir := filepath.Dir(f)
		if _, dup := seen[dir]; dup {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// arduinoDeclaredLibraries collects the union of libraries declared
// in library.properties (depends=) and any project-level
// libraries.txt file (newline-separated library names).
func arduinoDeclaredLibraries(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	// library.properties (when the project IS a library).
	libProps := filepath.Join(projectRoot, "library.properties")
	if body, err := os.ReadFile(libProps); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(body)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "depends=") {
				csv := strings.TrimPrefix(line, "depends=")
				for _, name := range strings.Split(csv, ",") {
					name = strings.TrimSpace(name)
					if name != "" {
						out[name] = struct{}{}
						// Also register foo as foo.h for header match.
						out[name+".h"] = struct{}{}
					}
				}
			}
		}
	}
	// libraries.txt (project convention).
	libTxt := filepath.Join(projectRoot, "libraries.txt")
	if body, err := os.ReadFile(libTxt); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			name := strings.TrimSpace(line)
			if name == "" || strings.HasPrefix(name, "#") {
				continue
			}
			out[name] = struct{}{}
			out[name+".h"] = struct{}{}
		}
	}
	return out
}

func arduinoFindManifest(projectRoot string) string {
	for _, name := range []string{"library.properties", "libraries.txt", "platform.txt"} {
		p := filepath.Join(projectRoot, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// arduinoCoreHeaderSet covers headers bundled with the Arduino core
// and the most common AVR / ESP32 / STM32 built-in headers that
// should never trip the missing-dep gate.
func arduinoCoreHeaderSet() map[string]struct{} {
	names := []string{
		"Arduino.h", "WString.h", "Print.h", "Printable.h", "Stream.h",
		"HardwareSerial.h", "Wire.h", "SPI.h", "EEPROM.h", "Servo.h",
		"SoftwareSerial.h", "Tone.h", "IPAddress.h", "Client.h", "Server.h",
		"Udp.h", "WiFi.h", "WiFiClient.h", "WiFiServer.h", "WiFiUdp.h",
		"Ethernet.h", "SD.h", "FS.h", "SPIFFS.h", "LittleFS.h",
		// ESP32
		"esp_system.h", "esp_wifi.h", "esp_log.h", "esp_err.h", "nvs.h",
		"nvs_flash.h", "driver/gpio.h", "driver/uart.h", "driver/i2c.h",
		"driver/spi_master.h", "freertos/FreeRTOS.h", "freertos/task.h",
		"freertos/queue.h", "freertos/semphr.h", "freertos/event_groups.h",
		// AVR
		"avr/io.h", "avr/interrupt.h", "avr/pgmspace.h", "avr/wdt.h",
		"util/delay.h", "util/atomic.h",
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
		out[filepath.Base(n)] = struct{}{}
	}
	return out
}
