package failure

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

var volatileIdempotencyMetaKeys = map[string]struct{}{
	"agent_task_sequence": {},
	"recovered_from_wal":  {},
	"resume_checkpoint":   {},
	"worker_id":           {},
}

// DeriveIdempotencyKey canonicalizes task identity into a restart-stable token.
func DeriveIdempotencyKey(namespace, action, prompt, repo, runner string, meta map[string]string) string {
	h := sha256.New()
	writeStableField(h, "namespace", namespace)
	writeStableField(h, "action", action)
	writeStableField(h, "prompt", prompt)
	writeStableField(h, "repo", repo)
	writeStableField(h, "runner", runner)

	keys := make([]string, 0, len(meta))
	for k := range meta {
		if _, skip := volatileIdempotencyMetaKeys[k]; skip {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeStableField(h, "meta."+k, meta[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeStableField(h interface{ Write([]byte) (int, error) }, name, value string) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	_, _ = h.Write([]byte(name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{'\n'})
}
