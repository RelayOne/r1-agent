// SPDX-License-Identifier: MIT
// ESLint flat config for r1-web. Spec item 49/55.
//
// Mirrors the rule set shadcn/ui's CLI generator scaffolds (React +
// React Hooks + TypeScript) without pulling in their opinionated
// `@typescript-eslint/recommended-type-checked` (which requires a
// running tsc service and slows lint considerably). The custom
// data-testid rule lives in eslint-rules/ and is wired in item 53.
import js from "@eslint/js";
import tsEslint from "@typescript-eslint/eslint-plugin";
import tsParser from "@typescript-eslint/parser";
import reactPlugin from "eslint-plugin-react";
import reactHooks from "eslint-plugin-react-hooks";

export default [
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "../internal/server/static/dist/**",
      "src/components/ui/**",
    ],
  },
  js.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parser: tsParser,
      parserOptions: {
        ecmaVersion: 2022,
        sourceType: "module",
        ecmaFeatures: { jsx: true },
      },
      globals: {
        window: "readonly",
        document: "readonly",
        navigator: "readonly",
        console: "readonly",
        setTimeout: "readonly",
        clearTimeout: "readonly",
        setInterval: "readonly",
        clearInterval: "readonly",
        queueMicrotask: "readonly",
        requestAnimationFrame: "readonly",
        cancelAnimationFrame: "readonly",
        indexedDB: "readonly",
        IDBDatabase: "readonly",
        IDBOpenDBRequest: "readonly",
        FileSystemDirectoryHandle: "readonly",
        KeyboardEvent: "readonly",
        Event: "readonly",
        EventTarget: "readonly",
        HTMLElement: "readonly",
        HTMLTextAreaElement: "readonly",
        HTMLButtonElement: "readonly",
        HTMLInputElement: "readonly",
        HTMLPreElement: "readonly",
        HTMLDivElement: "readonly",
        Storage: "readonly",
        MediaQueryListEvent: "readonly",
        process: "readonly",
        globalThis: "readonly",
        Element: "readonly",
        JSX: "readonly",
        React: "readonly",
        RegExpExecArray: "readonly",
        Promise: "readonly",
      },
    },
    plugins: {
      "@typescript-eslint": tsEslint,
      react: reactPlugin,
      "react-hooks": reactHooks,
    },
    settings: {
      react: { version: "18.3" },
    },
    rules: {
      ...tsEslint.configs.recommended.rules,
      ...reactPlugin.configs.recommended.rules,
      ...reactHooks.configs.recommended.rules,
      // shadcn-style relaxations:
      "react/react-in-jsx-scope": "off",
      "react/prop-types": "off",
      "react/display-name": "off",
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/no-explicit-any": "warn",
      "@typescript-eslint/no-empty-object-type": "off",
      "no-empty-pattern": "off",
    },
  },
  {
    files: ["src/**/*.test.{ts,tsx}"],
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
      "react-hooks/rules-of-hooks": "off",
    },
  },
  {
    files: ["src/**/*.stories.{ts,tsx}"],
    rules: {
      "react-hooks/rules-of-hooks": "off",
    },
  },
];
