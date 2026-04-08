package convergence

import (
	"os"
	"path/filepath"
	"strings"
)

// Domain represents a detected technology in the project.
type Domain string

const (
	DomainPostgres   Domain = "postgres"
	DomainMySQL      Domain = "mysql"
	DomainRedis      Domain = "redis"
	DomainKafka      Domain = "kafka"
	DomainGRPC       Domain = "grpc"
	DomainGraphQL    Domain = "graphql"
	DomainDocker     Domain = "docker"
	DomainK8s        Domain = "kubernetes"
	DomainStripe     Domain = "stripe"
	DomainAWS        Domain = "aws"
	DomainReact      Domain = "react"
	DomainNextJS     Domain = "nextjs"
	DomainTerraform  Domain = "terraform"
	DomainProtobuf   Domain = "protobuf"
	DomainElastic    Domain = "elasticsearch"
	DomainMongoDB    Domain = "mongodb"
	DomainRabbitMQ   Domain = "rabbitmq"
	DomainCloudflare Domain = "cloudflare"
	DomainTauri      Domain = "tauri"
)

// goModSignals maps Go module import paths to domains.
var goModSignals = map[string]Domain{
	"github.com/lib/pq":                     DomainPostgres,
	"github.com/jackc/pgx":                  DomainPostgres,
	"github.com/jackc/pgconn":               DomainPostgres,
	"github.com/go-sql-driver/mysql":        DomainMySQL,
	"github.com/redis/go-redis":             DomainRedis,
	"github.com/gomodule/redigo":            DomainRedis,
	"github.com/segmentio/kafka-go":         DomainKafka,
	"github.com/confluentinc/confluent-kafka-go": DomainKafka,
	"github.com/Shopify/sarama":             DomainKafka,
	"github.com/IBM/sarama":                 DomainKafka,
	"google.golang.org/grpc":                DomainGRPC,
	"google.golang.org/protobuf":            DomainProtobuf,
	"github.com/99designs/gqlgen":           DomainGraphQL,
	"github.com/graphql-go/graphql":         DomainGraphQL,
	"github.com/stripe/stripe-go":           DomainStripe,
	"github.com/aws/aws-sdk-go":             DomainAWS,
	"github.com/aws/aws-sdk-go-v2":          DomainAWS,
	"github.com/olivere/elastic":            DomainElastic,
	"github.com/elastic/go-elasticsearch":   DomainElastic,
	"go.mongodb.org/mongo-driver":           DomainMongoDB,
	"github.com/rabbitmq/amqp091-go":        DomainRabbitMQ,
	"github.com/streadway/amqp":             DomainRabbitMQ,
	"github.com/cloudflare/cloudflare-go":   DomainCloudflare,
}

// packageJSONSignals maps npm package names to domains.
var packageJSONSignals = map[string]Domain{
	"pg":                    DomainPostgres,
	"knex":                  DomainPostgres,
	"sequelize":             DomainPostgres,
	"typeorm":               DomainPostgres,
	"prisma":                DomainPostgres,
	"mysql2":                DomainMySQL,
	"redis":                 DomainRedis,
	"ioredis":               DomainRedis,
	"kafkajs":               DomainKafka,
	"@grpc/grpc-js":         DomainGRPC,
	"graphql":               DomainGraphQL,
	"@apollo/server":        DomainGraphQL,
	"apollo-server":         DomainGraphQL,
	"stripe":                DomainStripe,
	"@stripe/stripe-js":     DomainStripe,
	"aws-sdk":               DomainAWS,
	"@aws-sdk/client-s3":    DomainAWS,
	"react":                 DomainReact,
	"react-dom":             DomainReact,
	"next":                  DomainNextJS,
	"@elastic/elasticsearch": DomainElastic,
	"mongodb":               DomainMongoDB,
	"mongoose":              DomainMongoDB,
	"amqplib":               DomainRabbitMQ,
	"cloudflare":            DomainCloudflare,
	"@cloudflare/workers-types": DomainCloudflare,
	"@tauri-apps/api":       DomainTauri,
	"@tauri-apps/cli":       DomainTauri,
}

// fileSignals maps file existence to domains.
var fileSignals = map[string]Domain{
	"Dockerfile":           DomainDocker,
	"docker-compose.yml":   DomainDocker,
	"docker-compose.yaml":  DomainDocker,
	"kubernetes":           DomainK8s,
	"k8s":                  DomainK8s,
	"terraform":            DomainTerraform,
	"main.tf":              DomainTerraform,
	"tauri.conf.json":      DomainTauri,
	"src-tauri":            DomainTauri,
}

// DetectDomains scans the project root for dependency files and returns
// the set of technology domains detected. This is intentionally fast —
// it reads only manifest files, not the full source tree.
func DetectDomains(projectDir string) map[Domain]bool {
	domains := make(map[Domain]bool)

	// Check go.mod
	if data, err := os.ReadFile(filepath.Join(projectDir, "go.mod")); err == nil {
		detectGoMod(string(data), domains)
	}

	// Check package.json
	if data, err := os.ReadFile(filepath.Join(projectDir, "package.json")); err == nil {
		detectPackageJSON(string(data), domains)
	}

	// Check Cargo.toml
	if data, err := os.ReadFile(filepath.Join(projectDir, "Cargo.toml")); err == nil {
		detectCargoToml(string(data), domains)
	}

	// Check requirements.txt / pyproject.toml
	for _, pyFile := range []string{"requirements.txt", "pyproject.toml", "Pipfile"} {
		if data, err := os.ReadFile(filepath.Join(projectDir, pyFile)); err == nil {
			detectPython(string(data), domains)
		}
	}

	// Check file-based signals
	detectFileSignals(projectDir, domains)

	return domains
}

func detectGoMod(content string, domains map[Domain]bool) {
	for signal, domain := range goModSignals {
		if strings.Contains(content, signal) {
			domains[domain] = true
		}
	}
}

func detectPackageJSON(content string, domains map[Domain]bool) {
	for signal, domain := range packageJSONSignals {
		// Match "package-name" with quotes to avoid substring false positives
		if strings.Contains(content, `"`+signal+`"`) {
			domains[domain] = true
		}
	}
}

func detectCargoToml(content string, domains map[Domain]bool) {
	cargoSignals := map[string]Domain{
		"sqlx":        DomainPostgres,
		"diesel":      DomainPostgres,
		"tokio-postgres": DomainPostgres,
		"rdkafka":     DomainKafka,
		"redis":       DomainRedis,
		"tonic":       DomainGRPC,
		"prost":       DomainProtobuf,
		"stripe":      DomainStripe,
		"aws-sdk":     DomainAWS,
		"mongodb":     DomainMongoDB,
		"tauri":       DomainTauri,
		"elasticsearch": DomainElastic,
	}
	for signal, domain := range cargoSignals {
		if strings.Contains(content, signal) {
			domains[domain] = true
		}
	}
}

func detectPython(content string, domains map[Domain]bool) {
	pySignals := map[string]Domain{
		"psycopg":     DomainPostgres,
		"asyncpg":     DomainPostgres,
		"sqlalchemy":  DomainPostgres,
		"mysqlclient": DomainMySQL,
		"redis":       DomainRedis,
		"kafka":       DomainKafka,
		"grpcio":      DomainGRPC,
		"graphene":    DomainGraphQL,
		"stripe":      DomainStripe,
		"boto3":       DomainAWS,
		"pymongo":     DomainMongoDB,
		"elasticsearch": DomainElastic,
		"pika":        DomainRabbitMQ,
	}
	for signal, domain := range pySignals {
		if strings.Contains(content, signal) {
			domains[domain] = true
		}
	}
}

func detectFileSignals(dir string, domains map[Domain]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if domain, ok := fileSignals[name]; ok {
			domains[domain] = true
		}
	}
	// Check for .proto files (protobuf/gRPC)
	if protos, _ := filepath.Glob(filepath.Join(dir, "**/*.proto")); len(protos) > 0 {
		domains[DomainProtobuf] = true
	}
	if protos, _ := filepath.Glob(filepath.Join(dir, "*.proto")); len(protos) > 0 {
		domains[DomainProtobuf] = true
	}
}

// NewValidatorForProject creates a validator with default rules plus
// domain-specific rules activated based on detected project technologies.
func NewValidatorForProject(projectDir string) *Validator {
	rules := DefaultRules()
	domains := DetectDomains(projectDir)
	rules = append(rules, DomainRules(domains)...)
	return &Validator{rules: rules}
}
