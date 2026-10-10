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
// it tries the cached, then the configured hub addresses over mTLS pinned
// to the enrolled CA, sends Hello, takes HelloAck's epoch and seq floor
// (02i), sends a Snapshot built for that stream and the results the hub has
// not acknowledged, and serves the stream until it ends. Then it backs off
// ([Backoff]), or reconnects at once after a wake or a network change that
// touched the connection (ADR 0029). An HTTP/2 keepalive ([Keepalive])
// catches a silent hub. While connected, the same goroutine sends an A5
// Heartbeat every HelloAck.heartbeat_interval (1 to 15 s, else 15 s;
// [LoopOptions].Heartbeat fills what later rows own). A stop says Bye{SHUTDOWN} and cuts the connection
// if the hub has not closed it 2 s later. It ends on its own ([EndedError]) only for
// Bye{REVOKED}, Bye{UNKNOWN_MACHINE}, another fleet's HelloAck or an
// expired certificate. The first three are written whole to [EndedFile]
// before Run returns; `fleet-agent run` calls [CheckEnded] before any
// network call and refuses while the file names its machine, and Enroll
// removes it with a new identity. [Loop.Status] is its state in memory.
//
// Discovery (mDNS) and certificate renewal come with plan row 4's later
// parts.
package conn
