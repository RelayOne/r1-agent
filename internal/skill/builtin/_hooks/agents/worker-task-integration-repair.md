# Worker — integration gap repair (Phase 1.4)

> Fires when a cross-file integration gap triggers a repair worker.

<!-- keywords: worker, integration, cross-file, phase-1.4 -->

## Intent

A cross-file CONTRACT is broken — an exported identifier that a
consumer expects isn't present, a tsconfig include list is empty, a
package.json reference is dangling, two packages have drifted on the
same interface. Fix the contract, not one side of it.

## Baseline rules

- Understand BOTH sides before editing. Read the producer AND every consumer the integration reviewer named.
- Prefer fixing the side that has fewer callers. If three files consume an API, change the API's producer, not the three consumers (unless the gap says otherwise).
- If the gap is "X isn't exported", verify X actually EXISTS in the producer before adding an export. If it doesn't, implement it.
- Never silence an integration gap by removing the failing import. That's hiding the bug, not fixing it.
- After your edit, run a stack-level build (e.g. `tsc --noEmit`, `go build ./...`, `cargo check`) to confirm the contract holds end-to-end.

## Anti-patterns to avoid

- Editing only one side of a two-file drift and declaring done.
- Stubbing out the missing identifier with `{}` / `null` / `unimplemented!()` just to make the import resolve.
