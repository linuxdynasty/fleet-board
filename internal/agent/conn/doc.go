// Package conn is the agent's link to the hub. So far it holds enrollment
// (02g A1, 03 W1): [Enroll] parses the token, checks the hub address, makes
// or reuses the pending Ed25519 key, dials the hub's agent listener over TLS
// pinned to the CA the token names (no client certificate; the handshake
// fails before any byte is sent to a hub without that CA), checks the
// EnrollResponse, and saves the identity, all or nothing.
//
// The state folder (04 Q9, [DefaultDir]) is mode 0700 and every file in it
// 0600; a looser one is refused ([ErrLooseMode]). Enrollment lives in plain
// files ([IdentityFile], [HubAddressFile]) so it survives losing agent.db;
// install_id comes from agent.db (internal/agent/ring). An enrolled folder is never
// replaced ([ErrAlreadyEnrolled]). A lost reply is recovered by enrolling
// again: the kept key and install_id make the hub replay its answer.
//
// The Connect stream, discovery, backoff and certificate renewal come with
// plan row 4's later parts.
package conn
