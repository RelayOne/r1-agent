# kafka-operations

> Kafka consumer group tuning, exactly-once semantics, partition design, DLQ patterns, and schema registry configuration

<!-- keywords: kafka, consumer group, rebalance, partition, offset, exactly-once, dead letter, dlq, schema registry, avro, protobuf, confluent, sarama, kafkajs -->

## Critical Rules

1. **Always use CooperativeStickyAssignor.** Eager rebalancing (the old default) forces every consumer to stop, causing 45+ minute outages with 100+ consumers. Cooperative sticky uses incremental two-phase rebalancing. On Kafka 4.0+, use KIP-848 server-side rebalancing (`group.protocol=consumer`) for 20x faster rebalances.

2. **Ensure max.poll.records * per_record_processing_time < max.poll.interval.ms.** Exceeding this kicks the consumer for slow processing while heartbeats continue, triggering cascading rebalance storms.

3. **Set `partitioner=murmur2_random` in librdkafka clients (Go, Python, .NET).** The Java partitioner uses Murmur2; librdkafka defaults to CRC32. The same key produces different partition assignments across languages without this fix.

4. **Never auto-register schemas in production.** Set `auto.register.schemas=false`. Register schemas exclusively through CI/CD after compatibility checks against the Schema Registry `/compatibility` endpoint.

5. **Partitions can increase but never decrease.** Increasing also breaks existing key-to-partition mappings. Start with 6-12 partitions per topic. Keep ~100 partitions per broker.

## Consumer Timeout Tuning

| Parameter | Controls | Default | Thread |
|-----------|----------|---------|--------|
| `session.timeout.ms` | Broker detects dead consumer | 45s (Kafka 3.0+) | Heartbeat |
| `heartbeat.interval.ms` | Heartbeat frequency | 3s | Heartbeat |
| `max.poll.interval.ms` | Max time between poll() calls | 300s | Application |

Low-latency: `session.timeout.ms=10000`, `max.poll.interval.ms=30000`, `max.poll.records=100`.
Batch processing: `session.timeout.ms=45000`, `max.poll.interval.ms=600000`, `max.poll.records=500`.
Kubernetes: Use static membership (`group.instance.id=${HOSTNAME}`, `session.timeout.ms=300000`).

## Exactly-Once Semantics

EOS builds on three layers: idempotent producers (default since Kafka 3.0), transactional APIs, and `read_committed` consumer isolation. EOS only covers Kafka-to-Kafka pipelines. For database writes, use the outbox pattern plus consumer-side idempotency.

- Idempotent producer overhead: essentially zero.
- Transactional overhead: 3-20% depending on batch size (3% at 100ms commit intervals).
- Use the same `transactional.id` for the same input partition. Handle `ProducerFencedException` as fatal.
- For Go EOS: use confluent-kafka-go (segmentio/kafka-go has limited transaction support).
- Monitor LSO (Last Stable Offset) lag. Long-running transactions block all read-committed consumers.

## Schema Registry Compatibility Modes

| Mode | Rule | Safe Changes |
|------|------|-------------|
| BACKWARD (default) | New reads old | Delete fields; add optional with defaults |
| FORWARD | Old reads new | Add fields; delete optional with defaults |
| FULL | Both directions | Add/delete optional fields with defaults only |
| FULL_TRANSITIVE | Full against all versions | Safest for production |

Avro golden rule: always add fields with defaults. Protobuf golden rule: never change or reuse field numbers. Use RecordNameStrategy for event-sourcing with multiple event types per topic.

## Partition Key Design

Avoid hot partitions: customer ID with Zipfian distribution concentrates 80%+ of traffic on 1-2 partitions. Timestamps concentrate all writes on one partition. Detect skew by monitoring per-partition lag variance (>20% deviation from mean = investigate).

Mitigations:
- **Key salting**: `key:hash(key)%numSalts` spreads hot keys (breaks per-key ordering).
- **Compound keys**: `region:customerId` increases cardinality while preserving ordering.
- **Null keys**: Trigger sticky partitioning (Kafka 2.4+). Never use when ordering matters.

## Dead Letter Queue Patterns

Use per-topic naming: `{topic}.dlq`. For automated retry chains (Uber's pattern):
- `{topic}.retry.1` (10s delay) -> `{topic}.retry.2` (10m) -> `{topic}.retry.3` (1h) -> `{topic}.dlq`

Classify errors immediately: permanent errors (deserialization, validation) go directly to DLQ. Transient errors (network, rate limiting) enter the retry chain. Preserve context in Kafka headers (original topic, partition, offset, error reason, retry count). Alert when DLQ ingestion exceeds 5% of main topic volume.

## Log Compaction

`compact` keeps only the latest value per key. Tombstones (null value) retained for `delete.retention.ms` (24h default). The active segment is never compacted. Set `min.cleanable.dirty.ratio=0.5`, `min.compaction.lag.ms=3600000`. Topics without keys cannot be compacted. Use `compact,delete` for TTL-bounded state or GDPR compliance.

## Common Gotchas

- **Rebalance storms during rolling deploys**: Limit concurrent pod restarts to 1-2 using PodDisruptionBudgets. Never scale out during an active rebalance.
- **WAL bloat with CDC**: Replication slots retain all WAL until Debezium catches up. Set `heartbeat.interval.ms=10000`.
- **Consumer group ID reuse**: Using the same group ID across environments causes cross-environment offset interference.
- **Poison messages**: Track the offset being processed. After N crashes on the same offset, skip to DLQ.
