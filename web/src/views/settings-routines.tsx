import { useState, type JSX } from "react";
import { create } from "@bufbuild/protobuf";
import { FieldMaskSchema } from "@bufbuild/protobuf/wkt";
import {
  CronTriggerSchema,
  RoutineBuiltin,
  RoutineSchema,
  RoutineSpecSchema,
  RoutineTriggerSchema,
  UpdateRoutineRequestSchema,
  type Routine,
  type RoutineTrigger,
} from "../../../gen/ts/fleet/hub/v1/routine_pb.ts";
import { HarnessSchema, type Harness } from "../../../gen/ts/fleet/type/v1/ids_pb.ts";
import { fleetClient } from "../client.ts";
import { DAY_NAMES, formatWeeklyCron, parseWeeklyCron, routineScheduleText } from "./settings-helpers.ts";
import type { Saver } from "./settings-save.ts";
import styles from "./settings.module.css";

// RoutineRow shows one routine with its on/off switch and, for the built-in
// weekly insights report, its day, time, harness and model. Edits go through
// UpdateRoutine with one spec mask path each; a built-in routine is never
// offered a delete (disable it instead).
export function RoutineRow({ routine, saver }: { routine: Routine; saver: Saver }): JSX.Element {
  const spec = routine.spec ?? create(RoutineSpecSchema, {});
  const weekly =
    spec.trigger?.kind.case === "cron" ? parseWeeklyCron(spec.trigger.kind.value.expression) : null;
  const [day, setDay] = useState(() => weekly?.day ?? 1);
  const [time, setTime] = useState(
    () => `${String(weekly?.hour ?? 7).padStart(2, "0")}:${String(weekly?.minute ?? 0).padStart(2, "0")}`,
  );
  const [timezone, setTimezone] = useState(() =>
    spec.trigger?.kind.case === "cron" ? (spec.trigger.kind.value.timezone ?? "") : "",
  );
  const [harness, setHarness] = useState(() => spec.harness?.name ?? "");
  const [model, setModel] = useState(() => spec.model ?? "");
  const [rawExpression, setRawExpression] = useState(() =>
    spec.trigger?.kind.case === "cron" ? spec.trigger.kind.value.expression : "",
  );
  const isWeeklyReport = spec.builtin === RoutineBuiltin.INSIGHTS_WEEKLY;
  const key = routine.id?.value ?? spec.displayName;

  const cronTrigger = (expression: string, zone: string): RoutineTrigger =>
    create(RoutineTriggerSchema, {
      kind: {
        case: "cron",
        value: create(CronTriggerSchema, { expression, timezone: zone }),
      },
    });

  const update = (
    updateMask: string[],
    // The fields to send; only masked fields are read by the hub.
    partial: {
      enabled?: boolean;
      trigger?: RoutineTrigger;
      harness?: Harness;
      model?: string;
    },
  ): Promise<unknown> =>
    fleetClient().updateRoutine(
      create(UpdateRoutineRequestSchema, {
        requestId: crypto.randomUUID(),
        routine: create(RoutineSchema, {
          id: routine.id,
          spec: create(RoutineSpecSchema, { enabled: spec.enabled, ...partial }),
        }),
        updateMask: create(FieldMaskSchema, { paths: updateMask }),
        expectedVersion: routine.version,
      }),
    );

  const saveSchedule = (): Promise<unknown> => {
    if (weekly === null) {
      const parsed = parseWeeklyCron(rawExpression.trim());
      if (parsed === null) {
        return Promise.reject(new Error("Use a weekly time like “0 7 * * 1” (minute hour * * weekday)."));
      }
      return update(["spec.trigger"], {
        trigger: cronTrigger(rawExpression.trim(), timezone),
      });
    }
    const match = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
    if (match === null) {
      return Promise.reject(new Error("Use a time like 07:00."));
    }
    return update(["spec.trigger"], {
      trigger: cronTrigger(formatWeeklyCron(day, Number(match[1]), Number(match[2])), timezone),
    });
  };

  return (
    <li className={styles.card}>
      <p className={styles.head}>
        <span className={styles.chip}>{spec.enabled ? "on" : "off"}</span>
        {spec.displayName === "" ? "routine" : spec.displayName}
        {isWeeklyReport && <span className={styles.chip}>weekly report</span>}
      </p>
      <p className={styles.detail}>{routineScheduleText(spec.trigger)}</p>
      <div className={styles.actions}>
        <button
          type="button"
          disabled={saver.notice.status === "busy"}
          onClick={() => {
            void saver.run(() => update(["spec.enabled"], { enabled: !spec.enabled }));
          }}
        >
          Turn {spec.enabled ? "off" : "on"}
        </button>
      </div>
      {spec.trigger?.kind.case === "cron" && weekly !== null ? (
        <div className={styles.field}>
          <label htmlFor={`routine-day-${key}`}>Day</label>
          <select
            id={`routine-day-${key}`}
            value={day}
            disabled={saver.notice.status === "busy"}
            onChange={(event) => {
              setDay(Number(event.target.value));
            }}
          >
            {DAY_NAMES.map((name, index) => (
              <option key={name} value={index}>
                {name}
              </option>
            ))}
          </select>
          <label htmlFor={`routine-time-${key}`}>Time</label>
          <input
            id={`routine-time-${key}`}
            type="time"
            value={time}
            disabled={saver.notice.status === "busy"}
            onChange={(event) => {
              setTime(event.target.value);
            }}
          />
          <label htmlFor={`routine-zone-${key}`}>Timezone</label>
          <input
            id={`routine-zone-${key}`}
            value={timezone}
            placeholder="UTC"
            disabled={saver.notice.status === "busy"}
            onChange={(event) => {
              setTimezone(event.target.value);
            }}
          />
          <button
            type="button"
            disabled={saver.notice.status === "busy"}
            onClick={() => {
              void saver.run(saveSchedule);
            }}
          >
            Save time
          </button>
        </div>
      ) : (
        <p className={styles.field}>
          <label htmlFor={`routine-cron-${key}`}>Schedule</label>
          <input
            id={`routine-cron-${key}`}
            value={rawExpression}
            placeholder="0 7 * * 1"
            disabled={saver.notice.status === "busy"}
            onChange={(event) => {
              setRawExpression(event.target.value);
            }}
          />
          <button
            type="button"
            disabled={saver.notice.status === "busy"}
            onClick={() => {
              void saver.run(saveSchedule);
            }}
          >
            Save schedule
          </button>
        </p>
      )}
      <p className={styles.field}>
        <label htmlFor={`routine-harness-${key}`}>Harness</label>
        <input
          id={`routine-harness-${key}`}
          value={harness}
          placeholder="any"
          list={`harness-options-${key}`}
          disabled={saver.notice.status === "busy"}
          onChange={(event) => {
            setHarness(event.target.value);
          }}
        />
        <datalist id={`harness-options-${key}`}>
          {["claude", "codex", "muse", "cursor", "opencode"].map((name) => (
            <option key={name} value={name} />
          ))}
        </datalist>
        <button
          type="button"
          disabled={saver.notice.status === "busy"}
          onClick={() => {
            void saver.run(() =>
              update(["spec.harness"], { harness: create(HarnessSchema, { name: harness.trim() }) }),
            );
          }}
        >
          Save harness
        </button>
      </p>
      <p className={styles.field}>
        <label htmlFor={`routine-model-${key}`}>Model</label>
        <input
          id={`routine-model-${key}`}
          value={model}
          placeholder="harness default"
          disabled={saver.notice.status === "busy"}
          onChange={(event) => {
            setModel(event.target.value);
          }}
        />
        <button
          type="button"
          disabled={saver.notice.status === "busy"}
          onClick={() => {
            void saver.run(() => update(["spec.model"], { model: model.trim() }));
          }}
        >
          Save model
        </button>
      </p>
      <details>
        <summary>Details</summary>
        <p className={styles.detail}>
          UpdateRoutine{"{"}spec.enabled, spec.trigger, spec.harness, spec.model{"}"}. A built-in
          routine cannot be deleted; turn it off instead.
        </p>
      </details>
    </li>
  );
}
