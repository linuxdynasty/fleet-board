import { useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { setApprovalSecret } from "../client.ts";

export type Notice =
  | { status: "idle" }
  | { status: "busy" }
  | { status: "needs-secret" }
  | { status: "error"; message: string };

export function noticeMessage(err: unknown): Notice {
  if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
    // Usually the local_trusted hub asking for its approval secret
    // (ADR 0032); sometimes a missing scope. Either way the owner
    // types the secret and retries, so ask for it, don't just fail.
    return { status: "needs-secret" };
  }
  if (err instanceof ConnectError && err.code === Code.Aborted) {
    return { status: "error", message: "It changed underneath you. Wait a moment, then try again." };
  }
  return { status: "error", message: err instanceof Error ? err.message : String(err) };
}

export interface Saver {
  notice: Notice;
  run: (work: () => Promise<unknown>) => Promise<void>;
  saveSecret: (secret: string) => void;
}

// useSaver serializes the view's saves through one notice slot: while one
// save is in flight the inputs are disabled, and the next save reuses the
// same slot. A network retry reuses its own request_id (fleet-frontend);
// a second deliberate click gets a new key because each save mints one.
export function useSaver(): Saver {
  const [notice, setNotice] = useState<Notice>({ status: "idle" });
  const run = async (work: () => Promise<unknown>): Promise<void> => {
    setNotice({ status: "busy" });
    try {
      await work();
      setNotice({ status: "idle" });
    } catch (err) {
      setNotice(noticeMessage(err));
    }
  };
  return {
    notice,
    run,
    saveSecret: (secret: string) => {
      setApprovalSecret(secret);
      setNotice({ status: "idle" });
    },
  };
}
