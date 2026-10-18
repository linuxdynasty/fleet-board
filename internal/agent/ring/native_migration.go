package ring

// nativeJournalSQL is the dormant machine-side native ownership and effect
// journal (02i "What agent.db keeps", native execution spec, #266). The
// command log has no foreign key here: pruning it must not erase ownership or
// uncertain effects. Rows are retained until a measured retention policy is
// implemented with the real runtime producer.
const nativeJournalSQL = `CREATE TABLE native_sessions(
	session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 32),
	machine_id TEXT NOT NULL CHECK (length(machine_id) BETWEEN 1 AND 64),
	install_id TEXT NOT NULL CHECK (length(install_id) BETWEEN 1 AND 64),
	harness TEXT NOT NULL CHECK (length(harness) BETWEEN 1 AND 64),
	interface TEXT NOT NULL CHECK (length(interface) BETWEEN 1 AND 64),
	account_ref TEXT NOT NULL CHECK (length(account_ref) BETWEEN 1 AND 128),
	profile_ref TEXT NOT NULL CHECK (length(profile_ref) BETWEEN 1 AND 128),
	workdir_ref TEXT NOT NULL CHECK (length(workdir_ref) BETWEEN 1 AND 128),
	model TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 128),
	effort TEXT NOT NULL CHECK (length(effort) BETWEEN 1 AND 64),
	qualification_ref TEXT NOT NULL CHECK (length(qualification_ref) BETWEEN 1 AND 128),
	controller_id TEXT NOT NULL CHECK (length(controller_id) BETWEEN 1 AND 128),
	generation INTEGER NOT NULL CHECK (generation = 1),
	control_state TEXT NOT NULL CHECK (control_state IN ('managed', 'held')),
	provider_session_id TEXT CHECK (provider_session_id IS NULL OR length(provider_session_id) BETWEEN 1 AND 256),
	start_effect_id TEXT CHECK (start_effect_id IS NULL OR length(start_effect_id) BETWEEN 1 AND 128),
	CHECK ((provider_session_id IS NULL) = (start_effect_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX native_provider_identity ON native_sessions(
	machine_id, harness, interface, account_ref, profile_ref, provider_session_id
) WHERE provider_session_id IS NOT NULL;
CREATE TRIGGER native_session_kept BEFORE UPDATE ON native_sessions
WHEN NEW.session_id IS NOT OLD.session_id OR NEW.machine_id IS NOT OLD.machine_id
	OR NEW.install_id IS NOT OLD.install_id OR NEW.harness IS NOT OLD.harness
	OR NEW.interface IS NOT OLD.interface OR NEW.account_ref IS NOT OLD.account_ref
	OR NEW.profile_ref IS NOT OLD.profile_ref OR NEW.workdir_ref IS NOT OLD.workdir_ref
	OR NEW.model IS NOT OLD.model OR NEW.effort IS NOT OLD.effort
	OR NEW.qualification_ref IS NOT OLD.qualification_ref
	OR NEW.controller_id IS NOT OLD.controller_id OR NEW.generation IS NOT OLD.generation
	OR (OLD.control_state = 'held' AND NEW.control_state != 'held')
	OR (OLD.provider_session_id IS NOT NULL AND NEW.provider_session_id IS NOT OLD.provider_session_id)
	OR (OLD.start_effect_id IS NOT NULL AND NEW.start_effect_id IS NOT OLD.start_effect_id)
BEGIN SELECT RAISE(ABORT, 'native session identity and held control are kept'); END;
CREATE TRIGGER native_session_no_delete BEFORE DELETE ON native_sessions
BEGIN SELECT RAISE(ABORT, 'native session is kept'); END;
CREATE TRIGGER native_session_no_replace BEFORE INSERT ON native_sessions
WHEN EXISTS (SELECT 1 FROM native_sessions WHERE session_id = NEW.session_id)
BEGIN SELECT RAISE(ABORT, 'native session identity is kept'); END;
CREATE TRIGGER native_session_starts_unbound BEFORE INSERT ON native_sessions
WHEN NEW.provider_session_id IS NOT NULL OR NEW.start_effect_id IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'native session starts without provider binding'); END;
CREATE TABLE native_effects(
	effect_id TEXT PRIMARY KEY CHECK (length(effect_id) BETWEEN 1 AND 128),
	command_id TEXT NOT NULL UNIQUE CHECK (length(command_id) BETWEEN 1 AND 128),
	session_id TEXT NOT NULL REFERENCES native_sessions(session_id),
	generation INTEGER NOT NULL CHECK (generation > 0),
	kind TEXT NOT NULL CHECK (kind IN ('start', 'send_turn', 'resume', 'interrupt', 'stop')),
	command_body_digest TEXT NOT NULL CHECK (length(command_body_digest) = 64 AND command_body_digest NOT GLOB '*[^0-9a-f]*'),
	admission_order INTEGER NOT NULL CHECK (admission_order > 0),
	state TEXT NOT NULL CHECK (state IN ('prepared', 'may_have_run', 'confirmed', 'no_effect', 'reconcile_required')),
	retry_of_effect_id TEXT NOT NULL DEFAULT '',
	evidence_kind TEXT NOT NULL DEFAULT '',
	evidence_digest TEXT NOT NULL DEFAULT '' CHECK (evidence_digest = '' OR
		(length(evidence_digest) = 64 AND evidence_digest NOT GLOB '*[^0-9a-f]*')),
	CHECK (evidence_kind IN ('', 'provider_receipt', 'provider_absence',
		'operator_reconciled', 'prelaunch_refusal')),
	CHECK ((state IN ('confirmed', 'no_effect')) = (evidence_kind != '')),
	CHECK ((state IN ('confirmed', 'no_effect')) = (evidence_digest != '')),
	CHECK (state != 'confirmed' OR evidence_kind IN ('provider_receipt', 'operator_reconciled')),
	CHECK (state != 'no_effect' OR evidence_kind IN
		('provider_absence', 'operator_reconciled', 'prelaunch_refusal')),
	UNIQUE(session_id, admission_order)
) STRICT;
CREATE INDEX native_effects_unresolved ON native_effects(effect_id)
	WHERE state IN ('prepared', 'may_have_run', 'reconcile_required');
CREATE UNIQUE INDEX native_retry_successor ON native_effects(retry_of_effect_id)
	WHERE retry_of_effect_id != '';
CREATE TRIGGER native_provider_bound BEFORE UPDATE OF provider_session_id, start_effect_id ON native_sessions
WHEN OLD.provider_session_id IS NULL AND
	NOT EXISTS (SELECT 1 FROM native_effects WHERE effect_id = NEW.start_effect_id
	AND session_id = NEW.session_id AND kind = 'start' AND generation = NEW.generation
	AND state IN ('may_have_run', 'reconcile_required', 'confirmed'))
BEGIN SELECT RAISE(ABORT, 'native provider binding needs a claimed start'); END;
CREATE TRIGGER native_effect_order BEFORE INSERT ON native_effects
WHEN NEW.admission_order != coalesce((SELECT max(admission_order) + 1 FROM native_effects
	WHERE session_id = NEW.session_id), 1) OR NEW.state != 'prepared'
BEGIN SELECT RAISE(ABORT, 'native effects start prepared in admission order'); END;
CREATE TRIGGER native_effect_owner BEFORE INSERT ON native_effects
WHEN NOT EXISTS (SELECT 1 FROM native_sessions WHERE session_id = NEW.session_id
	AND generation = NEW.generation AND (control_state = 'managed'
		OR NEW.kind IN ('interrupt', 'stop')))
BEGIN SELECT RAISE(ABORT, 'native effect needs the current controller generation'); END;
CREATE TRIGGER native_effect_logged BEFORE INSERT ON native_effects
WHEN NOT EXISTS (SELECT 1 FROM command_log WHERE command_id = NEW.command_id
	AND kind = 'native.' || NEW.kind AND length(command) > 0 AND status != 'finished')
BEGIN SELECT RAISE(ABORT, 'native effect needs its logged command'); END;
CREATE TRIGGER native_effect_retry BEFORE INSERT ON native_effects
WHEN NEW.retry_of_effect_id != '' AND NOT EXISTS (
	SELECT 1 FROM native_effects WHERE effect_id = NEW.retry_of_effect_id
	AND session_id = NEW.session_id AND kind = NEW.kind
	AND generation = NEW.generation AND state = 'no_effect')
BEGIN SELECT RAISE(ABORT, 'native retry needs earlier no-effect proof'); END;
CREATE TRIGGER native_bound_start_no_effect BEFORE UPDATE OF state ON native_effects
WHEN NEW.state = 'no_effect' AND OLD.state != 'no_effect'
	AND EXISTS (SELECT 1 FROM native_sessions WHERE start_effect_id = OLD.effect_id)
BEGIN SELECT RAISE(ABORT, 'bound native start cannot be no-effect'); END;
CREATE TRIGGER native_claim_guard BEFORE UPDATE OF state ON native_effects
WHEN OLD.state = 'prepared' AND NEW.state = 'may_have_run' AND (
	NOT EXISTS (SELECT 1 FROM native_sessions WHERE session_id = NEW.session_id
		AND generation = NEW.generation AND (control_state = 'managed'
			OR NEW.kind IN ('interrupt', 'stop')))
	OR (NEW.kind NOT IN ('interrupt', 'stop') AND EXISTS (
		SELECT 1 FROM native_effects WHERE session_id = NEW.session_id
		AND admission_order < NEW.admission_order
		AND state NOT IN ('confirmed', 'no_effect'))))
BEGIN SELECT RAISE(ABORT, 'native claim needs current control and order'); END;
CREATE TRIGGER native_effect_kept BEFORE UPDATE ON native_effects
WHEN NEW.effect_id IS NOT OLD.effect_id OR NEW.command_id IS NOT OLD.command_id
	OR NEW.session_id IS NOT OLD.session_id OR NEW.generation IS NOT OLD.generation
	OR NEW.kind IS NOT OLD.kind OR NEW.command_body_digest IS NOT OLD.command_body_digest
	OR NEW.admission_order IS NOT OLD.admission_order
	OR NEW.retry_of_effect_id IS NOT OLD.retry_of_effect_id
	OR NOT ((OLD.state = 'prepared' AND NEW.state IN ('may_have_run', 'no_effect'))
		OR (OLD.state = 'may_have_run' AND NEW.state IN ('reconcile_required', 'confirmed', 'no_effect'))
		OR (OLD.state = 'reconcile_required' AND NEW.state IN ('confirmed', 'no_effect')))
	OR (OLD.evidence_kind != '' AND NEW.evidence_kind IS NOT OLD.evidence_kind)
	OR (OLD.evidence_digest != '' AND NEW.evidence_digest IS NOT OLD.evidence_digest)
BEGIN SELECT RAISE(ABORT, 'native effect identity and state are kept'); END;
CREATE TRIGGER native_effect_no_delete BEFORE DELETE ON native_effects
BEGIN SELECT RAISE(ABORT, 'native effect is kept'); END;
CREATE TRIGGER native_effect_no_replace BEFORE INSERT ON native_effects
WHEN EXISTS (SELECT 1 FROM native_effects WHERE effect_id = NEW.effect_id)
BEGIN SELECT RAISE(ABORT, 'native effect identity is kept'); END;`
