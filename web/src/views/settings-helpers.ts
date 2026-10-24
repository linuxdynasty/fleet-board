import { durationMs, type Duration } from "@bufbuild/protobuf/wkt";
import type { SettingsScope } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";

// providerForHost names the code host behind a repo address. The contract
// stores host and path only (RepoRef); the mockup reads the provider off the
// host the same way (view-settings.js).
export function providerForHost(host: string): string {
  const lower = host.toLowerCase();
  if (lower.includes("gitlab")) {
    return "GitLab";
  }
  if (lower.includes("bitbucket")) {
    return "Bitbucket";
  }
  if (lower.includes("github")) {
    return "GitHub";
  }
  return host;
}

// durationToMinutes reads an idle threshold as whole minutes.
export function durationToMinutes(d: Duration | undefined): number | null {
  if (d === undefined) {
    return null;
  }
  return Math.round(durationMs(d) / 60000);
}

// layerLabel names the layer a Settings row belongs to, in plain words.
export function layerLabel(scope: SettingsScope | undefined): string {
  if (scope === undefined || scope.kind.case === undefined) {
    return "unknown layer";
  }
  switch (scope.kind.case) {
    case "fleet":
      return "fleet default";
    case "groupLabel":
      return `group ${scope.kind.value}`;
    case "machine":
      return `machine ${scope.kind.value.value}`;
  }
}
