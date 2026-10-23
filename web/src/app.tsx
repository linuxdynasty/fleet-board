import type { JSX } from "react";
import { ROUTES, routeHref, useHashRoute, type RouteName } from "./router.ts";
import { MachinesView } from "./views/machines.tsx";
import { UsageView } from "./views/usage.tsx";
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

export function App(): JSX.Element {
  const route = useHashRoute();
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
        ) : (
          <PlannedView name={route} />
        )}
      </main>
    </div>
  );
}
