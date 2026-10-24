// Settings view (row 14): the setting values at the layer they are set,
// the watched repos, the routines (including the built-in weekly insights
// report), and the suggestions-in-triage switch. Reads: the SETTINGS, REPOS
// and ROUTINES watch topics. Writes: UpdateSettings (machine-layer values
// and the fleet suggestions switch), UpdateRepo (pause or resume only) and
// UpdateRoutine (on/off, schedule, harness, model; a built-in is never
// deleted). Plain words first; raw contract names live behind details.
import { useMemo, useState, type Dispatch, type FormEvent, type JSX, type SetStateAction } from "react";
import { ChangeType, WatchTopic } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import type { WatchResponse } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import type { Settings } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import type { Repo } from "../../../gen/ts/fleet/hub/v1/repo_pb.ts";
import { RoutineBuiltin, type Routine } from "../../../gen/ts/fleet/hub/v1/routine_pb.ts";
import { useWatch } from "../watch.ts";
import { layerLabel } from "./settings-helpers.ts";
import { useSaver } from "./settings-save.ts";
import { LayerCard } from "./settings-values.tsx";
import { RepoRow } from "./settings-repos.tsx";
import { RoutineRow } from "./settings-routines.tsx";
import { SuggestionsToggle } from "./settings-suggestions.tsx";
import styles from "./settings.module.css";

function settingsKey(s: Settings): string {
  return s.id?.value ?? `${layerLabel(s.spec?.scope)}-${s.version}`;
}

function repoKey(r: Repo): string {
  return r.id?.value ?? `${r.spec?.repo?.host ?? ""}/${r.spec?.repo?.path ?? ""}`;
}

function routineKey(r: Routine): string {
  return r.id?.value ?? r.spec?.displayName ?? "";
}

// upsertRow folds one watched resource into its map, newest version wins.
function upsertRow<T extends { version: bigint }>(
  setRows: Dispatch<SetStateAction<Map<string, T>>>,
  keyOf: (row: T) => string,
  change: ChangeType,
  next: T,
): void {
  setRows((prev) => {
    const rows = new Map(prev);
    if (change === ChangeType.DELETED) {
      rows.delete(keyOf(next));
    } else {
      const seen = rows.get(keyOf(next));
      if (seen === undefined || next.version >= seen.version) {
        rows.set(keyOf(next), next);
      }
    }
    return rows;
  });
}

function layerRank(s: Settings): number {
  const branch = s.spec?.scope?.kind.case;
  if (branch === "fleet") {
    return 0;
  }
  if (branch === "groupLabel") {
    return 1;
  }
  return 2;
}

export function SettingsView(): JSX.Element {
  const [layers, setLayers] = useState<Map<string, Settings>>(new Map());
  const [repos, setRepos] = useState<Map<string, Repo>>(new Map());
  const [routines, setRoutines] = useState<Map<string, Routine>>(new Map());
  const [initialDone, setInitialDone] = useState(false);
  const [secret, setSecret] = useState("");
  const saver = useSaver();
  const state = useWatch(
    useMemo(
      () => [
        { topic: WatchTopic.SETTINGS, sendInitial: true },
        { topic: WatchTopic.REPOS, sendInitial: true },
        { topic: WatchTopic.ROUTINES, sendInitial: true },
      ],
      [],
    ),
    useMemo(
      () => ({
        onResponse: (msg: WatchResponse) => {
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            setInitialDone(true);
            return;
          }
          const resource = msg.resource;
          if (resource.case === "settings" && msg.topic === WatchTopic.SETTINGS) {
            upsertRow(setLayers, settingsKey, msg.changeType, resource.value);
          } else if (resource.case === "repo" && msg.topic === WatchTopic.REPOS) {
            upsertRow(setRepos, repoKey, msg.changeType, resource.value);
          } else if (resource.case === "routine" && msg.topic === WatchTopic.ROUTINES) {
            upsertRow(setRoutines, routineKey, msg.changeType, resource.value);
          }
        },
        onResync: () => {
          setLayers(new Map());
          setRepos(new Map());
          setRoutines(new Map());
          setInitialDone(false);
        },
      }),
      [],
    ),
  );

  const orderedLayers = [...layers.values()].sort((a, b) => {
    const rank = layerRank(a) - layerRank(b);
    return rank !== 0 ? rank : layerLabel(a.spec?.scope).localeCompare(layerLabel(b.spec?.scope));
  });
  const fleet = orderedLayers.find((s) => s.spec?.scope?.kind.case === "fleet");
  const orderedRepos = [...repos.values()].sort((a, b) =>
    `${a.spec?.repo?.host ?? ""}/${a.spec?.repo?.path ?? ""}`.localeCompare(
      `${b.spec?.repo?.host ?? ""}/${b.spec?.repo?.path ?? ""}`,
    ),
  );
  const orderedRoutines = [...routines.values()].sort((a, b) => {
    const aWeekly = a.spec?.builtin === RoutineBuiltin.INSIGHTS_WEEKLY ? 0 : 1;
    const bWeekly = b.spec?.builtin === RoutineBuiltin.INSIGHTS_WEEKLY ? 0 : 1;
    return aWeekly - bWeekly || (a.spec?.displayName ?? "").localeCompare(b.spec?.displayName ?? "");
  });
  const empty = orderedLayers.length === 0 && orderedRepos.length === 0 && orderedRoutines.length === 0;

  const saveSecret = (event: FormEvent): void => {
    event.preventDefault();
    saver.saveSecret(secret);
    setSecret("");
  };

  if (state === "denied") {
    return (
      <section>
        <h1>Settings</h1>
        <p role="alert">
          The hub refused access: sign-in expired or permission revoked. Nothing shown is live.
        </p>
      </section>
    );
  }
  if (state === "reconnecting" && empty && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Settings</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }
  return (
    <section>
      <h1>Settings</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      {saver.notice.status === "error" && <p role="alert">That did not go through: {saver.notice.message}</p>}
      {saver.notice.status === "needs-secret" && (
        <form onSubmit={saveSecret}>
          <p role="alert">
            The hub refused that action. If this hub runs in local-trusted mode, type its local
            approval secret, then try again.
          </p>
          <label>
            Approval secret{" "}
            <input
              type="password"
              value={secret}
              autoComplete="off"
              onChange={(event) => {
                setSecret(event.target.value);
              }}
            />
          </label>{" "}
          <button type="submit">Save secret</button>
        </form>
      )}
      {empty ? (
        <p>{initialDone ? "Nothing set yet." : "Loading…"}</p>
      ) : (
        <>
          <div className={styles.section}>
            <h2>Setting values</h2>
            <ul className={styles.list}>
              {orderedLayers.map((layer) => (
                <LayerCard key={`${settingsKey(layer)}-${layer.version}`} layer={layer} saver={saver} />
              ))}
            </ul>
          </div>
          <div className={styles.section}>
            <h2>Watched repos</h2>
            {orderedRepos.length === 0 ? (
              <p>No repos watched.</p>
            ) : (
              <ul className={styles.list}>
                {orderedRepos.map((repo) => (
                  <RepoRow key={`${repoKey(repo)}-${repo.version}`} repo={repo} saver={saver} />
                ))}
              </ul>
            )}
          </div>
          <div className={styles.section}>
            <h2>Routines</h2>
            {orderedRoutines.length === 0 ? (
              <p>No routines.</p>
            ) : (
              <ul className={styles.list}>
                {orderedRoutines.map((routine) => (
                  <RoutineRow
                    key={`${routineKey(routine)}-${routine.version}`}
                    routine={routine}
                    saver={saver}
                  />
                ))}
              </ul>
            )}
          </div>
          <div className={styles.section}>
            <h2>Suggestions</h2>
            <ul className={styles.list}>
              <SuggestionsToggle fleet={fleet} saver={saver} />
            </ul>
          </div>
        </>
      )}
    </section>
  );
}
