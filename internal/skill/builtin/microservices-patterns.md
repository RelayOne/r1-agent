# microservices-patterns

> Microservices architecture patterns: decomposition, communication, sagas, and data consistency

<!-- keywords: microservices, service mesh, event sourcing, saga, domain driven design, bounded context -->

## Critical Rules

1. **Decompose by business capability, not by technical layer.** A "User Service" that owns user registration, profile, and auth is better than splitting "UserController" from "UserRepository" into separate services.

2. **Each service owns its data.** No shared databases. If two services need the same data, one owns it and the other requests it or subscribes to change events.

3. **Design for failure.** Every remote call can fail. Use circuit breakers, retries with backoff, and timeouts on all inter-service communication.

4. **Avoid distributed monoliths.** If every change requires deploying multiple services simultaneously, you have a distributed monolith. Services must be independently deployable.

5. **Start with fewer, larger services.** Premature decomposition is worse than a monolith. Split when you have clear bounded contexts and team boundaries.

## Service Decomposition Strategies

### By Business Capability
- Map services to organizational capabilities: orders, inventory, payments, shipping
- Each service encapsulates a complete business function with its own data store
- Teams align to services (Conway's Law works in your favor)

### By Subdomain (DDD)
- Identify bounded contexts through event storming or domain analysis
- Core domain: build in-house with your best engineers
- Supporting domain: build or buy depending on competitive advantage
- Generic domain: buy off-the-shelf (auth, email, payments)

## Communication Patterns

### Synchronous (Request/Response)
- **REST over HTTP** for simple CRUD between services. Use when the caller needs an immediate answer.
- **gRPC** for internal service-to-service calls. Binary protocol, schema enforcement, streaming support.
- Always set timeouts. A missing timeout turns a slow dependency into a cascading failure.

### Asynchronous (Event-Driven)
- **Choreography:** Services emit events, others react. No central coordinator. Best for loosely coupled workflows.
- **Orchestration:** A central service directs the workflow. Easier to reason about, harder to scale.
- Use choreography by default. Switch to orchestration when the workflow has complex branching or compensating actions.

### Event Bus Selection
| Requirement | Solution |
|-------------|----------|
| At-least-once, ordered | Kafka (partitioned by key) |
| Fanout to many consumers | SNS + SQS or RabbitMQ exchanges |
| Exactly-once semantics | Kafka with idempotent consumers |
| Low latency, simple | Redis Streams or NATS |

## Saga Pattern for Distributed Transactions

- No distributed ACID transactions across services. Use sagas instead.
- **Choreography saga:** Each service publishes an event that triggers the next step. Compensating events undo on failure.
- **Orchestration saga:** A saga coordinator issues commands and handles rollbacks.
- Every step must have a compensating action: CreateOrder -> CancelOrder, ReserveStock -> ReleaseStock.
- Store saga state persistently. Crashes mid-saga must resume, not restart.

## Event Sourcing and CQRS

- **Event sourcing:** Store state changes as an append-only log of events. Rebuild current state by replaying events.
- **CQRS:** Separate read models from write models. Write to the event store, project into read-optimized views.
- Use event sourcing when you need full audit trail or temporal queries. Not every service needs it.
- Snapshots prevent slow replays: store a snapshot every N events.

## API Gateway Patterns

- Single entry point for external clients. Routes to internal services.
- Handle cross-cutting concerns: authentication, rate limiting, request logging, TLS termination.
- **BFF (Backend for Frontend):** Separate gateways per client type (web, mobile, third-party).
- Avoid business logic in the gateway. It should be a thin routing and auth layer.

## Data Consistency Across Services

- Accept eventual consistency. Strong consistency across services requires distributed locks or 2PC, which kill availability.
- **Outbox pattern:** Write events to an outbox table in the same transaction as the data change. A relay process publishes events from the outbox.
- **Change data capture (CDC):** Debezium or similar tools stream database changes as events.
- Idempotent consumers: every event handler must produce the same result if the same event is delivered twice.

## Health Checks and Resilience

- **Liveness probe:** Is the process alive? Restart if not. Do not check dependencies here.
- **Readiness probe:** Can the service handle traffic? Check database connections, required caches.
- **Circuit breaker states:** Closed (normal) -> Open (failing, reject fast) -> Half-Open (test recovery).
- **Bulkhead pattern:** Isolate thread pools per dependency. A slow service should not exhaust all threads.
- **Service discovery:** Use DNS-based (Consul, Kubernetes DNS) or registry-based (Eureka). Avoid hardcoded addresses.

## Common Gotchas

- **Chatty services:** 10 HTTP calls to render one page means your boundaries are wrong. Aggregate or merge services.
- **Shared libraries with business logic:** A shared library that all services depend on couples them. Share only infrastructure utilities.
- **Ignoring schema evolution:** Add fields as optional. Never rename or remove fields without a deprecation period.
- **Testing in isolation only:** Contract tests (Pact) verify that services agree on API shape. Integration tests catch what unit tests miss.
- **Logging without correlation:** Every request needs a trace ID that flows through all services. Without it, debugging distributed failures is impossible.
