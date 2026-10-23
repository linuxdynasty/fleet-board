// The Watch hook: one FleetService.Watch server stream per tab, with the
// resume token, RESYNC_REQUIRED handling, and reconnects that show a
// "reconnecting" state instead of stale data (fleet-frontend). Views pass
// their subscriptions and a per-message callback; dedupe by (id,
// version) happens in the view, because only the view knows the key.
import { useEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import type { WatchResponse } from "../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeType,
  TopicSubscriptionSchema,
  WatchRequestSchema,
  type WatchTopic,
} from "../../gen/ts/fleet/hub/v1/watch_pb.ts";
import { fleetClient } from "./client.ts";

export type WatchState = "live" | "reconnecting";

export interface WatchHandlers {
  onResponse: (msg: WatchResponse) => void;
  // Called with RESYNC_REQUIRED: the view drops its local state, then
  // the hook reopens with send_initial and no token.
  onResync: () => void;
}

function backoffMs(attempt: number): number {
  const capped = Math.min(attempt, 6);
  const base = 500 * 2 ** capped;
  return base + Math.floor(Math.random() * 500);
}

export interface WatchSubscription {
  topic: WatchTopic;
  filter?: string;
}

export function useWatch(
  subscriptions: WatchSubscription[],
  handlers: WatchHandlers,
): WatchState {
  const [state, setState] = useState<WatchState>("reconnecting");
  const handlersRef = useRef(handlers);
  useEffect(() => {
    handlersRef.current = handlers;
  });
  const subsKey = JSON.stringify(
    subscriptions.map((s) => ({ topic: s.topic, filter: s.filter ?? "" })),
  );

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const subs = JSON.parse(subsKey) as WatchSubscription[];

    const open = async (resumeToken: string, attempt: number): Promise<void> => {
      if (cancelled) {
        return;
      }
      setState(attempt === 0 ? "live" : "reconnecting");
      try {
        const stream = fleetClient().watch(
          create(WatchRequestSchema, {
            subscriptions: subs.map((s) =>
              create(TopicSubscriptionSchema, {
                topic: s.topic,
                filter: s.filter ?? "",
                // A fresh open (or a resync) asks for the initial state;
                // a resume continues from the token instead.
                sendInitial: resumeToken === "",
              }),
            ),
            resumeToken,
          }),
        );
        for await (const msg of stream) {
          if (cancelled) {
            return;
          }
          if (msg.changeType === ChangeType.RESYNC_REQUIRED) {
            handlersRef.current.onResync();
            await open("", 0);
            return;
          }
          handlersRef.current.onResponse(msg);
          resumeToken = msg.resumeToken;
          attempt = 0;
        }
        // The stream ended without resync: reopen from the token.
        throw new Error("watch stream closed");
      } catch (err) {
        if (cancelled) {
          return;
        }
        if (err instanceof ConnectError) {
          // A permission loss stays visible instead of looping quietly.
          if (err.code === Code.PermissionDenied || err.code === Code.Unauthenticated) {
            throw err;
          }
        }
        timer = setTimeout(() => {
          void open(resumeToken, attempt + 1);
        }, backoffMs(attempt));
      }
    };

    void open("", 0);
    return () => {
      cancelled = true;
      if (timer !== null) {
        clearTimeout(timer);
      }
    };
  }, [subsKey]);

  return state;
}
