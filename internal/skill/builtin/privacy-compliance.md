# privacy-compliance

> GDPR, CCPA, PIPEDA compliance patterns, PII handling, and data protection

<!-- keywords: gdpr, ccpa, pipeda, privacy, pii, personal data, consent, data protection, anonymize, pseudonymize, retention, right to delete, dpa -->

## Critical Rules

1. **PII must be identifiable in your schema.** Tag every column/field that contains personal data. You cannot comply with deletion requests if you don't know where PII lives.

2. **Consent must be explicit and recorded.** Store what was consented to, when, how (UI element), and version of terms. Consent must be as easy to withdraw as to give.

3. **Data minimization:** Only collect PII you actually need. "We might use it later" is not a legal basis. If you don't need date of birth, don't ask for it.

4. **Retention limits must be enforced automatically.** Define retention periods per data category. Run automated purge jobs. "We'll delete it manually" fails audits.

5. **Cross-border transfer requires legal basis.** EU data to US requires SCCs (Standard Contractual Clauses) or adequacy decision. Don't ignore this.

## GDPR Requirements

### Rights (must be implementable in <30 days)
- **Access (Art. 15):** Export all data about a person in machine-readable format
- **Rectification (Art. 16):** Allow correction of inaccurate data
- **Erasure (Art. 17):** Delete all personal data ("right to be forgotten")
- **Portability (Art. 20):** Export in structured, machine-readable format (JSON/CSV)
- **Restriction (Art. 18):** Mark data as restricted (keep but don't process)
- **Object (Art. 21):** Opt out of processing for specific purposes

### Implementation Pattern
```
User requests deletion →
  1. Verify identity (don't delete wrong person's data)
  2. Find ALL personal data (search every service, backup, log, cache)
  3. Delete or anonymize (replace with tokens, not just soft-delete)
  4. Confirm deletion to user
  5. Record that deletion occurred (without recording deleted data)
```

## CCPA/CPRA (California)

- **"Do Not Sell My Personal Information"** link required on homepage
- **Opt-out of sale:** Must be honored within 15 business days
- **Financial incentive disclosure:** If you give discounts for data, disclose the value
- **Service provider contracts:** Must include CCPA-compliant DPA terms

## PIPEDA (Canada)

- **Meaningful consent:** Must be clear what data is collected and why
- **Accountability:** Designate a privacy officer
- **Limiting collection:** Only collect what's necessary for identified purposes
- **Safeguards:** Appropriate security for sensitivity level

## Technical Patterns

### PII Inventory
Maintain a data map: `table.column → PII category → legal basis → retention`

### Pseudonymization
- Replace identifiers with tokens: `user_id → uuid`
- Store mapping separately with stricter access controls
- Allows analytics without exposing identity

### Anonymization
- Remove ALL identifying fields (name, email, IP, device ID)
- k-anonymity: each record indistinguishable from k-1 others
- Cannot be reversed (unlike pseudonymization)

### Audit Logging
- Log every access to PII: who, what, when, why
- Immutable audit trail (append-only)
- Retention of audit logs independent of data retention

## Common Gotchas

- **Backups contain PII.** Deletion request must cascade to backups or backups must expire within retention period.
- **Logs contain PII.** IP addresses, email addresses, user agents in server logs. Rotate and purge.
- **Third-party SDKs collect PII.** Analytics, crash reporting, ad SDKs collect data you're responsible for.
- **Soft delete ≠ GDPR delete.** `is_deleted=true` still stores the data. Must actually purge or anonymize.
- **Email is PII.** Even hashed email is pseudonymous, not anonymous (can be re-identified).
- **Cookie consent banners.** Must block non-essential cookies BEFORE consent. Pre-checked boxes don't count.
