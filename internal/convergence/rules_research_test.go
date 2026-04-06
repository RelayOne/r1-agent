package convergence

import (
	"testing"
)

func TestTestAssertionWeakeningRule(t *testing.T) {
	rule := testAssertionWeakeningRule()

	// Should flag tautological assertions in test files
	tautological := []byte(`
func TestFoo(t *testing.T) {
	assert.True(true)
	assert.Equal(1, 1)
}`)
	findings := rule.Check("foo_test.go", tautological)
	if len(findings) == 0 {
		t.Error("should flag tautological assertion in test file")
	}

	// Should not flag non-test files
	findings = rule.Check("foo.go", tautological)
	if len(findings) != 0 {
		t.Error("should not check non-test files")
	}

	// Should not flag real assertions
	real := []byte(`
func TestFoo(t *testing.T) {
	result := Add(1, 2)
	assert.Equal(t, 3, result)
}`)
	findings = rule.Check("foo_test.go", real)
	if len(findings) != 0 {
		t.Error("should not flag real assertions")
	}
}

func TestAgentSelfReportRule(t *testing.T) {
	rule := agentSelfReportUnverifiedRule()

	// Should flag completion claims without evidence in output files
	noEvidence := []byte(`Task COMPLETED successfully. ALL TESTS PASS.`)
	findings := rule.Check("task-output.txt", noEvidence)
	if len(findings) == 0 {
		t.Error("should flag unverified completion claim")
	}

	// Should not flag when evidence is present
	withEvidence := []byte(`Task COMPLETED. ok github.com/foo/bar coverage: 85%`)
	findings = rule.Check("task-output.txt", withEvidence)
	if len(findings) != 0 {
		t.Error("should not flag when evidence is present")
	}

	// Should not check Go source files
	findings = rule.Check("main.go", noEvidence)
	if len(findings) != 0 {
		t.Error("should not check Go source files")
	}
}

func TestUnboundedGoroutineSpawnRule(t *testing.T) {
	rule := unboundedGoroutineSpawnRule()

	// Should flag go func inside for loop
	unbounded := []byte(`package main

func process(items []string) {
	for _, item := range items {
		go func(s string) {
			handle(s)
		}(item)
	}
}`)
	findings := rule.Check("main.go", unbounded)
	if len(findings) == 0 {
		t.Error("should flag unbounded goroutine spawn in loop")
	}

	// Should not flag when semaphore is present
	bounded := []byte(`package main

func process(items []string) {
	sem := make(chan struct{}, 10)
	for _, item := range items {
		sem <- struct{}{}
		go func(s string) {
			defer func() { <-sem }()
			handle(s)
		}(item)
	}
}`)
	findings = rule.Check("main.go", bounded)
	if len(findings) != 0 {
		t.Error("should not flag when semaphore is present")
	}

	// Should not check test files
	findings = rule.Check("main_test.go", unbounded)
	if len(findings) != 0 {
		t.Error("should not check test files")
	}
}

func TestCacheNoTTLRule(t *testing.T) {
	rule := cacheNoTTLRule()

	// Should flag cache set without TTL
	noTTL := []byte(`package main

func store(cache *Cache) {
	cache.Set("key", "value")
}`)
	findings := rule.Check("cache_handler.go", noTTL)
	if len(findings) == 0 {
		t.Error("should flag cache set without TTL")
	}

	// Should not flag cache set with TTL
	withTTL := []byte(`package main

func store(cache *Cache) {
	cache.SetEx("key", "value", 5*time.Minute)
}`)
	findings = rule.Check("cache_handler.go", withTTL)
	if len(findings) != 0 {
		t.Error("should not flag cache set with TTL")
	}
}

func TestMissingErrorStateUIRule(t *testing.T) {
	rule := missingErrorStateUIRule()

	// Should flag component that fetches data without error handling
	noError := []byte(`
const UserList = () => {
	const { data } = useQuery('users', fetchUsers);
	return <ul>{data.map(u => <li>{u.name}</li>)}</ul>;
};`)
	findings := rule.Check("UserList.tsx", noError)
	if len(findings) == 0 {
		t.Error("should flag missing error state")
	}

	// Should not flag when error handling exists
	withError := []byte(`
const UserList = () => {
	const { data, error, isLoading } = useQuery('users', fetchUsers);
	if (error) return <div>Error: {error.message}</div>;
	return <ul>{data.map(u => <li>{u.name}</li>)}</ul>;
};`)
	findings = rule.Check("UserList.tsx", withError)
	if len(findings) != 0 {
		t.Error("should not flag when error state is handled")
	}

	// Should not check non-frontend files
	findings = rule.Check("main.go", noError)
	if len(findings) != 0 {
		t.Error("should not check non-frontend files")
	}
}

func TestRetryWithoutBackoffRule(t *testing.T) {
	rule := retryWithoutBackoffRule()

	// Should flag constant-delay retry
	constantRetry := []byte(`package main

func fetchWithRetry(url string) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := fetch(url)
		if err == nil {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return errors.New("failed")
}`)
	findings := rule.Check("client.go", constantRetry)
	if len(findings) == 0 {
		t.Error("should flag constant-delay retry")
	}

	// Should not flag exponential backoff
	exponential := []byte(`package main

func fetchWithRetry(url string) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := fetch(url)
		if err == nil {
			return nil
		}
		delay := baseDelay * time.Duration(1<<attempt)
		time.Sleep(delay)
	}
	return errors.New("failed")
}`)
	findings = rule.Check("client.go", exponential)
	if len(findings) != 0 {
		t.Error("should not flag exponential backoff")
	}
}

func TestResearchRulesRegistered(t *testing.T) {
	rules := ResearchRules()
	if len(rules) != 6 {
		t.Errorf("expected 6 research rules, got %d", len(rules))
	}

	ids := make(map[string]bool)
	for _, r := range rules {
		ids[r.ID] = true
	}
	expected := []string{
		"test-assertion-weakening",
		"agent-self-report",
		"unbounded-goroutine",
		"cache-ttl",
		"missing-error-state",
		"retry-backoff",
	}
	for _, id := range expected {
		if !ids[id] {
			t.Errorf("missing research rule: %s", id)
		}
	}
}

func TestResearchRulesInDefaultRules(t *testing.T) {
	rules := DefaultRules()
	found := 0
	for _, r := range rules {
		if r.ID == "test-assertion-weakening" || r.ID == "unbounded-goroutine" ||
			r.ID == "cache-ttl" || r.ID == "missing-error-state" ||
			r.ID == "retry-backoff" || r.ID == "agent-self-report" {
			found++
		}
	}
	if found != 6 {
		t.Errorf("expected 6 research rules in DefaultRules, found %d", found)
	}
}
