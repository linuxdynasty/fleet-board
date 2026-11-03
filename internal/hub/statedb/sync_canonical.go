package statedb

// syncCanonicalSQL is migration 33 (row-23 review P1-2): rewrite legacy
// mirror keys and kinds to the short form. Rows the syncer stored before
// SyncKey normalization carry the full-form kind
// ("<integration>/WORK_ITEM_KIND_ISSUE/<project>/<key>"), which new
// lookups no longer find, so the next sync INSERTs a duplicate beside a
// stale READY phantom. The rewrite strips the WORK_ITEM_KIND_ enum
// prefix from the key's kind segment and from the kind column, exactly
// like normalizeWorkKind; rows already short match no LIKE arm and stay
// untouched. The degenerate bare-prefix kind is left alone: stripping it
// would empty the column against its CHECK.
const syncCanonicalSQL = `UPDATE work_items
	SET key = REPLACE(key, '/WORK_ITEM_KIND_', '/'),
		kind = REPLACE(kind, 'WORK_ITEM_KIND_', '')
	WHERE key LIKE '%/WORK_ITEM_KIND\_%' ESCAPE '\'
		OR (kind LIKE 'WORK_ITEM_KIND\_%' ESCAPE '\' AND kind <> 'WORK_ITEM_KIND_');`
