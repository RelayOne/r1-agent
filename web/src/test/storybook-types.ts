// SPDX-License-Identifier: MIT
// Local CSF 3 type shims for `<Component>.stories.tsx` files.
//
// Storybook itself is wired in spec 8 (`agentic-test-harness`); until
// then we keep the story files written in canonical CSF 3 form so the
// MCP Storybook runner picks them up unchanged once `@storybook/react`
// is installed. These local shims let `tsc --noEmit` succeed without
// pulling Storybook into the runtime tree (per spec §Boundaries —
// no extra deps, and per the dep-pin rules: Storybook is owned by
// spec 8, not this spec).
//
// The shapes mirror `@storybook/react`'s public CSF 3 surface as of
// Storybook 8.4 (May 2026): `Meta<typeof Component>` and
// `StoryObj<typeof Meta>`. When spec 8 swaps these for the real
// imports, the story files require no edits.
import type { ComponentType } from "react";

/** CSF 3 `Meta` for a component. */
export interface Meta<TComponent extends ComponentType<never> = ComponentType<never>> {
  /** Title path in the Storybook sidebar. */
  title?: string;
  component: TComponent;
  /** Default args applied to every story unless overridden. */
  args?: Partial<ComponentProps<TComponent>>;
  /** Argtypes for controls / docs. */
  argTypes?: Record<string, unknown>;
  /** Per-meta parameters (layout, docs, msw, etc.). */
  parameters?: Record<string, unknown>;
  /** Decorators applied to all stories. */
  decorators?: Array<(Story: ComponentType) => JSX.Element>;
  /** Tags (autodocs etc.). */
  tags?: string[];
}

/** Helper to extract a component's props for `args`. */
type ComponentProps<C> = C extends ComponentType<infer P> ? P : never;

/** CSF 3 `StoryObj`. Accepts either a `Meta` or a component. */
export type StoryObj<TMetaOrComponent> = TMetaOrComponent extends Meta<infer C>
  ? StoryShape<ComponentProps<C>>
  : TMetaOrComponent extends ComponentType<infer P>
    ? StoryShape<P>
    : StoryShape<Record<string, unknown>>;

interface StoryShape<TArgs> {
  args?: Partial<TArgs>;
  argTypes?: Record<string, unknown>;
  parameters?: Record<string, unknown>;
  decorators?: Array<(Story: ComponentType) => JSX.Element>;
  name?: string;
  tags?: string[];
  /** Optional render override. */
  render?: (args: TArgs) => JSX.Element;
  /** Optional play function for interaction tests. */
  play?: (ctx: { canvasElement: HTMLElement }) => Promise<void> | void;
}
