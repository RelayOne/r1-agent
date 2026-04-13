# [Feature Name] — Implementation Spec

## Overview
<!-- 1 paragraph: what, why, who -->

## Stack & Versions
<!-- Exact. e.g. "Node 20, TypeScript 5.4, Drizzle 0.30" -->

## Existing Patterns to Follow
- Endpoint: `src/routes/example.ts`
- Tests: `tests/routes/example.test.ts`
- Errors: `src/utils/errors.ts`
- DB: `src/db/queries/example.ts`
- Auth: existing `src/services/auth.ts` — do NOT build custom

## Library Preferences
- Validation: [zod/joi/existing]
- HTTP: [axios/fetch/existing]

## Data Models
### [Entity]
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|

## API Endpoints
### [METHOD] [/path]
**Auth:** required | public
**Request:** `{}`
**Success (201):** `{}`
**Errors:**
| Status | When | Body |
|--------|------|------|

## Business Logic
### [Operation]
1. Validate: [checks]  2. Execute: [DB op]  3. Side effects: [what]  4. Return: [shape]

## Error Handling
| Failure | Strategy | User Sees |
|---------|----------|-----------|

## Boundaries — What NOT To Do
- Do NOT modify [files]
- Do NOT build custom [thing] — use [existing]

## Testing
### [Deliverable]
- [ ] Happy: [input] → [exact output]
- [ ] Error: [input] → [status + error key]
- [ ] Edge: [boundary with values]

## Acceptance Criteria
- WHEN [condition] THE SYSTEM SHALL [measurable behavior]

## Implementation Checklist
<!-- Each item: what to build + files + patterns + libraries + errors + criteria -->
1. [ ] [Detailed self-contained item]
