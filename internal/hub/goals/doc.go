// Package goals is the hub side of dispatched goals (plan row 22): the
// goal state machine and its phase classes (03l W6), the dispatch checks
// with their Block, Confirm, Warn and Info severities (03h), attempts
// (every retry is a new goal), the verifier (a DONE claim checked against
// the code host before the worker is freed), READY_FOR_HUMAN_MERGE, and
// the work item stage after a goal ends. Observed goals stay in the
// observed package (03k); the scheduler pass that picks work is row 23
// and is not here.
package goals
