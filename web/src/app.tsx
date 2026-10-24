import type { JSX } from "react";
import { useEffect } from "react";
import { ROUTES, routeHref, useHashRoute, useNavigate, type RouteName } from "./router.ts";
import { TRIAGE_MOVE_EVENT, isTypingTarget, keyAction, type TriageMoveDetail } from "./keys.ts";
import { InsightsView } from "./views/insights.tsx";
import { MachinesView } from "./views/machines.tsx";
import { SettingsView } from "./views/settings.tsx";
import { UsageView } from "./views/usage.tsx";
import { SessionView } from "./views/session.tsx";
import { TriageView } from "./views/triage.tsx";
import { WorkView } from "./views/work.tsx";
import styles from "./app.module.css";

const LABELS: Record<RouteName, string> = {
  machines: "Machines",
  usage: "Usage",
  session: "Session",
  triage: "Triage",
  work: "Work",
  insights: "Insights",
  settings: "Settings",
};

function PlannedView({ name }: { name: RouteName }): JSX.Element {
  return (
    <section>
      <h1>{LABELS[name]}</h1>
      <p>Planned: this view arrives in its row-14 slice.</p>
    </section>
  );
}

function useViewKeys(route: RouteName, navigate: (name: RouteName) => void): void {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      if (isTypingTarget(event.target)) {
        return;
      }
      const action = keyAction(event.key, route);
      if (action === null) {
        return;
      }
      if (action.kind === "view") {
        navigate(action.route);
      } else if (action.kind === "next-session") {
        navigate("session");
      } else {
        window.dispatchEvent(
          new CustomEvent<TriageMoveDetail>(TRIAGE_MOVE_EVENT, { detail: { delta: action.delta } }),
        );
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [route, navigate]);
}

export function App(): JSX.Element {
  const route = useHashRoute();
  useViewKeys(route, useNavigate());
  return (
    <div className={styles.page}>
      <nav className={styles.nav} aria-label="Views">
        {ROUTES.map((name) => (
          <a
            key={name}
            href={routeHref(name)}
            aria-current={route === name ? "page" : undefined}
            className={route === name ? styles.active : undefined}
          >
            {LABELS[name]}
          </a>
        ))}
      </nav>
      <main className={styles.main}>
        {route === "machines" ? (
          <MachinesView />
        ) : route === "usage" ? (
          <UsageView />
        ) : route === "session" ? (
          <SessionView />
        ) : route === "triage" ? (
          <TriageView />
        ) : route === "work" ? (
          <WorkView />
        ) : route === "insights" ? (
          <InsightsView />
        ) : route === "settings" ? (
          <SettingsView />
        ) : (
          <PlannedView name={route} />
        )}
      </main>
    </div>
  );
}
