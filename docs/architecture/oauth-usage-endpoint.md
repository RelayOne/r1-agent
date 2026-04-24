# OAuth Usage Endpoint

R1 polls an undocumented Anthropic OAuth endpoint to track subscription
utilization for rate-limit management.

## Endpoint

```
GET https://api.anthropic.com/api/oauth/usage
```

## Request Headers

```
Authorization: Bearer <oauth_access_token>
anthropic-beta: oauth-2025-04-20
User-Agent: stoke/0.1.0
```

## Response Schema

```json
{
  "five_hour": {
    "utilization": 42.5,
    "resets_at": "2026-04-08T15:30:00Z"
  },
  "seven_day": {
    "utilization": 12.3,
    "resets_at": "2026-04-14T00:00:00Z"
  },
  "seven_day_opus": {
    "utilization": 5.0,
    "resets_at": "2026-04-14T00:00:00Z"
  }
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `five_hour.utilization` | float64 | 0-100, percentage of 5-hour window consumed |
| `five_hour.resets_at` | *time.Time | When the 5-hour window resets (null if unused) |
| `seven_day.utilization` | float64 | 0-100, percentage of 7-day window consumed |
| `seven_day.resets_at` | *time.Time | When the 7-day window resets |
| `seven_day_opus.utilization` | float64 | 0-100, Opus-specific 7-day window |
| `seven_day_opus.resets_at` | *time.Time | When the Opus window resets |

## Risk

This is the highest single-point-of-failure in the codebase. If Anthropic
changes this endpoint's URL, headers, or response schema, all rate-limit
tracking silently breaks. The poller will log errors but the circuit breaker
will not trip (it only trips on consecutive execution failures, not polling failures).

## Contract Test

`internal/subscriptions/usage_contract_test.go` validates the response schema
against a known-good fixture. Run with:

```bash
go test ./internal/subscriptions/ -run TestUsageResponseSchema
```

## Implementation

- **Poller**: `internal/subscriptions/usage.go` — `PollClaudeUsage()` and `StartPoller()`
- **Manager**: `internal/subscriptions/manager.go` — `UpdateUtilization()` updates pool status
- **Thresholds**: 95% = exhausted, 80% = throttled (configurable in manager)

## Fix path when it breaks

1. Check the contract test (`TestUsageResponseSchema`)
2. Compare the actual response against the documented schema above
3. Update `UsageData` struct in `usage.go`
4. Update the contract test fixture
5. Update this document
