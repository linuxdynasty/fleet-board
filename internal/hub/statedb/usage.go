package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// usageReadingsSQL is migration 22 (plan row 12, the usage reads): the
// append-only reading history behind ListUsageReadings (03b
// "Persistence", 03f). One row per stored reading: the account, the
// hub-adjusted read_time readings order by across machines (02i
// "Clocks"), the hub's received_time (the tie-break for equal read
// times, and the order for a clock_untrusted machine), the agent's own
// read time for display, the source and parse short names, and the
// buckets and spend as JSON. The key refines 03f's (account_id,
// read_time) with received_time so two readings at the same millisecond
// both land.
const usageReadingsSQL = `CREATE TABLE usage_readings(
	account_id TEXT NOT NULL CHECK (account_id <> ''),
	read_ms INTEGER NOT NULL,
	received_ms INTEGER NOT NULL,
	agent_read_ms INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL CHECK (source IN ('PROBE', 'PASSIVE', 'IDENTITY_PROBE')),
	parse TEXT NOT NULL DEFAULT 'OK' CHECK (parse IN ('UNSPECIFIED', 'OK', 'PARTIAL', 'FAILED')),
	buckets_json TEXT NOT NULL DEFAULT '[]',
	spend_json TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (account_id, read_ms, received_ms)
) STRICT;`

// usageReadingsReadMsSQL is migration 27 (plan row 13): the read_ms
// index behind the time-bounded reading scans. The hourly rollup and
// GetInsights recompute only recent windows, so they filter by read_ms
// instead of loading the whole retained history (the primary key leads
// with account_id and cannot serve that bound).
const usageReadingsReadMsSQL = `CREATE INDEX usage_readings_read_ms ON usage_readings(read_ms);`

// ErrUsageInvalid: a usage reading with no account, no read or received
// time, an unknown source or parse, or buckets/spend that are not JSON.
var ErrUsageInvalid = errors.New("statedb: the usage reading is invalid")

// usageSources is the 02c UsageSource short-name set the store accepts.
var usageSources = map[string]bool{
	"PROBE": true, "PASSIVE": true, "IDENTITY_PROBE": true,
}

// usageParses is the ParseOutcome short-name set the store accepts.
var usageParses = map[string]bool{
	"UNSPECIFIED": true, "OK": true, "PARTIAL": true, "FAILED": true,
}

// UsageSeed is one stored reading for tests and the usage raise path:
// the account, the hub-adjusted read time, the hub receive time, the
// agent's own read time, and the source, parse, buckets and spend.
// Buckets is a JSON array of LimitBucket protojson and spend one
// SpendReading protojson (empty when unset).
type UsageSeed struct {
	AccountID     string
	ReadTime      time.Time
	ReceivedTime  time.Time
	AgentReadTime time.Time
	Source        string
	Parse         string
	BucketsJSON   []byte
	SpendJSON     []byte
}

// UsageReadingStored is one usage_readings row.
type UsageReadingStored struct {
	AccountID   string
	ReadMs      int64
	ReceivedMs  int64
	AgentReadMs int64
	Source      string
	Parse       string
	BucketsJSON []byte
	SpendJSON   []byte
}

// InsertUsageReading appends one reading in one writer transaction. It
// writes no audit row: readings are hub-observed history, like the raise
// path's triage rows.
func InsertUsageReading(ctx context.Context, s *store.Store, seed UsageSeed) (UsageReadingStored, error) {
	if seed.AccountID == "" || seed.ReadTime.IsZero() || seed.ReceivedTime.IsZero() {
		return UsageReadingStored{}, fmt.Errorf("%w: a reading needs an account, a read time and a received time", ErrUsageInvalid)
	}
	if !usageSources[seed.Source] {
		return UsageReadingStored{}, fmt.Errorf("%w: unknown usage source %q", ErrUsageInvalid, seed.Source)
	}
	parse := seed.Parse
	if parse == "" {
		parse = "OK"
	}
	if !usageParses[parse] {
		return UsageReadingStored{}, fmt.Errorf("%w: unknown parse outcome %q", ErrUsageInvalid, seed.Parse)
	}
	buckets := seed.BucketsJSON
	if len(buckets) == 0 {
		buckets = []byte("[]")
	}
	if !json.Valid(buckets) {
		return UsageReadingStored{}, fmt.Errorf("%w: the buckets are not valid JSON", ErrUsageInvalid)
	}
	if len(seed.SpendJSON) > 0 && !json.Valid(seed.SpendJSON) {
		return UsageReadingStored{}, fmt.Errorf("%w: the spend is not valid JSON", ErrUsageInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	out := UsageReadingStored{
		AccountID: seed.AccountID, ReadMs: seed.ReadTime.UnixMilli(),
		ReceivedMs: seed.ReceivedTime.UnixMilli(), AgentReadMs: seed.AgentReadTime.UnixMilli(),
		Source: seed.Source, Parse: parse, BucketsJSON: buckets, SpendJSON: seed.SpendJSON,
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(jctx, `INSERT INTO usage_readings(account_id, read_ms, received_ms,
			agent_read_ms, source, parse, buckets_json, spend_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			out.AccountID, out.ReadMs, out.ReceivedMs, out.AgentReadMs,
			out.Source, out.Parse, string(out.BucketsJSON), string(out.SpendJSON)); err != nil {
			return fmt.Errorf("statedb: insert usage reading: %w", err)
		}
		return nil
	})
	if err != nil {
		return UsageReadingStored{}, err
	}
	return out, nil
}
