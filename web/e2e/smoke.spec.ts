import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";

import {
  SCREENSHOT_DIR,
  VIEW_HEADINGS,
  VIEWS,
  fleetdUrl,
  screenshotPath,
  viewUrl,
  type ViewName,
} from "./helpers.ts";

// Smoke per view against fleetd on the seed: each of the 7 views renders its
// heading, and a phone-width screenshot lands under e2e/screenshots/ (which
// is gitignored; the shots are described in the PR, never committed).

test.beforeAll(() => {
  fs.mkdirSync(SCREENSHOT_DIR, { recursive: true });
});

// Extra proof per view beyond the heading. Machines carries the seeded
// laptop, which shows the page reads live hub data, not just the shell;
// the seed holds nothing the other views would list, so they assert the
// heading and the screenshot alone.
async function extraCheck(page: Page, view: ViewName): Promise<void> {
  if (view === "machines") {
    await expect(page.getByText("laptop")).toBeVisible();
  }
}

for (const view of VIEWS) {
  test(`the ${view} view renders its heading`, async ({ page }) => {
    await page.goto(viewUrl(fleetdUrl(), view));
    await expect(page.getByRole("heading", { name: VIEW_HEADINGS[view] })).toBeVisible();
    await extraCheck(page, view);
    await page.screenshot({ path: screenshotPath(view), fullPage: true });
  });
}
