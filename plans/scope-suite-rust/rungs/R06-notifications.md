# R06-rust — notification preferences API with JSON schema

Axum CRUD for a notification preferences resource. Exercises Zod-style
validation (with `validator` crate or serde + manual checks), per-user
GET/PUT, and JSON response shapes.

## Scope

Binary crate `notify-api`. In-memory store (DashMap or
`RwLock<HashMap<UserId, NotificationPrefs>>`).

Model:
```rust
struct NotificationPrefs {
    email: bool,
    sms: bool,
    push: bool,
    digest: DigestSchedule,      // "off" | "daily" | "weekly"
    quiet_hours: Option<QuietHours>,
}
struct QuietHours { start: String, end: String } // "HH:MM"
enum DigestSchedule { Off, Daily, Weekly }
```

Endpoints:
- `GET /prefs/:user_id` → 200 with current prefs or 404 if unknown.
- `PUT /prefs/:user_id` → 200 echoing merged prefs, 400 on invalid.

Validation: `digest` must be one of the three strings; `quiet_hours`
entries must match `\d\d:\d\d`. Invalid input → 400 with `{ "error": "<reason>" }`.

Default prefs on first PUT (`GET` before any PUT → 404 unless a
default user is seeded):
```rust
NotificationPrefs { email: true, sms: false, push: true, digest: DigestSchedule::Daily, quiet_hours: None }
```

## Acceptance

- `Cargo.toml`: axum, tokio full, serde (derive), serde_json, regex,
  dev reqwest.
- At least 4 tests covering: GET unknown user → 404, PUT valid →
  returns echoed prefs, PUT invalid digest → 400, GET after PUT
  returns the stored value.
- `cargo build` + `cargo test` exit 0.
- `cargo clippy -- -D warnings` exits 0.

## What NOT to do

- No database.
- No authentication.
- No pagination / listing endpoints.
