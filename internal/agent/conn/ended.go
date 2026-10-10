package conn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// EndedFile records the end the connection loop stopped on when only the
// owner can fix it: revoked, unknown machine or another fleet (03 W1). It
// is written whole before [Loop.Run] returns, so a restarted `fleet-agent
// run` refuses before any network call instead of asking the hub again
// ([CheckEnded]). Enroll removes it when it saves a new identity; the owner
// may remove it by hand to try again. An expired certificate is read from
// the certificate itself and is never recorded here.
const EndedFile = "ended.json"

// endedVersion is the layout of EndedFile.
const endedVersion = 1

// maxEndedPeer caps the hub address read back from EndedFile for a message.
const maxEndedPeer = 270

// ended is the content of EndedFile.
type ended struct {
	Version   int       `json:"version"`
	End       End       `json:"end"`
	MachineID string    `json:"machine_id"`
	Peer      string    `json:"peer"`
	Time      time.Time `json:"time"`
}

// recorded reports whether the end is one EndedFile keeps.
func recorded(e End) bool {
	return e == EndRevoked || e == EndUnknownMachine || e == EndFleetMismatch
}

// saveEnded writes EndedFile whole (temp file, fsync, rename, folder fsync).
func saveEnded(dir string, rec ended) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("conn: encode %s: %w", EndedFile, err)
	}
	return writePrivate(dir, EndedFile, data, true)
}

// saveEnd records an end that needs the owner before Run returns it. A
// failed write is logged and the loop still ends: this run stops either way.
func (l *Loop) saveEnd(ctx context.Context, e End, peer string) {
	if !recorded(e) {
		return
	}
	rec := ended{Version: endedVersion, End: e, MachineID: l.opts.Identity.MachineID, Peer: peer, Time: time.Now().UTC()}
	if err := saveEnded(l.opts.Dir, rec); err != nil {
		l.log.ErrorContext(ctx, "connect.failed", "peer", peer, "step", "save", "err_kind", errKind(err))
	}
}

// CheckEnded reads EndedFile; the caller runs it at the start, before any
// network call (`fleet-agent run`, row 4 PR 3-iii). A record for id's
// machine is an [*EndedError] with Recorded set, whose message names the
// hub and the owner's fix: the caller must not reconnect. A record for another machine is a leftover of an earlier
// enrollment: it is removed and removed is true. No file is (false, nil).
// A damaged file is an error naming it, as for a damaged identity.json;
// removing it lets run try again.
func CheckEnded(dir string, id Identity) (removed bool, err error) {
	path := filepath.Join(dir, EndedFile)
	data, err := readPrivate(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rec ended
	if err := dec.Decode(&rec); err != nil || rec.Version != endedVersion || !recorded(rec.End) || rec.MachineID == "" {
		return false, fmt.Errorf("conn: %s is damaged or from another version; remove it to try again", path)
	}
	if rec.MachineID != id.MachineID {
		if err := removePrivate(dir, EndedFile); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, &EndedError{End: rec.End, Recorded: true, Peer: clean(rec.Peer, maxEndedPeer), Time: rec.Time, Dir: dir}
}

// recordedMessage is the refusal of a run that finds its own end in
// EndedFile: what happened, where, and the owner's fix.
func (e *EndedError) recordedMessage() string {
	var what, fix string
	switch e.End {
	case EndFleetMismatch:
		what = "the hub at " + e.Peer + " belongs to another fleet"
		fix = "check the hub address, then re-enroll or remove `ended.json` to try again"
	case EndUnknownMachine:
		what = "the hub at " + e.Peer + " does not know this machine"
		fix = "to enroll it again, remove `identity.json` (keep `agent.db`), then `fleet-agent enroll --token <new>`"
	default:
		what = "the hub at " + e.Peer + " revoked this machine"
		fix = "to enroll it again, remove `identity.json` (keep `agent.db`), then `fleet-agent enroll --token <new>`"
	}
	return fmt.Sprintf("conn: %s (%s, recorded in %s at %s), so run does not reconnect; %s",
		what, e.End, filepath.Join(e.Dir, EndedFile), e.Time.Format(time.RFC3339), fix)
}
