package inventory

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxNameBytes caps a session or window name and a pane's current command
// (02e "Labels, names, window names": 256 bytes).
const maxNameBytes = 256

// cleanText makes the developer's text safe to report (02e): invalid
// UTF-8 and every control character (C0, DEL, C1; names are one line, so
// newline and tab too) are dropped, then the text is cut to max bytes on a
// rune boundary. It is never logged either way.
func cleanText(s string, limit int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Tag shapes. A tag is data anyone who can run tmux can set (AGENTS.md):
// only a value with its id's shape is reported, anything else is dropped
// whole (never cut or repaired). The hub trusts a session_id only when its
// own table minted it (row-7 ruling fork 7).
var (
	// A hub-assigned id (SessionId, GoalId; 02e "Id format"), and the
	// agent-minted "obs-" goal ids.
	idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// A PrincipalId: "local-owner", "device/<id>", "token/<id>",
	// "local:<os user>" or a hub-assigned id.
	principalRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)
	// A PaneKey's canonical string: server/server_start_id/pane_id.
	paneKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}/[a-z0-9-]{1,64}/%[0-9]{1,10}$`)
)

func validID(s string) bool        { return idRE.MatchString(s) }
func validPrincipal(s string) bool { return principalRE.MatchString(s) }
func validPaneKey(s string) bool   { return paneKeyRE.MatchString(s) }

// The FLEET_* names E1 reports the @fleet_* user options under (row-7
// ruling fork 7; snapshot.proto TmuxPane.fleet_tags).
const (
	tagSessionID = "FLEET_SESSION_ID"
	tagOwner     = "FLEET_OWNER"
	tagPaneKey   = "FLEET_PANE_KEY"
	tagGoalID    = "FLEET_GOAL_ID"
)

// attachViewMark is the @fleet_internal value row 26 sets on a grouped
// attach-view session (row-7 ruling fork 6).
const attachViewMark = "attach_view"
