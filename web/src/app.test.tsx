import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./app.tsx";

describe("dashboard shell", () => {
  it("shows the view links and the machines view by default", () => {
    window.location.hash = "";
    render(<App />);
    for (const label of ["Machines", "Usage", "Session", "Triage", "Work", "Insights", "Settings"]) {
      expect(screen.getByRole("link", { name: label })).toBeDefined();
    }
    expect(screen.getByRole("heading", { name: "Machines" })).toBeDefined();
  });
});
