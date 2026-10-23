// Connect client for the hub API (fleet.hub.v1). The messages and the
// service descriptor are generated from api/fleet (buf generate puts
// them in gen/ts); no request is hand-written here.
import { createConnectTransport } from "@connectrpc/connect-web";
import { createClient, type Client } from "@connectrpc/connect";
import { FleetService } from "../../gen/ts/fleet/hub/v1/service_pb.ts";

let client: Client<typeof FleetService> | null = null;

// fleetClient returns the shared FleetService client. The dev server
// proxies /fleet.hub.v1.* to the hub (vite.config.ts); the embedded
// build serves the API and the dashboard from the same origin.
export function fleetClient(): Client<typeof FleetService> {
  if (client === null) {
    const transport = createConnectTransport({ baseUrl: "/" });
    client = createClient(FleetService, transport);
  }
  return client;
}
