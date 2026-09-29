# 0007. Web UI: React, TypeScript and Vite, embedded in the hub

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

The project may be open-sourced or shared with friends, so the UI stack should
be the one most contributors already know. The hub ships as a single Go binary
([0001](0001-system-architecture.md)), so the UI must build to static files
that the binary embeds, with no separate web server.

Stack Overflow Developer Survey 2025, web frameworks, all respondents:
React 44.7%, Next.js 20.8%, Angular 18.2%, Vue.js 17.6%, Svelte 7.2%; htmx
appears only in write-ins
([survey.stackoverflow.co/2025/technology](https://survey.stackoverflow.co/2025/technology),
read 2026-09-29).

## Options

1. React + TypeScript + Vite, static single-page app.
2. Next.js (React with a Node server).
3. Vue or Svelte.
4. Server-rendered HTML with htmx.

## Decision

React + TypeScript (strict mode) + Vite, built to static files and embedded in
`fleetd` with Go's `embed`.

- API client generated from the `fleet.v1` protobuf contract
  ([0003](0003-transport-grpc.md)); no hand-written request code.
- Live updates over a server stream or SSE.
- Tests: component tests and end-to-end browser tests in CI.
- The clickable mockup under `mockup/` is the reference for the four views
  (Triage, Work, Session, Fleet) and keyboard model.

Not Next.js: it expects a Node server, and the hub is one Go binary. Not htmx:
fewer contributors know it, and the UI is interactive enough (live panes,
switcher, keyboard triage) to benefit from a component model.

## Consequences

- Largest pool of contributors and libraries.
- A Node toolchain is needed to build the UI, but not to run the hub.
- Bundle size needs watching; the UI is an internal tool, not a public site.
