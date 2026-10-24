import { useState, type JSX } from "react";
import { create } from "@bufbuild/protobuf";
import { FieldMaskSchema } from "@bufbuild/protobuf/wkt";
import {
  SettingsSchema,
  SettingsSpecSchema,
  UpdateSettingsRequestSchema,
  type Settings,
} from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import { SettingValuesSchema } from "../../../gen/ts/fleet/type/v1/settings_pb.ts";
import { fleetClient } from "../client.ts";
import { durationToMinutes, layerLabel, minutesToDuration } from "./settings-helpers.ts";
import type { Saver } from "./settings-save.ts";
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

async function saveValueField(layer: Settings, field: ValueField, text: string): Promise<unknown> {
  const trimmed = text.trim();
  if (field === "share_identity_email") {
    throw new Error("Use the checkbox for the email setting.");
  }
  if (field === "max_active" || field === "max_total") {
    const value = Number(trimmed);
    const limit = field === "max_active" ? 16 : 32;
    if (!Number.isInteger(value) || value < 1 || value > limit) {
      throw new Error(`Use a whole number from 1 to ${limit}.`);
    }
    return fleetClient().updateSettings(
      create(UpdateSettingsRequestSchema, {
        requestId: crypto.randomUUID(),
        settings: create(SettingsSchema, {
          id: layer.id,
          spec: create(SettingsSpecSchema, {
            scope: layer.spec?.scope,
            values: create(SettingValuesSchema, field === "max_active" ? { maxActive: value } : { maxTotal: value }),
          }),
        }),
        updateMask: create(FieldMaskSchema, { paths: [`spec.values.${field}`] }),
        expectedVersion: layer.version,
      }),
    );
  }
  if (field === "idle_threshold") {
    const minutes = Number(trimmed);
    return fleetClient().updateSettings(
      create(UpdateSettingsRequestSchema, {
        requestId: crypto.randomUUID(),
        settings: create(SettingsSchema, {
          id: layer.id,
          spec: create(SettingsSpecSchema, {
            scope: layer.spec?.scope,
            values: create(SettingValuesSchema, { idleThreshold: minutesToDuration(minutes) }),
          }),
        }),
        updateMask: create(FieldMaskSchema, { paths: [`spec.values.${field}`] }),
        expectedVersion: layer.version,
      }),
    );
  }
  if (trimmed === "") {
    throw new Error("The window name cannot be empty.");
  }
  return fleetClient().updateSettings(
    create(UpdateSettingsRequestSchema, {
      requestId: crypto.randomUUID(),
      settings: create(SettingsSchema, {
        id: layer.id,
        spec: create(SettingsSpecSchema, {
          scope: layer.spec?.scope,
          values: create(SettingValuesSchema, { windowNameTemplate: trimmed }),
        }),
      }),
      updateMask: create(FieldMaskSchema, { paths: [`spec.values.${field}`] }),
      expectedVersion: layer.version,
    }),
  );
}

function SettingField({
  layer,
  field,
  saver,
}: {
  layer: Settings;
  field: ValueField;
  saver: Saver;
}): JSX.Element {
  const [text, setText] = useState(() => fieldText(layer, field));
  if (field === "share_identity_email") {
    const on = layer.spec?.values?.shareIdentityEmail === true;
    return (
      <p className={styles.field}>
        <label>
          <input
            type="checkbox"
            checked={on}
            disabled={saver.notice.status === "busy"}
            onChange={() => {
              void saver.run(() =>
                fleetClient().updateSettings(
                  create(UpdateSettingsRequestSchema, {
                    requestId: crypto.randomUUID(),
                    settings: create(SettingsSchema, {
                      id: layer.id,
                      spec: create(SettingsSpecSchema, {
                        scope: layer.spec?.scope,
                        values: create(SettingValuesSchema, { shareIdentityEmail: !on }),
                      }),
                    }),
                    updateMask: create(FieldMaskSchema, { paths: [`spec.values.${field}`] }),
                    expectedVersion: layer.version,
                  }),
                ),
              );
            }}
          />{" "}
          {FIELD_LABEL[field]}
        </label>
      </p>
    );
  }
  return (
    <p className={styles.field}>
      <label htmlFor={`setting-${layer.id?.value ?? "na"}-${field}`}>{FIELD_LABEL[field]}</label>
      <input
        id={`setting-${layer.id?.value ?? "na"}-${field}`}
        value={text}
        inputMode={field === "window_name_template" ? "text" : "numeric"}
        placeholder={field === "window_name_template" ? "{harness} {work}" : "not set"}
        disabled={saver.notice.status === "busy"}
        onChange={(event) => {
          setText(event.target.value);
        }}
      />
      <button
        type="button"
        disabled={saver.notice.status === "busy"}
        onClick={() => {
          void saver.run(() => saveValueField(layer, field, text));
        }}
      >
        Save
      </button>
    </p>
  );
}

// LayerCard shows one layer's setting values. Only the machine layer edits:
// the fleet and group rows are the layers the values were set at, read-only
// (ui-contract-map, Settings row).
export function LayerCard({ layer, saver }: { layer: Settings; saver: Saver }): JSX.Element {
  const editable = layer.spec?.scope?.kind.case === "machine";
  return (
    <li className={styles.card}>
      <p className={styles.head}>
        <span className={styles.chip}>{layerLabel(layer.spec?.scope)}</span>
        Setting values
      </p>
      {editable ? (
        <>
          {EDITABLE_FIELDS.map((field) => (
            <SettingField key={field} layer={layer} field={field} saver={saver} />
          ))}
          <details>
            <summary>Details</summary>
            <p className={styles.detail}>
              UpdateSettings{"{"}spec.values, update_mask: spec.values.&lt;field&gt;{"}"} on the
              machine layer. A machine&apos;s own policy file can only tighten these.
            </p>
          </details>
        </>
      ) : (
        EDITABLE_FIELDS.map((field) => {
          const text = fieldText(layer, field);
          return (
            <p className={styles.detail} key={field}>
              {FIELD_LABEL[field]}: {text === "" ? "not set" : text}
            </p>
          );
        })
      )}
    </li>
  );
}
