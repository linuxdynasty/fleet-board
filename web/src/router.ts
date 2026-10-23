// Hand-written hash router (ADR 0020: no router package). The view is
// the #/name part of the address, so links look like #usage. Unknown
// names fall back to the machines view.
import { useCallback, useEffect, useState } from "react";

export type RouteName =
  | "machines"
  | "usage"
  | "session"
  | "triage"
  | "work"
  | "insights"
  | "settings";

export const ROUTES: RouteName[] = [
  "machines",
  "usage",
  "session",
  "triage",
  "work",
  "insights",
  "settings",
];

function routeFromHash(): RouteName {
  const name = window.location.hash.replace(/^#\/?/, "").split("/")[0];
  return (ROUTES as string[]).includes(name ?? "") ? (name as RouteName) : "machines";
}

export function routeHref(name: RouteName): string {
  return `#/${name}`;
}

export function useHashRoute(): RouteName {
  const [route, setRoute] = useState<RouteName>(routeFromHash);
  useEffect(() => {
    const onChange = (): void => {
      setRoute(routeFromHash());
    };
    window.addEventListener("hashchange", onChange);
    return () => {
      window.removeEventListener("hashchange", onChange);
    };
  }, []);
  return route;
}

export function useNavigate(): (name: RouteName) => void {
  return useCallback((name: RouteName) => {
    window.location.hash = `/${name}`;
  }, []);
}
