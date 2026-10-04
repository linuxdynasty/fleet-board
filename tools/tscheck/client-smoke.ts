// Proves the generated hub descriptors work with the Connect client the
// dashboard will use (ADR 0020: @connectrpc/connect and connect-web v2).
// Compile-only: nothing here runs and nothing is shipped.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { FleetService } from "../../gen/ts/fleet/hub/v1/service_pb.js";

const transport = createConnectTransport({ baseUrl: "http://localhost" });

export const hub = createClient(FleetService, transport);
