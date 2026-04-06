# typescript-patterns

> Advanced TypeScript patterns: type safety, generics, discriminated unions, and strict configuration

<!-- keywords: typescript, type safety, generics, discriminated union, branded types, type guard, utility types -->

## Critical Rules

1. **Enable strict mode from the start.** `"strict": true` in tsconfig. Retrofitting strictness onto a loose codebase is exponentially harder. Non-negotiable for new projects.

2. **Never use `any` as an escape hatch.** Use `unknown` when the type is genuinely unknown, then narrow with type guards. `any` disables type checking and propagates silently.

3. **Discriminated unions over optional fields.** `{ type: "success", data: T } | { type: "error", error: E }` is safer than `{ data?: T, error?: E }` where both could be undefined.

4. **Prefer `as const` over enum for simple value sets.** Const assertions produce literal types with better tree-shaking and no runtime artifact.

5. **Every type assertion (`as`) is a potential bug.** Each one bypasses the compiler. Minimize them. When required, add a comment explaining why the assertion is safe.

## Discriminated Unions

- Define a union of types sharing a literal discriminant field (`kind`, `type`, `status`).
- Use `switch` on the discriminant. Add an `assertNever(x: never): never` default case to catch missing variants at compile time.
- Use for API responses (`{ type: "success", data } | { type: "error", error }`), state machines, and event types.

## Branded and Nominal Types

- Create branded types with intersection: `type UserId = string & { readonly __brand: "UserId" }`.
- Prevents mixing structurally identical values (UserId vs OrderId). Factory functions are the entry point.
- Useful for units (Meters vs Feet), sanitized strings (SafeHtml), and validated inputs (Email).

## Template Literal Types

- Use template literals to enforce string format: `type Route = \`/${string}\``, `type EventName = \`on${Capitalize<string>}\``.
- Combine with mapped types for auto-generating event handler types from event names. Catches typos at compile time.

## Conditional Types and Infer

- `infer` extracts types from within other types: `T extends Promise<infer U> ? U : T` unwraps promises.
- Conditional types distribute over unions by default. Wrap in `[T]` to prevent distribution when unwanted.
- Common patterns: `UnwrapPromise<T>`, `UnwrapArray<T>`, extracting function return types.

## Type Guards and Assertion Functions

- **Type guards** (`value is T`): narrow within `if` blocks. Use for branching logic.
- **Assertion functions** (`asserts value is T`): narrow for the rest of the scope. Use for preconditions.
- Always validate at runtime what you assert at the type level. A wrong type guard is worse than `any`.

## Generic Constraints and Defaults

- Constrain generics to the minimum required shape: `T extends { id: string }` not `T extends any`.
- Use defaults (`type Container<T = string>`) to reduce verbosity at common call sites.
- Avoid more than 3 type parameters. If you need more, the abstraction is probably wrong.

## Module Augmentation and Declaration Merging

- Use `declare module "express"` to extend third-party interfaces (e.g., adding `user` to Express `Request`).
- Use `declare global` to extend `Window` or other global types.
- Place augmentations in a `.d.ts` file included in your tsconfig. Only add properties you actually set.

## Utility Types Reference

| Type | Purpose | Example |
|------|---------|---------|
| `Partial<T>` | All fields optional | Update payloads |
| `Required<T>` | All fields required | Validated config |
| `Pick<T, K>` | Select specific fields | API response subset |
| `Omit<T, K>` | Remove specific fields | Create without ID |
| `Record<K, V>` | Object with known key type | Lookup maps |
| `Extract<T, U>` | Keep union members matching U | Filter event types |
| `Exclude<T, U>` | Remove union members matching U | Remove null from union |
| `NonNullable<T>` | Remove null and undefined | After null check |
| `ReturnType<T>` | Function return type | Infer from existing fn |
| `Parameters<T>` | Function parameter tuple | Wrapper functions |

## Strict Configuration Recommendations

- Enable `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noFallthroughCasesInSwitch`, `forceConsistentCasingInFileNames`.
- `noUncheckedIndexedAccess`: Array/object indexing returns `T | undefined`. Catches out-of-bounds access.
- `exactOptionalPropertyTypes`: Distinguishes missing properties from explicitly `undefined` ones.
- These flags catch real bugs. Enable them on new projects. Adopt incrementally on existing ones.

## Common Gotchas

- **Structural typing surprises:** `{ name: string, age: number }` is assignable to `{ name: string }`. Extra properties are allowed in assignments (but not in object literals).
- **Enums are nominal, but their values are not.** `MyEnum.A === 0` means any `number` can be passed where `MyEnum` is expected. Use string enums or `as const` objects.
- **Type widening:** `let x = "hello"` is `string`, not `"hello"`. Use `const` or `as const` for literal types.
- **`Object.keys` returns `string[]`, not `(keyof T)[]`.** This is intentional due to structural typing. Use a typed helper or type assertion when iterating.
- **Overusing `interface extends`.** Deep inheritance hierarchies are as problematic in types as in classes. Prefer composition with intersection types.
