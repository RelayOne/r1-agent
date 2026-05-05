// SPDX-License-Identifier: MIT
// Custom ESLint rule: require-data-testid. Spec web-chat-ui item 53/55.
//
// Flags interactive JSX elements (button, input, textarea, select,
// summary, [role="button"]) that don't carry a `data-testid` attribute.
// Storybook stories, shadcn primitives, and tests are out of scope —
// the eslint config in eslint.config.js excludes them via the
// top-level ignores list, so this rule doesn't need to.
//
// The rule fires on:
//   - <button>, <input>, <textarea>, <select>, <summary>, <a> (when
//     it has `href` or `onClick`).
//   - Any JSX element with role="button" / role="link" / role="menuitem".
//
// It does NOT fire when:
//   - The element spreads {...props} (caller likely forwards testid).
//   - The element already has data-testid.
//   - The element is one of shadcn's UI primitives (those should set
//     testids on the consumer side).

const INTERACTIVE_TAGS = new Set([
  "button",
  "input",
  "textarea",
  "select",
  "summary",
]);

const INTERACTIVE_ROLES = new Set(["button", "link", "menuitem", "checkbox", "switch", "tab"]);

function hasAttr(jsx, name) {
  for (const a of jsx.openingElement.attributes) {
    if (a.type === "JSXAttribute" && a.name && a.name.name === name) return true;
  }
  return false;
}

function hasSpread(jsx) {
  for (const a of jsx.openingElement.attributes) {
    if (a.type === "JSXSpreadAttribute") return true;
  }
  return false;
}

function getAttrValue(jsx, name) {
  for (const a of jsx.openingElement.attributes) {
    if (a.type === "JSXAttribute" && a.name && a.name.name === name) {
      if (a.value && a.value.type === "Literal") return a.value.value;
      if (a.value && a.value.type === "StringLiteral") return a.value.value;
    }
  }
  return null;
}

module.exports = {
  meta: {
    type: "problem",
    docs: {
      description:
        "every interactive JSX element must carry a data-testid attribute (web-chat-ui item 53)",
    },
    schema: [],
    messages: {
      missing:
        "interactive <{{tag}}> is missing data-testid. Add data-testid + aria-label per spec web-chat-ui item 53.",
    },
  },
  create(context) {
    return {
      JSXElement(node) {
        const open = node.openingElement;
        if (!open || !open.name) return;
        const tag = open.name.type === "JSXIdentifier" ? open.name.name : null;
        if (!tag) return;

        const isInteractiveTag = INTERACTIVE_TAGS.has(tag);
        const role = getAttrValue(node, "role");
        const isInteractiveRole =
          typeof role === "string" && INTERACTIVE_ROLES.has(role);
        const isAnchor = tag === "a" && (hasAttr(node, "href") || hasAttr(node, "onClick"));

        if (!isInteractiveTag && !isInteractiveRole && !isAnchor) return;

        if (hasAttr(node, "data-testid")) return;
        if (hasSpread(node)) return;

        context.report({
          node: open,
          messageId: "missing",
          data: { tag },
        });
      },
    };
  },
};
