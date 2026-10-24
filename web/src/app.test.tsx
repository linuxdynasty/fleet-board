import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./app.tsx";

describe("dashboard shell", () => {
  it("shows the view links and the machines view by default", () => {
    window.location.hash = "";
    const { unmount } = render(<App />);
    try {
      for (const label of ["Machines", "Usage", "Session", "Triage", "Work", "Insights", "Settings"]) {
        expect(screen.getByRole("link", { name: label })).toBeDefined();
      }
      expect(screen.getByRole("heading", { name: "Machines" })).toBeDefined();
    } finally {
      unmount();
    }
  });

  it("leaves Machines for Work when 2 is pressed", async () => {
    window.location.hash = "#/machines";
    const { unmount } = render(<App />);
    try {
      expect(screen.getByRole("heading", { name: "Machines" })).toBeDefined();
      fireEvent.keyDown(window, { key: "2" });
      expect(await screen.findByRole("heading", { name: "Work" })).toBeDefined();
    } finally {
      unmount();
    }
  });

  it("stays put when a digit is typed into a field", () => {
    window.location.hash = "#/machines";
    const { unmount } = render(<App />);
    const field = document.createElement("input");
    document.body.appendChild(field);
    try {
      fireEvent.keyDown(field, { key: "2" });
      expect(screen.getByRole("heading", { name: "Machines" })).toBeDefined();
      expect(screen.queryByRole("heading", { name: "Work" })).toBeNull();
    } finally {
      field.remove();
      unmount();
    }
  });
});
