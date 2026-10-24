// ESLint for the dashboard. Type-aware rules wait on typescript-eslint
// supporting TypeScript 7 (its peer range caps at 6.x, installed with
// --force; the parser only reads our TS 6-compatible syntax); tsc
// --noEmit is the type gate until then. Hooks rules run here.
import tsParser from "@typescript-eslint/parser";
import reactHooks from "eslint-plugin-react-hooks";

export default [
  {
    ignores: ["dist/**", "node_modules/**"],
  },
  {
    files: ["src/**/*.ts", "src/**/*.tsx", "e2e/**/*.ts", "playwright.config.ts"],
    languageOptions: {
      parser: tsParser,
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "no-unused-vars": "off",
    },
  },
];
