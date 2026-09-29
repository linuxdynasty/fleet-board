# 0005. GitHub integration through a GitHub App

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

GitHub is the first and main integration: the issue queue, claim comments, PR
state, checks and merges. The hub must decide what to dispatch and verify that
a goal's `DONE` matches a real merge. Pushing and merging are done by goals on
the worker machines, through each machine's own `gh` login; the hub never needs
to write code.

## Options

1. A GitHub App installed on the watched repos.
2. A personal access token (fine-grained or classic) on the hub.
3. Reusing a `gh` login from the hub machine.

## Decision

A GitHub App, with a fine-grained token as the documented fallback for users
who cannot create an App.

- **Permissions (read-only):** metadata, issues, pull requests, checks, commit
  statuses. No contents write, no admin. Exact permission names to be confirmed
  against GitHub's App permission list when the App manifest is written.
- **Credential:** the App's private key is a GitHub credential of the user's
  own, stored on the hub as a file readable only by the hub user. It is not a
  harness credential; the no-credential rule for providers is unaffected.
- **Sync:** polling with conditional requests (ETags) and GraphQL batches for
  PR state, check rollups and reviews. Webhooks need a public URL, which a hub
  on a laptop usually lacks, so they are optional later (through a tunnel).
  Confirm GitHub's current rate-limit rules for App installation tokens and
  conditional requests before setting poll intervals.
- **What the hub reads:** issues (author, labels, state, claim comments), PRs
  (state, mergeable, check rollup, reviews, linked issues), merges and closes.
- **Trusted-issues gate:** auto-dispatch only takes issues opened by, or
  labelled `agent-ready` by, an allow-listed user. Everything else needs a
  manual dispatch.
- **Verification:** a goal is freed only when the hub sees the linked PR
  merged or the issue closed.
- **What the hub never does:** push, merge, comment or edit on GitHub. Those
  happen inside goals, on workers, under the worker's own `gh` identity.

## Consequences

- The hub's GitHub access is narrow and revocable per repo from GitHub's UI.
- Setup has one more step (create and install the App); `fleetd setup github`
  should generate the App manifest to make it one click.
- Freshness depends on the poll interval until webhooks are added.

## Open questions

- Poll intervals per object type once rate limits are confirmed.
- Whether the hub should ever post (for example a "dispatched to X" comment),
  which would need issues write.
