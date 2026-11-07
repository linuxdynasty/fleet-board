package statedb

// syncCanonicalSQL is migration 33 (row-23 review P1-2): rewrite legacy
// mirror keys and kinds to the short form. Rows the syncer stored before
// SyncKey normalization carry the full-form kind
// ("<integration>/WORK_ITEM_KIND_ISSUE/<project>/<key>"), which new
// lookups no longer find, so the next sync INSERTs a duplicate beside a
// stale READY phantom. The rewrite strips the WORK_ITEM_KIND_ enum
// prefix from the key's kind segment and from the kind column, exactly
// like NormalizeWorkKey: the kind is the second segment (the project
// behind it may hold slashes, so a flat REPLACE would also rewrite the
// project path). Rows already short match no LIKE arm and stay
// untouched. The degenerate bare-prefix kind is left alone: stripping it
// would empty the column against its CHECK.
const syncCanonicalSQL = `UPDATE work_items
	SET key = CASE
		WHEN instr(key, '/') = 0 THEN key
		WHEN instr(substr(key, instr(key, '/') + 1), '/') = 0 THEN key
		ELSE substr(key, 1, instr(key, '/')) ||
			CASE WHEN substr(substr(key, instr(key, '/') + 1), 1,
					instr(substr(key, instr(key, '/') + 1), '/') - 1) LIKE 'WORK_ITEM_KIND\_%' ESCAPE '\'
				AND substr(substr(key, instr(key, '/') + 1), 1,
					instr(substr(key, instr(key, '/') + 1), '/') - 1) <> 'WORK_ITEM_KIND_'
				THEN substr(substr(substr(key, instr(key, '/') + 1), 1,
					instr(substr(key, instr(key, '/') + 1), '/') - 1), 16)
				ELSE substr(substr(key, instr(key, '/') + 1), 1,
					instr(substr(key, instr(key, '/') + 1), '/') - 1)
			END ||
			substr(substr(key, instr(key, '/') + 1),
				instr(substr(key, instr(key, '/') + 1), '/'))
		END,
		kind = REPLACE(kind, 'WORK_ITEM_KIND_', '')
	WHERE key LIKE '%/WORK_ITEM_KIND\_%' ESCAPE '\'
		OR (kind LIKE 'WORK_ITEM_KIND\_%' ESCAPE '\' AND kind <> 'WORK_ITEM_KIND_');`
