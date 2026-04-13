# barrel-export-uniqueness

> Why `export *` across multiple barrels blows up, and how shadcn/ui and Zod schema barrels fail specifically in monorepo packages

<!-- keywords: export *, barrel, re-export, TS2308, TS2484, duplicate export, shadcn/ui, Zod, index.ts -->

## TS2308: "Module has already exported a member"

When a barrel `index.ts` does `export *` from two sibling modules that both export the same identifier name, TypeScript raises TS2308:

```
error TS2308: Module './button' has already exported a member named 'cn'.
Consider explicitly re-exporting to resolve the ambiguity.
```

The ESM spec says ambiguous star-re-exports are silently omitted from the namespace. TypeScript, in a typed barrel, refuses to compile rather than silently drop the name. Fix: replace `export *` with explicit named re-exports for the offending files.

```ts
// BAD — both files happen to export `cn`, TS2308 fires
export * from './button'
export * from './alert-dialog'

// GOOD — name exactly what you want from each file
export { Button, buttonVariants } from './button'
export { AlertDialog, AlertDialogTrigger, AlertDialogContent, AlertDialogCancel } from './alert-dialog'
```

## shadcn/ui barrels

shadcn/ui components are copied into your repo and each file frequently exports a `cn` helper, a variants object, or overlapping names. Typical offenders:

- `button.tsx` exports `Button`, `buttonVariants`.
- `alert-dialog.tsx` exports `AlertDialog`, `AlertDialogTrigger`, ..., `AlertDialogCancel`.
- Several component files import from `@/lib/utils` and some older scaffolds re-export `cn`.

A naive `components/ui/index.ts` that does `export * from './button'; export * from './alert-dialog'; ...` eventually collides. Use named re-exports from the start.

```ts
// components/ui/index.ts
export { Button, buttonVariants } from './button'
export {
  AlertDialog,
  AlertDialogTrigger,
  AlertDialogContent,
  AlertDialogCancel,
  AlertDialogAction,
} from './alert-dialog'
```

## Zod schema barrels

Zod conventions produce both a value and a type per schema:

```ts
// schemas/alarm.ts
export const alarmSchema = z.object({ id: z.string(), ... })
export type Alarm = z.infer<typeof alarmSchema>
```

Across multiple schema files you often get parallel names (`Alarm`, `alarmSchema` in one file; `Alarm` re-derived somewhere else). A barrel that stars over all schema files will collide at the first duplicate name.

## TS2484: "Export declaration conflicts"

Mixing `export *` with explicit `export type {}` for names that also come through the star causes TS2484:

```
error TS2484: Export declaration conflicts with exported declaration of 'Alarm'.
```

```ts
// BAD — Alarm arrives via the star AND via the explicit type re-export
export * from './alarm'
export type { Alarm } from './alarm'

// GOOD — pick one style
export * from './alarm'
// (Alarm is already exported by the star)

// or
export { alarmSchema } from './alarm'
export type { Alarm } from './alarm'
```

## interfaces/* + schemas/* collisions

When a package re-exports from both `interfaces/*` (TypeScript-only types) and `schemas/*` (Zod value + inferred type), the same name often exists in both trees:

```ts
// interfaces/alarm.ts
export interface Alarm { /* ... */ }

// schemas/alarm.ts
export const alarmSchema = z.object({ /* ... */ })
export type Alarm = z.infer<typeof alarmSchema>

// index.ts — collision
export * from './interfaces/alarm'
export * from './schemas/alarm'
```

Fixes:

1. Rename one side (`AlarmEntity` vs `Alarm`).
2. Drop one source — usually keep the Zod-inferred type and delete the hand-written interface.
3. Use selective named exports so the barrel only re-exports non-overlapping names.

## Diagnostic

Find every duplicate-export error in a package:

```bash
pnpm --filter <pkg> exec tsc --noEmit 2>&1 | grep "TS2308\|TS2484"
```

Each hit names the duplicate identifier and the two files involved. Fix by switching the listed file to named re-exports.

## Gotchas

- **`export *` is convenient and dangerous**: the moment two source files share a name, compilation breaks. Prefer named re-exports in any barrel with more than two sources.
- **TS2308 = star+star collision**: two `export *` statements in the same barrel expose the same name.
- **TS2484 = star + explicit collision**: `export *` and `export { X }` (or `export type { X }`) for a name the star already covered.
- **shadcn/ui barrels collide on shared utils**: re-export only the component names you need, not `*`.
- **Zod `schemas/*` + `interfaces/*` often define the same type name**: pick one source or rename.
- **Type-only re-exports still count**: `export type { Alarm }` collides with a previous `export *` that included `Alarm`.
- **Silent ESM behavior vs TS behavior**: plain ESM silently excludes ambiguous names; TypeScript fails the build. Don't assume "it runs in node" means "it type-checks".
- **Fix at the barrel, not the source**: renaming a component in `button.tsx` to dodge the conflict ripples through every importer. Fix the `export *` to be a named re-export instead.
