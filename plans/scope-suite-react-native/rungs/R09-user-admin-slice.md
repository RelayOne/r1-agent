# R09-rn — User admin slice (list + detail + edit with mocked API)

Multi-screen RN slice: user list → user detail → edit form. All API
calls mocked via fetch spy. Exercises nav, state per screen, and
form submission flows.

## Scope

Project structure:
- `App.tsx` — NavigationContainer + stack nav of UserList →
  UserDetail → UserEdit.
- `src/api/users.ts`:
  - `listUsers(): Promise<User[]>` → GET /api/users
  - `getUser(id): Promise<User>` → GET /api/users/:id
  - `updateUser(id, partial): Promise<User>` → PATCH /api/users/:id
- `src/screens/UserList.tsx`: fetches on mount, renders `<Pressable testID="user-${id}">`. Shows loading/error states.
- `src/screens/UserDetail.tsx`: fetches single user, shows fields +
  "Edit" button.
- `src/screens/UserEdit.tsx`: form with name + email inputs
  (`testID="name"`, `testID="email"`), `<Pressable testID="save">` calls
  updateUser then nav.goBack().

- `__tests__/` covers:
  - UserList loading → rendered list with 2 users.
  - UserList 500 → error shown.
  - UserDetail renders selected user name + email.
  - UserEdit submit calls updateUser + nav.goBack.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- At least 4 tests pass.
- `global.fetch` always mocked (no real network).

## What NOT to do

- No TanStack Query or SWR; plain fetch.
- No real API server.
- No authentication.
