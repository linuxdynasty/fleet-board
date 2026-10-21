package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// This file is the opaque Watch resume token (02c): the position
// (hub_instance_id, hub_seq) and the subscriptions it covers (topic plus a
// hash of the filter). A resumed subscription is one the token covers with
// the same topic and filter; anything else restarts.

// watchTokenSub is one covered subscription.
type watchTokenSub struct {
	Topic  hubv1.WatchTopic `json:"t"`
	Filter string           `json:"f"` // filterHash of the subscription filter
}

// watchToken is the decoded form.
type watchToken struct {
	V    int             `json:"v"`
	IID  string          `json:"iid"`
	Seq  uint64          `json:"s"`
	Subs []watchTokenSub `json:"subs"`
}

// filterHash names a subscription filter in the token: the raw filter text
// hashed, so a reworded filter restarts instead of resuming.
func filterHash(filter string) string {
	sum := sha256.Sum256([]byte(filter))
	return hex.EncodeToString(sum[:])
}

// mintWatchToken seals the position a message leaves the client at.
func mintWatchToken(instanceID string, seq uint64, subs []watchTokenSub) string {
	raw, err := json.Marshal(watchToken{V: 1, IID: instanceID, Seq: seq, Subs: subs})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseWatchToken opens a client-supplied token. Anything foreign is an
// error the handler reports as RESUME_TOKEN_INVALID.
func parseWatchToken(token string) (watchToken, error) {
	if len(token) > maxFilterBytes {
		return watchToken{}, fmt.Errorf("api: resume token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return watchToken{}, fmt.Errorf("api: resume token: %w", err)
	}
	var t watchToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return watchToken{}, fmt.Errorf("api: resume token: %w", err)
	}
	if t.V != 1 || t.IID == "" {
		return watchToken{}, fmt.Errorf("api: resume token has no position")
	}
	return t, nil
}

// covers reports whether the token resumes this subscription: same topic
// and same filter hash (02c changing-the-subscription table).
func (t watchToken) covers(topic hubv1.WatchTopic, hash string) bool {
	for _, s := range t.Subs {
		if s.Topic == topic && s.Filter == hash {
			return true
		}
	}
	return false
}

// hasTopic reports whether the token ever covered the topic, under any
// filter: a changed filter restarts the topic, but a lost scope still
// resyncs it as SCOPES_CHANGED.
func (t watchToken) hasTopic(topic hubv1.WatchTopic) bool {
	for _, s := range t.Subs {
		if s.Topic == topic {
			return true
		}
	}
	return false
}
