# 0012. Integration model: work trackers and code hosts

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Amends: [0005](0005-github-app.md) (GitHub becomes the first integration, not the only one), [0011](0011-contract-amendments.md)

## Context

GitHub issues and pull requests are the first source of work, but the owner
wants fleet-board to support other systems later: GitLab (issues and merge
requests, nested groups, self-hosted instances), Jira (tickets only, with the
code on a separate host), and others such as Bitbucket and Linear.

The contract merged in step 2 assumed GitHub: `RepoKey` was an `owner/repo`
string with no host and no nesting, `IssueRef` was a repo plus a number,
`GoalSpec` tied the work item and the code to one repo, and the agent reported
`gh` status only. GitLab numbers issues and merge requests separately, and Jira
keys (`PROJ-123`) are strings in a different system from the code.

## Decision

**Two roles, one model.** An integration plays one or both roles:

| Role | Does | Examples |
|---|---|---|
| Work tracker | lists work items, claims, labels for the trusted-issues gate | GitHub Issues, GitLab Issues, Jira, Linear |
| Code host | repos, change requests, checks, reviews, merge state | GitHub, GitLab, Bitbucket |

A goal pairs a **work item** from any tracker with a **repo** on any code host
(a Jira ticket driving a GitHub PR is valid).

**Provider-neutral identifiers** (`api/fleet/v1/common.proto`):

- `RepoRef{host, path, code_host}`: path may be nested
  (`group/subgroup/repo`); SSH and HTTPS remotes normalize to the same value.
- `WorkItemRef{integration, provider, kind, project, key, url, display}`:
  `kind` is issue, ticket or change request; `key` is a string (`7398`,
  `12`, `PROJ-123`); `display` is the short form used in the UI and window
  names (`eshu#7398`, `eshu!12`, `PROJ-123`).
- `GoalSpec` carries `work_item`, `repo` and an optional `change_request`
  separately. The old `issue` field is reserved.
- The agent reports `ForgeCliStatus` for each code-host CLI it finds (`gh`,
  `glab`), with the provider's own permission name and a normalized
  `push_access`. The old `gh` field and `viewer_permission` are reserved.
- The default window name is `{harness} {work}`.

**Provider adapters** (`api/fleet/v1/integrations.proto`): a read-only
`IntegrationProvider` service (`Describe`, `ListWorkItems`, `GetWorkItem`,
`ListChangeRequests`, `GetChangeRequest`, `Watch`) over a normalized model
(`WorkItem`, `ChangeRequest`, `CheckSummary`, `Review`, `Claim`). Each adapter
declares `IntegrationCapabilities`: roles, work-item kinds, claim mechanisms
(comment, assignee, label, workflow status), sync modes (conditional polling,
polling, webhook), and whether it reports checks, reviews, merges, cross-links
and labels. The hub never asks an adapter for something it doesn't declare.

- **v1:** adapters are compiled into `fleetd` and implement the interface
  in-process. GitHub is the first, through the GitHub App of 0005.
- **Later:** the same service can be served by an out-of-process plugin over
  gRPC, so new providers can ship without changing the hub.
- **Read-only stays the rule** (0005): the hub never writes to a provider.
  Claims, pushes and merges happen inside goals on workers with the
  developer's own tools (`gh`, `glab`, Jira CLI or MCP), as they would by hand.

**Consent names stay generic.** `pr-open` means opening a change request on any
code host; adapters render it in the provider's and harness's terms.

## Consequences

- Adding GitLab or Jira means writing an adapter and its capability
  declaration; the agent contract, scheduler and UI don't change.
- Work-item display, claims and verification ("is it merged?") go through the
  normalized model, so the goal lifecycle works the same on every provider.
- Providers differ in what they can do (Jira has no merges, some have no
  labels); capabilities make that explicit instead of failing at run time.
- Changing field types in an accepted contract is acceptable here because no
  implementation exists yet; replaced fields and names are reserved anyway.

## Open questions

- Plugin transport and trust for out-of-process adapters (local socket,
  signing) when the first external adapter is written.
- How a Jira ticket names its target repo(s): a custom field, a component
  mapping, or owner configuration per project.
