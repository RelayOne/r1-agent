# ux-completeness

> Systematic checklist for UI/UX completeness covering reachability, feedback, accessibility, and internationalization.

<!-- keywords: ux, user experience, accessibility, i18n, onboarding, forms -->

## Reachability and Navigation

1. Every feature is reachable within 3 clicks from a primary navigation surface.
2. Breadcrumbs or back-navigation exist for any page deeper than level 2.
3. Deep links work -- users can bookmark or share any meaningful view.
4. Navigation state persists across refreshes (URL reflects current view).
5. Mobile navigation uses bottom sheets or hamburger menus consistently, never both.
6. Keyboard shortcuts exist for power-user workflows; discoverable via `?` or a help modal.

## User Journey Completeness

1. Map every happy path end-to-end: entry -> action -> confirmation -> next state.
2. Identify and handle edge cases: empty inputs, boundary values, concurrent edits, expired sessions.
3. Interruption recovery: if a user leaves mid-flow, they can resume (draft saving, URL state).
4. Destructive actions require explicit confirmation with clear consequences stated.
5. Undo is available for reversible actions (toast with undo button, 5-second window).
6. Error recovery paths never dead-end -- always offer a next step.

## Form Validation Patterns

1. **Inline validation** fires on blur for format checks (email, phone, URL).
2. **On-submit validation** for cross-field rules (password match, date ranges).
3. **Async validation** debounced at 300ms for uniqueness checks (username, email).
4. Error messages are specific: "Email must include @" not "Invalid input".
5. Valid fields show positive confirmation (green check) to reduce anxiety.
6. Disable submit button only while request is in-flight, never as a validation gate.
7. Preserve user input on validation failure -- never clear the form.

## Feedback Patterns

1. **Loading states**: skeleton screens for initial load, spinners for actions < 3s, progress bars for longer operations.
2. **Success feedback**: toast notifications auto-dismiss after 4s, or inline confirmation for critical actions.
3. **Error feedback**: inline for field errors, banner for page-level errors, modal for blocking errors.
4. **Empty states**: illustration + explanation + primary CTA ("No projects yet. Create your first project").
5. **Optimistic UI**: update the interface immediately, revert on failure with explanation.
6. **Rate limiting feedback**: show retry countdown, never silently swallow repeated clicks.

## Onboarding Flows

1. Progressive disclosure: show only what is needed at each step.
2. First-run experience should deliver value within 60 seconds.
3. Offer skip for optional steps; track completion for gentle re-prompts.
4. Use tooltips or coach marks sparingly (max 3-5 per flow).
5. Provide a sample/demo dataset so users can explore without setup.
6. Checklist-style onboarding with visible progress drives completion rates.

## Accessibility (WCAG 2.1 AA)

1. Color contrast ratio >= 4.5:1 for normal text, >= 3:1 for large text.
2. All interactive elements are keyboard-focusable with visible focus indicators.
3. Focus trap inside modals and drawers; restore focus on close.
4. ARIA labels on icon-only buttons: `<button aria-label="Close dialog">`.
5. Screen reader announcements for dynamic content via `aria-live="polite"`.
6. Form inputs have associated `<label>` elements or `aria-labelledby`.
7. Skip-to-content link as the first focusable element.
8. Test with VoiceOver (macOS), NVDA (Windows), and axe-core in CI.

## i18n and l10n Considerations

1. Extract all user-facing strings into message catalogs from day one.
2. Use ICU MessageFormat for plurals, gender, and select patterns.
3. Design for 40% text expansion (German, Finnish) -- avoid fixed-width layouts.
4. Right-to-left (RTL) support: use logical CSS properties (`margin-inline-start` not `margin-left`).
5. Date, number, and currency formatting via `Intl` APIs, never manual string building.
6. Avoid concatenating translated fragments -- use full-sentence translations.
7. Images and icons with embedded text need localized variants.

## Design System Consistency

1. Use design tokens (color, spacing, typography) from a single source of truth.
2. Component library covers 100% of UI patterns -- no one-off styled divs.
3. Spacing scale is consistent (4px base: 4, 8, 12, 16, 24, 32, 48).
4. Typography scale uses no more than 5 distinct sizes with clear hierarchy.
5. Interactive states are uniform: default, hover, active, focus, disabled.
6. Dark mode support via CSS custom properties, not duplicate stylesheets.
