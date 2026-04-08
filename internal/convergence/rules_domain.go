package convergence

import (
	"regexp"
	"strings"
)

// DomainRules returns rules activated only for detected project domains.
// This prevents false positives — Kafka rules won't fire on a project
// that doesn't use Kafka.
func DomainRules(domains map[Domain]bool) []Rule {
	var rules []Rule

	if domains[DomainPostgres] || domains[DomainMySQL] {
		rules = append(rules, sqlRules()...)
	}
	if domains[DomainRedis] {
		rules = append(rules, redisRules()...)
	}
	if domains[DomainKafka] || domains[DomainRabbitMQ] {
		rules = append(rules, messagingRules()...)
	}
	if domains[DomainGRPC] || domains[DomainProtobuf] {
		rules = append(rules, grpcRules()...)
	}
	if domains[DomainDocker] || domains[DomainK8s] {
		rules = append(rules, containerRules()...)
	}
	if domains[DomainStripe] {
		rules = append(rules, stripeRules()...)
	}
	if domains[DomainTerraform] {
		rules = append(rules, terraformRules()...)
	}
	if domains[DomainReact] || domains[DomainNextJS] {
		rules = append(rules, reactRules()...)
	}
	if domains[DomainGraphQL] {
		rules = append(rules, graphqlRules()...)
	}
	if domains[DomainElastic] || domains[DomainMongoDB] {
		rules = append(rules, nosqlRules()...)
	}

	return rules
}

// --- SQL / Relational DB rules ---

func sqlRules() []Rule {
	return []Rule{
		{
			ID: "sql-no-select-star", Name: "No SELECT * in production queries", Category: CatCodeQuality,
			Severity: SevMajor, Enabled: true,
			Description: "SELECT * fetches unnecessary columns, breaks on schema changes, and hurts performance",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				re := regexp.MustCompile(`(?i)SELECT\s+\*\s+FROM`)
				return regexCheck(re, file, content, "sql-no-select-star", CatCodeQuality, SevMajor,
					"SELECT * in query — enumerate columns explicitly",
					"Replace SELECT * with explicit column names")
			},
		},
		{
			ID: "sql-no-cascade-delete", Name: "No CASCADE DELETE without review", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "ON DELETE CASCADE can silently destroy related data across tables",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				re := regexp.MustCompile(`(?i)ON\s+DELETE\s+CASCADE`)
				return regexCheck(re, file, content, "sql-no-cascade-delete", CatReliability, SevBlocking,
					"ON DELETE CASCADE — silently destroys related data",
					"Use soft deletes or handle deletion explicitly in application code")
			},
		},
		{
			ID: "sql-transaction-timeout", Name: "Long-running transactions need timeout", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Transactions without timeout can hold locks indefinitely, causing deadlocks",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				beginTx := regexp.MustCompile(`(?i)(BEGIN|db\.Begin|\.BeginTx|\.Transaction)\b`)
				timeout := regexp.MustCompile(`(?i)(context\.WithTimeout|lock_timeout|statement_timeout)`)
				if beginTx.MatchString(s) && !timeout.MatchString(s) {
					lines := strings.Split(s, "\n")
					for i, line := range lines {
						if beginTx.MatchString(line) {
							return []Finding{{
								RuleID:      "sql-transaction-timeout",
								Category:    CatReliability,
								Severity:    SevMajor,
								File:        file,
								Line:        i + 1,
								Description: "Transaction without timeout — can hold locks indefinitely",
								Suggestion:  "Use context.WithTimeout or SET lock_timeout for bounded transactions",
								Evidence:    strings.TrimSpace(line),
							}}
						}
					}
				}
				return nil
			},
		},
		{
			ID: "sql-migration-index", Name: "Migrations adding columns should consider indexes", Category: CatCodeQuality,
			Severity: SevMajor, Enabled: true,
			Description: "Adding columns used in WHERE/JOIN without indexes degrades query performance at scale",
			Check: func(file string, content []byte) []Finding {
				ext := strings.ToLower(file)
				if !strings.Contains(ext, "migration") && !strings.Contains(ext, "migrate") {
					return nil
				}
				s := string(content)
				addCol := regexp.MustCompile(`(?i)ADD\s+COLUMN`)
				createIdx := regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX`)
				if addCol.MatchString(s) && !createIdx.MatchString(s) {
					return []Finding{{
						RuleID:      "sql-migration-index",
						Category:    CatCodeQuality,
						Severity:    SevMajor,
						File:        file,
						Line:        1,
						Description: "Migration adds columns without indexes — consider query patterns",
						Suggestion:  "Add CREATE INDEX for columns used in WHERE, JOIN, or ORDER BY clauses",
					}}
				}
				return nil
			},
		},
	}
}

// --- Redis rules ---

func redisRules() []Rule {
	return []Rule{
		{
			ID: "redis-no-keys-cmd", Name: "No KEYS command in production", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "KEYS command blocks Redis and scans entire keyspace — use SCAN instead",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				re := regexp.MustCompile(`(?i)\.(Keys|Do\([^)]*"KEYS")`)
				return regexCheck(re, file, content, "redis-no-keys-cmd", CatReliability, SevBlocking,
					"Redis KEYS command — blocks server, scans entire keyspace",
					"Use SCAN for iterative key discovery")
			},
		},
		{
			ID: "redis-ttl-required", Name: "Redis SET should include TTL", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Redis keys without TTL cause unbounded memory growth",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				set := regexp.MustCompile(`\.Set\(ctx,`)
				expiration := regexp.MustCompile(`\.Set\(ctx,[^,]+,[^,]+,\s*0\)`)
				var findings []Finding
				lines := strings.Split(s, "\n")
				for i, line := range lines {
					if set.MatchString(line) && expiration.MatchString(line) {
						findings = append(findings, Finding{
							RuleID:      "redis-ttl-required",
							Category:    CatReliability,
							Severity:    SevMajor,
							File:        file,
							Line:        i + 1,
							Description: "Redis SET with zero TTL — key never expires, memory grows unbounded",
							Suggestion:  "Set an appropriate TTL: client.Set(ctx, key, val, 24*time.Hour)",
							Evidence:    strings.TrimSpace(line),
						})
					}
				}
				return findings
			},
		},
	}
}

// --- Messaging (Kafka, RabbitMQ) rules ---

func messagingRules() []Rule {
	return []Rule{
		{
			ID: "msg-idempotent-consumer", Name: "Message consumers must be idempotent", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Message consumers without idempotency checks will corrupt state on redelivery",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				consumer := regexp.MustCompile(`(?i)(consume|handler|process)(Message|Event|Record)`)
				idempotency := regexp.MustCompile(`(?i)(idempoten|dedup|already.processed|message.id|event.id)`)
				if consumer.MatchString(s) && !idempotency.MatchString(s) {
					lines := strings.Split(s, "\n")
					for i, line := range lines {
						if consumer.MatchString(line) {
							return []Finding{{
								RuleID:      "msg-idempotent-consumer",
								Category:    CatReliability,
								Severity:    SevMajor,
								File:        file,
								Line:        i + 1,
								Description: "Message consumer without idempotency check — redelivery corrupts state",
								Suggestion:  "Track processed message IDs to handle at-least-once delivery",
								Evidence:    strings.TrimSpace(line),
							}}
						}
					}
				}
				return nil
			},
		},
		{
			ID: "msg-dlq-handler", Name: "Failed messages need dead letter handling", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Messages that fail processing repeatedly need a dead letter queue strategy",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				consumer := regexp.MustCompile(`(?i)(ReadMessage|Consume|Subscribe)\(`)
				dlq := regexp.MustCompile(`(?i)(dead.letter|dlq|DLQ|retry.count|max.retries)`)
				if consumer.MatchString(s) && !dlq.MatchString(s) {
					return []Finding{{
						RuleID:      "msg-dlq-handler",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        1,
						Description: "Message consumer without dead letter handling — poison messages block queue",
						Suggestion:  "Implement DLQ or max retry count to handle poison messages",
					}}
				}
				return nil
			},
		},
	}
}

// --- gRPC / Protobuf rules ---

func grpcRules() []Rule {
	return []Rule{
		{
			ID: "grpc-deadline", Name: "gRPC calls must set deadline", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "gRPC calls without deadline wait forever — always set context deadline",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				grpcCall := regexp.MustCompile(`\.\w+Client\.\w+\(ctx`)
				deadline := regexp.MustCompile(`(WithTimeout|WithDeadline|grpc\.WaitForReady)`)
				if grpcCall.MatchString(s) && !deadline.MatchString(s) {
					lines := strings.Split(s, "\n")
					for i, line := range lines {
						if grpcCall.MatchString(line) {
							return []Finding{{
								RuleID:      "grpc-deadline",
								Category:    CatReliability,
								Severity:    SevBlocking,
								File:        file,
								Line:        i + 1,
								Description: "gRPC call without deadline — will wait forever if server is slow",
								Suggestion:  "Use context.WithTimeout(ctx, 5*time.Second) before gRPC calls",
								Evidence:    strings.TrimSpace(line),
							}}
						}
					}
				}
				return nil
			},
		},
		{
			ID: "proto-field-reserved", Name: "Deleted proto fields must be reserved", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "Removing proto fields without reserving the number breaks wire compatibility",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".proto") {
					return nil
				}
				s := string(content)
				// Look for gaps in field numbers (simplified: flag if there's a comment about removed field)
				removed := regexp.MustCompile(`(?i)(removed|deprecated|deleted)\s+field`)
				reserved := regexp.MustCompile(`reserved\s+\d+`)
				if removed.MatchString(s) && !reserved.MatchString(s) {
					return []Finding{{
						RuleID:      "proto-field-reserved",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "Proto field removed without reserved — reusing the number breaks clients",
						Suggestion:  "Add 'reserved N;' for deleted field numbers",
					}}
				}
				return nil
			},
		},
	}
}

// --- Container / K8s rules ---

func containerRules() []Rule {
	return []Rule{
		{
			ID: "docker-no-root", Name: "Dockerfile must not run as root", Category: CatSecurity,
			Severity: SevBlocking, Enabled: true,
			Description: "Running containers as root exposes the host to privilege escalation",
			Check: func(file string, content []byte) []Finding {
				if !strings.Contains(file, "Dockerfile") {
					return nil
				}
				s := string(content)
				user := regexp.MustCompile(`(?m)^USER\s+`)
				if !user.MatchString(s) {
					return []Finding{{
						RuleID:      "docker-no-root",
						Category:    CatSecurity,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "Dockerfile without USER directive — container runs as root",
						Suggestion:  "Add USER nonroot or USER 1000 before the CMD/ENTRYPOINT",
					}}
				}
				return nil
			},
		},
		{
			ID: "k8s-resource-limits", Name: "K8s pods must have resource limits", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "Pods without resource limits can consume entire node and cause OOM kills",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
					return nil
				}
				s := string(content)
				if !strings.Contains(s, "kind: Deployment") && !strings.Contains(s, "kind: Pod") &&
					!strings.Contains(s, "kind: StatefulSet") && !strings.Contains(s, "kind: DaemonSet") {
					return nil
				}
				if !strings.Contains(s, "resources:") || !strings.Contains(s, "limits:") {
					return []Finding{{
						RuleID:      "k8s-resource-limits",
						Category:    CatReliability,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "K8s workload without resource limits — can OOM-kill the node",
						Suggestion:  "Add resources.limits.cpu and resources.limits.memory",
					}}
				}
				return nil
			},
		},
		{
			ID: "k8s-liveness-probe", Name: "K8s pods should have health probes", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Pods without liveness/readiness probes won't be restarted or removed from service on failure",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
					return nil
				}
				s := string(content)
				if !strings.Contains(s, "kind: Deployment") && !strings.Contains(s, "kind: StatefulSet") {
					return nil
				}
				if !strings.Contains(s, "livenessProbe") && !strings.Contains(s, "readinessProbe") {
					return []Finding{{
						RuleID:      "k8s-liveness-probe",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        1,
						Description: "K8s workload without health probes — failures go undetected",
						Suggestion:  "Add livenessProbe and readinessProbe to container spec",
					}}
				}
				return nil
			},
		},
	}
}

// --- Stripe rules ---

func stripeRules() []Rule {
	return []Rule{
		{
			ID: "stripe-webhook-verify", Name: "Stripe webhooks must verify signatures", Category: CatSecurity,
			Severity: SevBlocking, Enabled: true,
			Description: "Processing Stripe webhooks without signature verification allows event forgery",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				webhook := regexp.MustCompile(`(?i)(webhook|stripe.*event|event.*stripe)`)
				verify := regexp.MustCompile(`(?i)(ConstructEvent|VerifySignature|webhook.*secret|Stripe-Signature)`)
				if webhook.MatchString(s) && strings.Contains(s, "stripe") && !verify.MatchString(s) {
					return []Finding{{
						RuleID:      "stripe-webhook-verify",
						Category:    CatSecurity,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "Stripe webhook without signature verification — events can be forged",
						Suggestion:  "Use stripe.ConstructEvent(payload, sigHeader, webhookSecret) to verify",
					}}
				}
				return nil
			},
		},
		{
			ID: "stripe-idempotency", Name: "Stripe payment operations must use idempotency keys", Category: CatReliability,
			Severity: SevBlocking, Enabled: true,
			Description: "Payment operations without idempotency keys cause double charges on retry",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				payment := regexp.MustCompile(`(?i)(charge\.Create|paymentintent\.New|PaymentIntent\.create|Charge\.create)`)
				idempotency := regexp.MustCompile(`(?i)(IdempotencyKey|idempotency_key)`)
				if payment.MatchString(s) && !idempotency.MatchString(s) {
					lines := strings.Split(s, "\n")
					for i, line := range lines {
						if payment.MatchString(line) {
							return []Finding{{
								RuleID:      "stripe-idempotency",
								Category:    CatReliability,
								Severity:    SevBlocking,
								File:        file,
								Line:        i + 1,
								Description: "Stripe payment without idempotency key — retries cause double charges",
								Suggestion:  "Add IdempotencyKey to payment creation params",
								Evidence:    strings.TrimSpace(line),
							}}
						}
					}
				}
				return nil
			},
		},
	}
}

// --- Terraform rules ---

func terraformRules() []Rule {
	return []Rule{
		{
			ID: "tf-no-hardcoded-creds", Name: "No hardcoded credentials in Terraform", Category: CatSecurity,
			Severity: SevBlocking, Enabled: true,
			Description: "Hardcoded secrets in Terraform state are stored in plaintext",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".tf") {
					return nil
				}
				re := regexp.MustCompile(`(?i)(access_key|secret_key|password|token)\s*=\s*"[^"]{8,}"`)
				return regexCheck(re, file, content, "tf-no-hardcoded-creds", CatSecurity, SevBlocking,
					"Hardcoded credential in Terraform — stored in plaintext in state file",
					"Use variables with sensitive=true or reference a secrets manager")
			},
		},
		{
			ID: "tf-state-backend", Name: "Terraform must use remote state", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "Local Terraform state is not shared, not locked, and easily lost",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".tf") {
					return nil
				}
				s := string(content)
				if strings.Contains(s, "terraform {") && !strings.Contains(s, "backend ") {
					return []Finding{{
						RuleID:      "tf-state-backend",
						Category:    CatReliability,
						Severity:    SevMajor,
						File:        file,
						Line:        1,
						Description: "Terraform block without remote backend — state is local-only",
						Suggestion:  "Configure backend \"s3\" or \"gcs\" for shared, locked state",
					}}
				}
				return nil
			},
		},
	}
}

// --- React / Next.js rules ---

func reactRules() []Rule {
	return []Rule{
		{
			ID: "react-no-array-index-key", Name: "No array index as React key", Category: CatCodeQuality,
			Severity: SevMajor, Enabled: true,
			Description: "Using array index as key causes incorrect DOM updates when items reorder",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".tsx") && !strings.HasSuffix(file, ".jsx") {
					return nil
				}
				if isTestFile(file) {
					return nil
				}
				re := regexp.MustCompile(`key=\{(index|i|idx)\}`)
				return regexCheck(re, file, content, "react-no-array-index-key", CatCodeQuality, SevMajor,
					"Array index used as React key — causes incorrect DOM updates on reorder",
					"Use a stable unique ID: key={item.id}")
			},
		},
		{
			ID: "react-useeffect-deps", Name: "useEffect must have dependency array", Category: CatReliability,
			Severity: SevMajor, Enabled: true,
			Description: "useEffect without dependency array runs on every render — performance and infinite loop risk",
			Check: func(file string, content []byte) []Finding {
				if !strings.HasSuffix(file, ".tsx") && !strings.HasSuffix(file, ".jsx") &&
					!strings.HasSuffix(file, ".ts") && !strings.HasSuffix(file, ".js") {
					return nil
				}
				if isTestFile(file) {
					return nil
				}
				// Detect useEffect(() => { ... }) without second argument
				re := regexp.MustCompile(`useEffect\(\s*\([^)]*\)\s*=>\s*\{`)
				closeParen := regexp.MustCompile(`useEffect\([^)]*\)\s*\)`)
				s := string(content)
				if !re.MatchString(s) {
					return nil
				}
				// Simple heuristic: check if useEffect has , [] or , [deps]
				lines := strings.Split(s, "\n")
				var findings []Finding
				for i, line := range lines {
					if strings.Contains(line, "useEffect(") && !strings.Contains(line, "// eslint") {
						// Check next few lines for closing with deps
						hasDeps := false
						for j := i; j < i+10 && j < len(lines); j++ {
							if strings.Contains(lines[j], "], [") || strings.Contains(lines[j], "}, [") ||
								strings.Contains(lines[j], ", []") {
								hasDeps = true
								break
							}
						}
						if !hasDeps && closeParen.MatchString(line) {
							findings = append(findings, Finding{
								RuleID:      "react-useeffect-deps",
								Category:    CatReliability,
								Severity:    SevMajor,
								File:        file,
								Line:        i + 1,
								Description: "useEffect without dependency array — runs every render",
								Suggestion:  "Add dependency array: useEffect(() => { ... }, [deps])",
								Evidence:    strings.TrimSpace(line),
							})
						}
					}
				}
				return findings
			},
		},
	}
}

// --- GraphQL rules ---

func graphqlRules() []Rule {
	return []Rule{
		{
			ID: "gql-no-unbounded-query", Name: "GraphQL queries must have depth/complexity limits", Category: CatSecurity,
			Severity: SevBlocking, Enabled: true,
			Description: "Unbounded GraphQL queries enable denial-of-service via deeply nested queries",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				s := string(content)
				schema := regexp.MustCompile(`(?i)(NewSchema|graphql\.NewServer|apolloServer|makeExecutableSchema)`)
				depthLimit := regexp.MustCompile(`(?i)(depthLimit|maxDepth|complexity|queryComplexity|costAnalysis)`)
				if schema.MatchString(s) && !depthLimit.MatchString(s) {
					return []Finding{{
						RuleID:      "gql-no-unbounded-query",
						Category:    CatSecurity,
						Severity:    SevBlocking,
						File:        file,
						Line:        1,
						Description: "GraphQL server without depth/complexity limits — vulnerable to DoS",
						Suggestion:  "Add query depth limiting and cost analysis middleware",
					}}
				}
				return nil
			},
		},
	}
}

// --- NoSQL (MongoDB, Elasticsearch) rules ---

func nosqlRules() []Rule {
	return []Rule{
		{
			ID: "nosql-injection", Name: "NoSQL queries must sanitize input", Category: CatSecurity,
			Severity: SevBlocking, Enabled: true,
			Description: "Unsanitized input in NoSQL queries enables injection attacks",
			Check: func(file string, content []byte) []Finding {
				if isTestFile(file) {
					return nil
				}
				// Detect string concatenation in MongoDB queries
				re := regexp.MustCompile(`(?i)(Find|findOne|aggregate|updateOne|deleteOne)\([^)]*\+\s*`)
				return regexCheck(re, file, content, "nosql-injection", CatSecurity, SevBlocking,
					"String concatenation in NoSQL query — injection risk",
					"Use parameterized queries with bson.M{} or filter builders")
			},
		},
	}
}
