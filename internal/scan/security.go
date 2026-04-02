package scan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SecuritySurface categorizes security-relevant code.
type SecuritySurface struct {
	Category string `json:"category"` // auth, crypto, network, filesystem, injection, secrets
	File     string `json:"file"`
	Line     int    `json:"line"`
	Pattern  string `json:"pattern"`
	Risk     string `json:"risk"` // high, medium, low
	Note     string `json:"note"`
}

// SecurityMap is the complete security surface of a codebase.
type SecurityMap struct {
	Surfaces     []SecuritySurface `json:"surfaces"`
	FilesScanned int               `json:"files_scanned"`
}

type securityRule struct {
	category string
	risk     string
	pattern  *regexp.Regexp
	note     string
	exts     []string
}

var securityRules = []securityRule{
	// Auth
	{category: "auth", risk: "high", pattern: regexp.MustCompile(`(?i)(jwt|jsonwebtoken|jose|passport)`), note: "JWT/auth library usage", exts: []string{".ts", ".js", ".go", ".py"}},
	{category: "auth", risk: "high", pattern: regexp.MustCompile(`(?i)(bcrypt|argon2|scrypt|pbkdf2)`), note: "Password hashing", exts: []string{".ts", ".js", ".go", ".py", ".rs"}},
	{category: "auth", risk: "medium", pattern: regexp.MustCompile(`(?i)(session|cookie|csrf|cors)`), note: "Session/cookie management"},
	{category: "auth", risk: "high", pattern: regexp.MustCompile(`(?i)(oauth|openid|saml|sso)`), note: "OAuth/SSO integration"},

	// Crypto
	{category: "crypto", risk: "high", pattern: regexp.MustCompile(`(?i)(crypto\.create|createCipher|AES|RSA|ECDSA)`), note: "Cryptographic operation"},
	{category: "crypto", risk: "high", pattern: regexp.MustCompile(`(?i)(tls|ssl|certificate|x509)`), note: "TLS/certificate handling"},
	{category: "crypto", risk: "medium", pattern: regexp.MustCompile(`(?i)(hash|sha256|sha512|md5|hmac)`), note: "Hash computation"},

	// Network
	{category: "network", risk: "medium", pattern: regexp.MustCompile(`(?i)(http\.Listen|net\.Listen|\.listen\()`), note: "Network listener"},
	{category: "network", risk: "medium", pattern: regexp.MustCompile(`(?i)(fetch|axios|http\.Get|http\.Post|requests\.)`), note: "Outbound HTTP request"},
	{category: "network", risk: "high", pattern: regexp.MustCompile(`(?i)(websocket|ws\.Server|socket\.io)`), note: "WebSocket endpoint"},
	{category: "network", risk: "medium", pattern: regexp.MustCompile(`(?i)(dns|resolver|lookup)`), note: "DNS resolution"},

	// Filesystem
	{category: "filesystem", risk: "medium", pattern: regexp.MustCompile(`(?i)(readFile|writeFile|os\.Open|os\.Create|open\()`), note: "File I/O operation"},
	{category: "filesystem", risk: "high", pattern: regexp.MustCompile(`(?i)(path\.join|filepath\.Join).*\.\.(["']|,)`), note: "Path traversal risk"},
	{category: "filesystem", risk: "medium", pattern: regexp.MustCompile(`(?i)(tmp|temp|cache).*dir`), note: "Temporary file usage"},

	// Injection
	{category: "injection", risk: "high", pattern: regexp.MustCompile(`(?i)(exec|spawn|system|popen)\(`), note: "Command execution"},
	{category: "injection", risk: "high", pattern: regexp.MustCompile(`(?i)(innerHTML|dangerouslySetInnerHTML|v-html)`), note: "HTML injection surface"},
	{category: "injection", risk: "high", pattern: regexp.MustCompile(`(?i)(\$\{.*\}.*(?:SELECT|INSERT|UPDATE|DELETE|DROP))`), note: "SQL injection risk"},
	{category: "injection", risk: "high", pattern: regexp.MustCompile(`(?i)(eval|Function\(|setTimeout\(.*,)`), note: "Code injection surface"},

	// Secrets
	{category: "secrets", risk: "high", pattern: regexp.MustCompile(`(?i)(process\.env|os\.Getenv|os\.environ)`), note: "Environment variable access"},
	{category: "secrets", risk: "high", pattern: regexp.MustCompile(`(?i)(\.env|dotenv|godotenv)`), note: "Env file loading"},
	{category: "secrets", risk: "high", pattern: regexp.MustCompile(`(?i)(vault|secretmanager|ssm|keyvault)`), note: "Secrets manager usage"},
}

// MapSecuritySurface identifies all security-relevant code in a directory.
func MapSecuritySurface(dir string, modifiedOnly []string) (*SecurityMap, error) {
	result := &SecurityMap{}

	filesToScan := modifiedOnly
	if len(filesToScan) == 0 {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() { return nil }
			rel, _ := filepath.Rel(dir, path)
			if shouldScan(rel) {
				filesToScan = append(filesToScan, rel)
			}
			return nil
		})
	}

	for _, relPath := range filesToScan {
		fullPath := filepath.Join(dir, relPath)
		surfaces, err := scanSecurityFile(fullPath, relPath)
		if err != nil { continue }
		result.Surfaces = append(result.Surfaces, surfaces...)
		result.FilesScanned++
	}

	return result, nil
}

func scanSecurityFile(fullPath, relPath string) ([]SecuritySurface, error) {
	f, err := os.Open(fullPath)
	if err != nil { return nil, err }
	defer f.Close()

	ext := filepath.Ext(relPath)
	var surfaces []SecuritySurface

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, rule := range securityRules {
			if len(rule.exts) > 0 && !extMatch(ext, rule.exts) { continue }
			if rule.pattern.MatchString(line) {
				surfaces = append(surfaces, SecuritySurface{
					Category: rule.category,
					File:     relPath,
					Line:     lineNum,
					Pattern:  strings.TrimSpace(line),
					Risk:     rule.risk,
					Note:     rule.note,
				})
			}
		}
	}
	return surfaces, scanner.Err()
}

func extMatch(ext string, exts []string) bool {
	for _, e := range exts {
		if e == ext { return true }
	}
	return false
}

// Summary returns a category-grouped summary.
func (m *SecurityMap) Summary() string {
	if len(m.Surfaces) == 0 {
		return "No security-relevant code found"
	}
	cats := map[string]int{}
	for _, s := range m.Surfaces { cats[s.Category]++ }

	var parts []string
	for _, cat := range []string{"auth", "crypto", "network", "filesystem", "injection", "secrets"} {
		if n, ok := cats[cat]; ok {
			parts = append(parts, cat+": "+strings.Repeat("█", min(n, 10))+" "+itoa(n))
		}
	}
	return strings.Join(parts, "\n")
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
