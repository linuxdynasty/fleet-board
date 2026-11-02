package ring

// nativeWireKindsSQL replaces the journal's logged-command guard with the
// wire spelling. Migration 9 guarded on 'native.' plus the journal kind,
// but the executor's command log stores the Command.kind arm's message name
// ("SendNativeSession" et al., the executor's kindOf), so no production
// admission could ever pass the guard: the journal was specified before its
// wire arms existed. The journal is dormant (no production caller admits an
// effect yet), so no row can exist under the old spelling; the guard is
// replaced, not widened. NativeWireKind in native_types.go is the Go side of
// the same mapping; the two must agree.
const nativeWireKindsSQL = `DROP TRIGGER native_effect_logged;
CREATE TRIGGER native_effect_logged BEFORE INSERT ON native_effects
WHEN NOT EXISTS (SELECT 1 FROM command_log WHERE command_id = NEW.command_id
	AND kind = CASE NEW.kind
		WHEN 'start' THEN 'StartNativeSession'
		WHEN 'send_turn' THEN 'SendNativeTurn'
		WHEN 'resume' THEN 'ResumeNativeSession'
		WHEN 'interrupt' THEN 'InterruptNativeSession'
		WHEN 'stop' THEN 'StopNativeSession'
	END AND length(command) > 0 AND status != 'finished')
BEGIN SELECT RAISE(ABORT, 'native effect needs its logged command'); END;`
