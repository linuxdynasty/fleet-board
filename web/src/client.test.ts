import { describe, expect, it } from "vitest";
import { getApprovalSecret, setApprovalSecret } from "./client.ts";

describe("approval secret", () => {
  it("stays empty until the owner types it, then round-trips in the tab", () => {
    window.sessionStorage.clear();
    expect(getApprovalSecret()).toBeNull();
    setApprovalSecret("s3cret");
    expect(getApprovalSecret()).toBe("s3cret");
  });
});
