import type { JSX } from "react";
import { create } from "@bufbuild/protobuf";
import { FieldMaskSchema } from "@bufbuild/protobuf/wkt";
import {
  HubSettingsSchema,
  SettingsSchema,
  SettingsScopeSchema,
  SettingsSpecSchema,
  UpdateSettingsRequestSchema,
  type Settings,
} from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import { fleetClient } from "../client.ts";
import type { Saver } from "./settings-save.ts";
import styles from "./settings.module.css";

// SuggestionsToggle flips HubSettings.suggestions_in_triage on the fleet
// layer: on means rule-based suggestions also show up as low-severity
// Triage items (ui-contract-map, Settings row).
export function SuggestionsToggle({ fleet, saver }: { fleet: Settings | undefined; saver: Saver }): JSX.Element {
  const on = fleet?.spec?.hub?.suggestionsInTriage === true;
  return (
    <li className={styles.card}>
      <p className={styles.head}>Suggestions in Triage</p>
      <p className={styles.detail}>
        {on
          ? "On: rule-based suggestions also show up as low-severity Triage items."
          : "Off: suggestions stay on the Usage and Insights views only."}
      </p>
      <label>
        <input
          type="checkbox"
          checked={on}
          disabled={saver.notice.status === "busy" || fleet === undefined}
          onChange={() => {
            if (fleet === undefined) {
              return;
            }
            void saver.run(() =>
              fleetClient().updateSettings(
                create(UpdateSettingsRequestSchema, {
                  requestId: crypto.randomUUID(),
                  settings: create(SettingsSchema, {
                    id: fleet.id,
                    spec: create(SettingsSpecSchema, {
                      scope:
                        fleet.spec?.scope ??
                        create(SettingsScopeSchema, { kind: { case: "fleet", value: {} } }),
                      hub: create(HubSettingsSchema, { suggestionsInTriage: !on }),
                    }),
                  }),
                  updateMask: create(FieldMaskSchema, { paths: ["spec.hub.suggestions_in_triage"] }),
                  expectedVersion: fleet.version,
                }),
              ),
            );
          }}
        />{" "}
        Also show suggestions in Triage
      </label>
      <details>
        <summary>Details</summary>
        <p className={styles.detail}>
          UpdateSettings{"{"}spec.hub.suggestions_in_triage{"}"} on the fleet layer.
        </p>
      </details>
    </li>
  );
}
