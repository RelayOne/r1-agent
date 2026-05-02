package plan

import "testing"

func TestIsGroundTruthACCommand(t *testing.T) {
	groundTruth := []string{
		"pnpm build",
		"pnpm install",
		"pnpm typecheck",
		"pnpm test",
		"npm ci",
		"npm run build",
		"turbo run build --filter=@sentinel/web",
		"next build",
		"tsc --noEmit",
		"vitest run",
		"jest --coverage",
		"pytest tests/",
		"cargo test",
		"go test ./...",
		"go build ./cmd/r1",
		"cd /x && pnpm install && pnpm build",
	}
	for _, cmd := range groundTruth {
		if !isGroundTruthACCommand(cmd) {
			t.Fatalf("expected ground-truth: %q", cmd)
		}
	}
	overridable := []string{
		"grep -q 'Zod' packages/types/src/index.ts",
		"test -f packages/ui-web/src/button.tsx",
		"cat config.json | wc -l",
		"ls -la apps/",
		"[ -d packages/types ]",
	}
	for _, cmd := range overridable {
		if isGroundTruthACCommand(cmd) {
			t.Fatalf("should NOT be ground-truth (overridable by judge): %q", cmd)
		}
	}
	if isGroundTruthACCommand("") {
		t.Fatal("empty command must not be ground-truth")
	}
}
