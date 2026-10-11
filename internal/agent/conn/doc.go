// Package conn is the agent's link to the hub: enrollment and the
// connection loop. Enrollment (02g A1, 03 W1): [Enroll] parses the token, checks the hub address, makes
// or reuses the pending Ed25519 key, dials the hub's agent listener over TLS
// pinned to the CA the token names (no client certificate; any failed
// handshake is [ErrHubUntrusted], before any byte is sent), retries a busy
// hub or a lost connection a bounded number of times with the same request
// ([Retry]), checks the EnrollResponse, and saves the identity, all or
// nothing.
//
// The state folder (04 Q9, [DefaultDir]) is mode 0700 and every file in it
// 0600; a looser one, another owner's, or a symlink is refused
// ([ErrLooseMode]). Enrollment lives in plain files ([IdentityFile],
// [HubAddressFile]) so it survives losing agent.db; install_id comes from
// agent.db (internal/agent/ring) and survives re-enrollment. Until re-enroll
// exists, an enrolled folder is never replaced ([ErrAlreadyEnrolled]). A lost
// reply is recovered by enrolling again: the kept key and install_id make the
// hub replay its answer.
//
// [Loop] keeps an enrolled machine connected (03 W1 "State machine", 03n W2):
// it tries the cached address, then the answers of one mDNS browse
// ([Discover], best effort: ADR 0028) as they arrive, then the configured
// addresses ([HubAddressesFile], the hub's latest list, else the
// identity's) over mTLS pinned to the enrolled CA, sends Hello, takes HelloAck's epoch and seq floor
// (02i), sends a Snapshot built for that stream and the results the hub has
// not acknowledged, and serves the stream until it ends. Then it backs off
// ([Backoff]), or reconnects at once after a wake or a network change that
// touched the connection (ADR 0029). An HTTP/2 keepalive ([Keepalive])
// catches a silent hub. While connected, the same goroutine sends an A5
// Heartbeat every HelloAck.heartbeat_interval (1 to 15 s, else 15 s;
// [LoopOptions].Heartbeat fills what later rows own). With a
// [LoopOptions].Emitter, the stream attaches an event cursor at the HelloAck
// floor once the snapshot and its results are out, sends the ring's events
// in seq order at 200 a second at most (bursts of 64), and trims the ring
// on EventAck only when HelloAck listed event.ack. A stop says Bye{SHUTDOWN} and cuts the connection
// if the hub has not closed it 2 s later; once Connected, the stream's
// agent.db work runs on the stream's context, so a stop never fails it
// before that goodbye. It ends on its own ([EndedError]) only for
// Bye{REVOKED}, Bye{UNKNOWN_MACHINE}, another fleet's HelloAck or an
// expired certificate. The first three are written whole to [EndedFile]
// before Run returns; `fleet-agent run` calls [CheckEnded] before any
// network call and refuses while the file names its machine, and Enroll
// removes it with a new identity. [Loop.Status] is its state in memory and
// [Loop.StatusChanged] signals a change.
//
// The seam for commands and settings (plan row 6): [LoopOptions].Command
// and Settings take what the hub sends, with the stream's epoch, without
// blocking; [Loop.Send] queues acks, progress, logged results and
// SettingsAck for the Connected stream's one sender; OnConnected and
// OnDisconnected bracket each Connected stream; [Loop.HubClock] is the
// strict hub clock deadlines are checked on (02i "Clocks"); and
// [LoopOptions].Capabilities fills Hello.
//
// Enroll with no hub address browses the same way and sends its request
// only to the first answer whose handshake proves it holds the token's CA
// ([ErrNoHubFound] when none does). Every mDNS answer is untrusted: only a
// candidate until the pin passes.
//
// While connected the loop renews its client certificate (02g "Certificate
// renewal (A4)"): at two thirds of the life of the pair that ends last, checked
// when a stream connects and every [LoopOptions].RenewCheck, one renewal
// at a time makes a new key in [RenewKeyFile], calls RenewCert on the
// stream's connection, checks the answer, writes the pair to [CertFile]
// and swaps it in for the next handshake; identity.json is never
// rewritten. A handshake presents the pair that ends later among those
// inside their life by the agent's clock (the other one after an attempt
// that presented it failed); the renewal point, the expiry check and the
// expiry gauge follow the pair that ends last on hub time; a failed
// renewal is retried at the next check and never ends the loop.
package conn
