// Package conn is the agent's link to the hub. So far it holds enrollment
// (02g A1, 03 W1): [Enroll] parses the token, checks the hub address, makes
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
// The Connect stream, discovery, reconnect backoff and certificate renewal
// come with plan row 4's later parts.
package conn
