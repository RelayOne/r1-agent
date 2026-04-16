# Cedar policy bundles

These bundles declare default capability scopes for delegation (STOKE-014/015).
Stoke itself consumes them via the TrustPlane policy SDK; operators can load a
bundle via:

```bash
stoke policy apply <bundle-name>
```

## Bundles

- `default-blocked.cedar` — the base deny-list: no recursive delegation, no
  `send_message` on behalf of the user, no `execute_code`, no memory writes.
  Every other bundle extends this one rather than replacing it.
- `read-only-calendar.cedar` — can read calendar events; cannot create /
  modify / delete.
- `read-only-email.cedar` — can read inbox + search messages; cannot reply,
  forward, or send.
- `send-on-behalf-of.cedar` — can send messages on the user's behalf within
  declared recipient allow-list.
- `schedule-on-behalf-of.cedar` — can create / modify calendar events within
  declared calendar-id scope.
- `hire-from-trustplane.cedar` — can discover + hire agents from TrustPlane
  marketplace within declared budget and reputation floor.

## Authoring

Each bundle is a standalone Cedar policy file. Keep the blast radius small —
one bundle should grant one coherent scope. Cross-bundle composition happens
at policy-apply time when the operator stacks multiple bundles.

These files are currently static. A future iteration will load them via the
TrustPlane Cedar SDK for evaluation at every delegated-session tool
invocation (STOKE-015).
