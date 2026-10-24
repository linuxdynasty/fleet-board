// Connect client for the hub API (fleet.hub.v1). The messages and the
// service descriptor are generated from api/fleet (buf generate puts
// them in gen/ts); no request is hand-written here.
import { createConnectTransport } from "@connectrpc/connect-web";
import { createClient, type Client } from "@connectrpc/connect";
import { FleetService } from "../../gen/ts/fleet/hub/v1/service_pb.ts";

const SECRET_KEY = "fleet.approvalSecret";

// getApprovalSecret returns the local approval secret the owner typed
// into the dashboard this tab session, or null when none was entered.
// It lives in sessionStorage so it never touches disk (ADR 0032: the
// operator's secret stays with the operator).
export function getApprovalSecret(): string | null {
  try {
    return window.sessionStorage.getItem(SECRET_KEY);
  } catch {
    return null;
  }
}

// setApprovalSecret keeps the secret for this tab session only.
export function setApprovalSecret(secret: string): void {
  try {
    window.sessionStorage.setItem(SECRET_KEY, secret);
  } catch {
    // Private mode or no storage: the next refused action re-asks.
  }
}

let client: Client<typeof FleetService> | null = null;

// fleetClient returns the shared FleetService client. The dev server
// proxies /fleet.hub.v1.* to the hub (vite.config.ts); the embedded
// build serves the API and the dashboard from the same origin. Every
// request carries the tab's approval secret when one was entered, so
// writes work on a local_trusted hub (ADR 0032); other hubs ignore it.
export function fleetClient(): Client<typeof FleetService> {
  if (client === null) {
    const transport = createConnectTransport({
      baseUrl: "/",
      interceptors: [
        (next) => async (req) => {
          const secret = getApprovalSecret();
          if (secret !== null && secret !== "") {
            req.header.set("X-Fleet-Approval-Secret", secret);
          }
          return next(req);
        },
      ],
    });
    client = createClient(FleetService, transport);
  }
  return client;
}
