import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "happy-dom",
    // Component tests live in src/; the browser smoke under e2e/ runs
    // through Playwright (`npm run e2e`), never through vitest.
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
