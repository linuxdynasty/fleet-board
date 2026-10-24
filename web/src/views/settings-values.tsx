import type { JSX } from "react";
import type { Settings } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import { durationToMinutes, layerLabel } from "./settings-helpers.ts";
import styles from "./settings.module.css";

export type ValueField =
  | "max_active"
  | "max_total"
  | "idle_threshold"
  | "window_name_template"
  | "share_identity_email";

const FIELD_LABEL: Record<ValueField, string> = {
  max_active: "Goals using quota at once",
  max_total: "Goals in total",
  idle_threshold: "Idle after (minutes)",
  window_name_template: "Window name",
  share_identity_email: "Share account email",
};

const EDITABLE_FIELDS: ValueField[] = [
  "max_active",
  "max_total",
  "idle_threshold",
  "window_name_template",
  "share_identity_email",
];

function fieldText(s: Settings, field: ValueField): string {
  const values = s.spec?.values;
  if (values === undefined) {
    return "";
  }
  switch (field) {
    case "max_active":
      return values.maxActive === undefined ? "" : String(values.maxActive);
    case "max_total":
      return values.maxTotal === undefined ? "" : String(values.maxTotal);
    case "idle_threshold":
      return values.idleThreshold === undefined ? "" : String(durationToMinutes(values.idleThreshold) ?? "");
    case "window_name_template":
      return values.windowNameTemplate ?? "";
    case "share_identity_email":
      return values.shareIdentityEmail === true ? "on" : "off";
  }
}

// LayerCard shows one layer's setting values, read-only for now. The hub
// takes UpdateSettings on the fleet layer only (group and machine layers
// follow with their management), so the per-layer edit controls stay out
// until the hub serves them. Editing the fleet suggestions switch below is
// the one write this slice offers.
export function LayerCard({ layer }: { layer: Settings }): JSX.Element {
  return (
    <li className={styles.card}>
      <p className={styles.head}>
        <span className={styles.chip}>{layerLabel(layer.spec?.scope)}</span>
        Setting values
      </p>
      {EDITABLE_FIELDS.map((field) => {
        const text = fieldText(layer, field);
        return (
          <p className={styles.detail} key={field}>
            {FIELD_LABEL[field]}: {text === "" ? "not set" : text}
          </p>
        );
      })}
    </li>
  );
}
