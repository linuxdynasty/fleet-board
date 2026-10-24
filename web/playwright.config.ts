import { defineConfig } from "@playwright/test";

// Smoke run for the built dashboard: the global setup starts fleetd on the
// seed and serves the built page from it, so the base URL is only known at
// run time (see e2e/helpers.ts). Phone width, one Chromium project.
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/setup.ts",
  globalTeardown: "./e2e/teardown.ts",
  timeout: 60_000,
  use: {
    viewport: { width: 390, height: 844 },
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
});
