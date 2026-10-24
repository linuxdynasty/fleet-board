import type { JSX } from "react";
import { create } from "@bufbuild/protobuf";
import { FieldMaskSchema } from "@bufbuild/protobuf/wkt";
import {
  RepoSchema,
  RepoSpecSchema,
  UpdateRepoRequestSchema,
  type Repo,
} from "../../../gen/ts/fleet/hub/v1/repo_pb.ts";
import { fleetClient } from "../client.ts";
import { providerForHost } from "./settings-helpers.ts";
import type { Saver } from "./settings-save.ts";
import styles from "./settings.module.css";

// RepoRow shows one watched repo with its host, provider and pause state.
// Pause and resume go through UpdateRepo with spec.enabled only; there is no
// delete or create control in this slice.
export function RepoRow({ repo, saver }: { repo: Repo; saver: Saver }): JSX.Element {
  const host = repo.spec?.repo?.host ?? "";
  const path = repo.spec?.repo?.path ?? "";
  const watching = repo.spec?.enabled === true;
  return (
    <li className={styles.card}>
      <p className={styles.head}>
        <span className={styles.chip}>{providerForHost(host)}</span>
        {host === "" ? "unknown address" : `${host}/${path}`}
      </p>
      <p className={styles.detail}>{watching ? "Watching: goals can start here." : "Paused: no new goals here."}</p>
      <div className={styles.actions}>
        <button
          type="button"
          disabled={saver.notice.status === "busy"}
          onClick={() => {
            void saver.run(() =>
              fleetClient().updateRepo(
                create(UpdateRepoRequestSchema, {
                  requestId: crypto.randomUUID(),
                  repo: create(RepoSchema, {
                    id: repo.id,
                    spec: create(RepoSpecSchema, { enabled: !watching }),
                  }),
                  updateMask: create(FieldMaskSchema, { paths: ["spec.enabled"] }),
                  expectedVersion: repo.version,
                }),
              ),
            );
          }}
        >
          {watching ? "Pause" : "Resume"}
        </button>
      </div>
      <details>
        <summary>Details</summary>
        <p className={styles.detail}>UpdateRepo{"{"}spec.enabled{"}"}. Running goals keep going.</p>
      </details>
    </li>
  );
}
