# 02f. Hub API: authentication and terminal attach

- Status: Draft for review
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md), [02b-hub-api.md](02b-hub-api.md)
- Output: `api/fleet/hub/v1/auth.proto`, `token.proto`, `device.proto`,
  `terminal.proto`, `AuthMode` and `SignInMethod` in `common.proto`
- Decisions: [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md)
  (sign-in, claim URL, API tokens, devices, loopback-only `local_trusted`,
  LOGS scope; addendum 10 to 16 for what this file adds)
- Review: `no-signin-flow`, `local-trusted-attack-surface`,
  `attach-websocket-contract-gaps` in
  [the contract review](../reviews/2026-10-02-contract-review.md)

[02d](02d-hub-authz.md) says what an authenticated caller may do. This file says
how a caller becomes authenticated, how the credential travels on each
transport, and how a terminal attach over WebSocket is opened and closed.

## Modes

| | `local_trusted` | `authenticated` |
|---|---|---|
| Listens on | loopback only (`127.0.0.1`, `::1`); `fleetd` refuses to start on any other address, with no override flag | any address the owner configures, normally the Tailscale or LAN interface, with TLS |
| Who calls | the owner, with no sign-in | users (passkey or GitHub), API tokens, paired devices |
| Principal | fixed `{id "local-owner", name "owner", kind USER, role OWNER}` | by credential, below |
| Sign-in RPCs | `FAILED_PRECONDITION` (nobody signs in) | all of [auth.proto](../../api/fleet/hub/v1/auth.proto) |
| Attach | needs a ticket and passes the Host and Origin checks | same |

`design.md` once said the hub listens only on the Tailscale interface and attach
needs login; both statements are now scoped to `authenticated` mode.
`local_trusted` is a convenience for one person on one machine, and the Host and
Origin checks below are what keep a web page the owner visits from using it.

## Who sends which credential

| Caller | Credential | Carried as | Hub principal |
|---|---|---|---|
| Browser | session cookie | `Cookie: __Host-fleet_session=<256-bit random>` over TLS, `fleet_session` over loopback HTTP; `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`. The hub stores a SHA-256 of it | `{kind USER, id <user>, role <user's role>}` |
| `fleet` CLI, scripts | API token | `Authorization: Bearer fbt_<token id>.<secret>` (`FLEET_TOKEN`) | `{kind API_TOKEN, id "token/<id>", role mapped, on_behalf_of <user>}` |
| Phone | paired device | `Authorization: Fleet-Device <device id>.<unix seconds>.<nonce>.<Ed25519 signature>` over method, path, SHA-256 of the body, timestamp and nonce; the hub refuses a timestamp more than 60 s off or a nonce it saw in that window | `{kind DEVICE, device, id "device/<id>", role mapped, on_behalf_of <user>}` |
| Owner in `local_trusted` | none | nothing | the fixed local owner |
| `fleet-agent`, `fleetd` peers | mTLS client certificate | not this API: `AgentService` only | n/a |

A request with two credential kinds (a cookie and an `Authorization` header) is
`UNAUTHENTICATED` / `CREDENTIALS_INVALID`, so a token script never silently
rides a browser cookie. A signed request covers a stream (`Watch`) once, at
open; the stream ends the moment the credential is revoked. The browser's
cookie covers unary calls, `Watch` and the same-origin WebSocket attach; the
bearer and device carriers cover the same calls for non-browser clients.

`GetServerInfo`, `RedeemInvite`, `BeginPasskeyLogin`, `FinishPasskeyLogin`,
`RequestPairing` and the two GitHub redirect endpoints need no credential and
are rate limited per peer address. Every other RPC needs one. A missing, unknown
or revoked credential is `UNAUTHENTICATED` / `CREDENTIALS_INVALID`; an expired
cookie, token or session is `CREDENTIALS_EXPIRED` ("sign in again").

## Host and Origin checks

Applied to every request on every transport, before any handler, in both modes:

1. **Host.** The `Host` header must be one of the hub's configured public hosts,
   plus in `local_trusted` mode the loopback names (`localhost`, `127.0.0.1`,
   `[::1]`) with the listening port. Anything else is HTTP 403 with
   `HOST_NOT_ALLOWED`. This is what stops DNS rebinding: the attacker's domain
   resolves to `127.0.0.1` but its `Host` is not on the list.
2. **Origin.** A request that carries an `Origin` header must carry exactly an
   allowed origin (scheme, host and port of a configured public origin, or the
   loopback origin in `local_trusted`); otherwise HTTP 403 with
   `ORIGIN_NOT_ALLOWED`. A cookie-authenticated request, and every WebSocket
   upgrade, must carry an `Origin`. A bearer or device request, and in
   `local_trusted` a request from a non-browser process, may omit it.
3. **No CORS.** The hub sends no `Access-Control-Allow-*` headers, so a page on
   another site cannot read a response even if it could send the request. A
   Connect POST must carry `Connect-Protocol-Version` or a gRPC content type
   (not CORS-safelisted, so a cross-site form cannot forge it), and no mutating
   RPC accepts a Connect GET.
4. **Cookies are `SameSite=Strict`**, so the browser does not attach them to a
   cross-site request at all.

An HTTP-level refusal is a Connect error body (`{"code":
"permission_denied", "details": [ErrorDetail]}`) with status 401 for
credentials and 403 for Host and Origin. A WebSocket refused before the upgrade
gets the same.

## First run: the claim URL

In `authenticated` mode a hub with no users has no owner. On start `fleetd`
creates a one-time owner invite exactly as `CreateUserInvite` would (role
`OWNER`, 24 h) and prints the **claim URL** to its log and terminal:
`https://<public origin>/invite#<token>`. The token is in the fragment, so it
reaches no server log or `Referer`. Every start while the hub is unclaimed
replaces the token; `fleetd claim` prints a fresh one. `GetServerInfo`
reports `claim_pending` so the web UI shows "claim this hub".

1. The page reads the fragment and calls `RedeemInvite{invite_token,
   display_name}`. The hub burns the invite, creates the owner (state
   `INVITED`) and sets a **registration session** cookie: it can register a
   passkey or link GitHub for that user and do nothing else, for 15 minutes.
2. The owner registers a passkey (`BeginPasskeyRegistration`,
   `FinishPasskeyRegistration`), or links a GitHub account (below). The first
   credential makes the user `ACTIVE` and upgrades the session to a full one.
3. If nothing is registered in 15 minutes the registration session ends and the
   invite is reissued; `claim_pending` stays true.

Whoever opens the URL first becomes the owner, so the URL is printed only to the
operator's terminal and the owner claims before exposing the hub. A wrong token
is `INVITE_INVALID` with no hint, and redemptions are limited to 10 a minute per
peer address. Other users come in the same way: `CreateUserInvite` (ADMIN)
returns an invite URL, or only a `github_login` for someone who will sign in
with GitHub.

## Passkeys

WebAuthn through a server library; the hub stores credential ids and public
keys, never a password. Relying-party id: the hub's public host. User
verification required, discoverable credential required (so sign-in asks for no
username), attestation `none`, sign counter checked when the authenticator has
one. The options and the credential travel as the W3C JSON forms in
`options_json` and `credential_json` (`auth.proto`), so the browser's own
`parseCreationOptionsFromJSON` and `toJSON` helpers apply and no client code
interprets them. Ceremonies live 5 minutes and are single use.

WebAuthn works only in a secure context: HTTPS, or `localhost`. An
`authenticated` hub reached over plain HTTP at a LAN name offers GitHub sign-in
or needs TLS (Tailscale serves a certificate). `GetServerInfo.sign_in_methods`
lists what is usable.

`FinishPasskeyLogin` sets the session cookie. A bad assertion, an unknown
credential and a disabled user all answer `CREDENTIALS_INVALID`, so the answer
does not reveal which users exist. `DeletePasskey` refuses to remove the last
sign-in method (`LAST_CREDENTIAL`).

## GitHub sign-in

Through the GitHub App the hub already has (ADR 0005), using its **user
authorization** (web flow) and nothing else: the hub asks for no repository
access on behalf of the user, reads `GET /user` once, and discards the user
token; it stores the numeric GitHub user id and the login. Two plain HTTP
endpoints, because it is a browser redirect:

| Endpoint | What it does |
|---|---|
| `GET /auth/github/start?purpose=signin\|link` | Sets a short-lived `__Host-fleet_oauth` cookie holding a random `state` and a PKCE verifier, then redirects to GitHub's authorize URL. `link` needs a full or registration session |
| `GET /auth/github/callback?code&state` | Checks `state` against the cookie, exchanges the code, reads the user, then: `signin` finds the user whose `spec.github_login` equals the login (case-insensitive; pins the numeric id on first use) and sets the session cookie; `link` stores the link on the signed-in user. Redirects to `/` on success or `/signin?error=<REASON>` on failure |

A GitHub account that is not on the allow-list ends at `GITHUB_NOT_ALLOWED`. The
allow-list is `UserSpec.github_login` of the active users, so there is one list
and one place to edit it. After the first sign-in the numeric id is what
matches, so a renamed or recycled login cannot take over a user. Setup adds the
App's client id and secret and the callback URL
`<public origin>/auth/github/callback` to the hub configuration (secret file
mode 0600, like the private key); until then `github_sign_in` is
`SIGN_IN_METHOD_DISABLED`.

## Sessions and sign-out

A session cookie lasts `HubSettings.session_max_age` (30 days) and ends after
`session_idle_timeout` (7 days) without use. `SignOut` ends the caller's own
session and clears the cookie. `ListLoginSessions` and `RevokeLoginSession`
show and end a user's sessions (ADMIN: anyone's). Revoking a session, disabling
a user or revoking a token or device takes effect at once, including on open
streams: `Watch` ends `UNAUTHENTICATED`, an attach closes with `HUB_CLOSED`.
Demoting a user ends its streams with `RESYNC_REQUIRED` / `SCOPES_CHANGED`
([02d](02d-hub-authz.md#scope-per-watch-topic)).

## API tokens

For the CLI and scripts. A user (a signed-in session, or the local owner)
creates one with `CreateApiToken{name, scopes, ttl}`; a token or device cannot
create another. Scopes are required, are a subset of the creator's own, and
include `LOGS` only if the creator holds it and ticks it; anything else is
`SCOPE_NOT_GRANTABLE`. Lifetime defaults to 90 days, at most 365. The secret
`fbt_<id>.<secret>` is shown once in the response and never stored (only a
SHA-256 of it); a retry of the same `request_id` gets `SECRET_ALREADY_DELIVERED`
and the user makes a new one. The `fbt_` prefix lets secret scanners find a
leaked token. The CLI keeps it in `FLEET_TOKEN` or `~/.config/fleet/token`
(mode 0600). `RevokeApiToken` takes effect at once. Because creating the first
token needs a session, `fleetd token create` does the same on the hub host
(it talks to the running hub through its local admin socket, so it needs no
network and no password).

## Devices

Pairing is unchanged ([02d](02d-hub-authz.md#pairing)): `RequestPairing`, a
six-digit code the owner compares, `ApprovePairing` with the granted scopes. An
empty `requested_scopes` means `READ` and `APPROVALS`. `LOGS`, `ADMIN` and
`SETTINGS` are never in the default, and `LOGS` is granted only when the owner
lists it. The device then signs every request with its Ed25519 key (table
above); the private key never leaves the device. Push providers are still open
(question 3 of 02b).

## Terminal attach over WebSocket

A browser cannot stream a request body, so it carries `AttachTerminalRequest`
and `AttachTerminalResponse` (the hub messages, never the agent's `TermUp` and
`TermDown`, which hub messages must not embed) over a WebSocket. A CLI uses the
same RPC over HTTP/2 with its bearer header and needs no ticket.

**Why a ticket.** A WebSocket upgrade carries ambient authority: the cookie, or
nothing at all in `local_trusted` mode. WebSockets are not subject to CORS, so a
page on another site can open one to `localhost` (cross-site WebSocket
hijacking). The Origin and Host checks refuse it, and the ticket is a second
lock: only a caller that passed the same checks on a Connect POST (which an
attacker's page cannot read) holds one.

**Flow.**

1. `CreateAttachTicket{session, read_only}` over the normal channel. The ticket
   is 256-bit random, valid 60 s, bound to the principal, the session, the
   read-only flag and the request's Origin, and shown once.
2. `GET /fleet.hub.v1.FleetService/AttachTerminal` with `Upgrade: websocket`
   and `Sec-WebSocket-Protocol: fleet.attach.v1` (the hub refuses an upgrade
   that does not offer it). Before the upgrade the hub checks Host, Origin
   (required) and the credential (cookie, bearer or device signature; none in
   `local_trusted`); failures are plain HTTP 401 and 403.
3. The first frame must arrive within 10 s and be an `AttachTerminalRequest`
   whose `open` carries the `ticket`. The hub burns the ticket on that frame,
   whether or not the attach then succeeds, and checks principal, session,
   Origin and expiry (`ATTACH_TICKET_INVALID`), then the scope and the machine
   (`SCOPE_MISSING`, `MACHINE_OFFLINE`, `RESOURCE_NOT_FOUND`, ...). `read_only`
   in `AttachOpen` can only tighten the ticket's.
4. The hub sends `AttachReady`, then `output`. The client sends `input`,
   `resize`, `select`, `ack` and finally `close`.

**Framing.** Binary frames only (a text frame closes with 1003). One frame is
exactly one protobuf message. Client-to-hub frames are at most 8 KiB, hub-to-
client at most 66 KiB (a 64 KiB output plus the envelope); more is close 1009.
The hub pings every 30 s and closes after 10 s without a pong. `AttachAck`:
the client sends it at least every 32 KiB written or 200 ms while output flows,
the hub stops reading the agent past 256 KiB unacknowledged, and closes with
`HUB_CLOSED` if no ack arrives for 30 s while output waits (ADR 0016).

**Errors and close codes.** A refused or failed attach is sent as
`AttachClosed{reason ERROR, error ErrorDetail}` first, then the socket closes:

| `AttachClosed` | Close code |
|---|---|
| `DETACHED`, `SESSION_ENDED`, `IDLE_TIMEOUT` | 1000 normal |
| `AGENT_SHUTDOWN`, `HUB_CLOSED` (hub shutdown, revoked credential) | 1001 going away |
| `AGENT_OFFLINE` | 1013 try again later |
| `ERROR` with `ATTACH_TICKET_INVALID`, `CREDENTIALS_*`, `SCOPE_MISSING`, `ROLE_INSUFFICIENT`, `DEVICE_NOT_ACTIVE`, `RESOURCE_NOT_FOUND` | 1008 policy violation |
| `ERROR` with `RATE_LIMITED`, `MACHINE_OFFLINE`, or a retryable `UNAVAILABLE` | 1013 try again later |
| `ERROR` with `INVALID_ARGUMENT` (first frame not `open`, bad message) | 1007 invalid payload |
| `ERROR` with `INTERNAL` | 1011 internal error |

The close reason text is empty; the typed detail is the `AttachClosed` frame. A
client that reconnects asks for a new ticket and opens a new attach (a new view
session); nothing resumes.

## Audit

Sign-in and credential events are audited like any other action (catalogue in
[02b](02b-hub-api.md#audit-actions)): `auth.claim`, `auth.sign_in`,
`auth.sign_in_failed`, `auth.sign_out`, `passkey.add`, `passkey.delete`,
`user.invite`, `user.update`, `token.create`, `token.revoke`, `auth.session_revoke`,
`attach.open`, `attach.close`. Secrets never appear in `detail`.
