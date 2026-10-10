package typing

// Purpose names the gate that must pass before a line is sent; each
// allowed caller states one (row-7 ruling fork 11). The first four are the
// fleet.type.v1.SendKeysPurpose values, by the same name and number, so the
// control package maps them one to one (02h "SendKeys purposes"); this
// package does not decide them. The other three are gates that are not
// SendKeys gates (#140): the probe's commands, a harness command, and the
// goal start. Row 7 defines the set; each later row implements its gate.
// The zero value is refused.
type Purpose int

// The purposes. Add one only with an arbiter ruling and the gate it names.
const (
	PurposeUnspecified Purpose = iota
	// PurposePromptAnswer answers a prompt still showing (SendKeys, F3).
	PurposePromptAnswer
	// PurposeGoalNudge is the nudge for a stopped goal (SendKeys, F3).
	PurposeGoalNudge
	// PurposeGoalResume is the resume text of a paused goal (SendKeys, G4).
	PurposeGoalResume
	// PurposeOperatorText is free text from an operator (SendKeys, F3).
	PurposeOperatorText
	// PurposeProbeCommand types a probe's status, usage or catalog command
	// (row 8).
	PurposeProbeCommand
	// PurposeHarnessCommand types a catalog command (F8 RunHarnessCommand)
	// or a StartGoal step that runs one (row 18).
	PurposeHarnessCommand
	// PurposeGoalStart types the goal into the pane StartGoal chose (02h
	// step 13, row 21).
	PurposeGoalStart
	purposeEnd // one past the last purpose
)

var purposeNames = [...]string{
	PurposeUnspecified:    "unspecified",
	PurposePromptAnswer:   "prompt_answer",
	PurposeGoalNudge:      "goal_nudge",
	PurposeGoalResume:     "goal_resume",
	PurposeOperatorText:   "operator_text",
	PurposeProbeCommand:   "probe_command",
	PurposeHarnessCommand: "harness_command",
	PurposeGoalStart:      "goal_start",
}

// String is the purpose's snake_case name, the `purpose` field of the log
// events typing.sent and typing.refused; "unknown" for a value outside the
// set.
func (p Purpose) String() string {
	if p < 0 || p >= purposeEnd {
		return "unknown"
	}
	return purposeNames[p]
}

func (p Purpose) valid() bool { return p > PurposeUnspecified && p < purposeEnd }

// Key is one named key from the closed set a line may type (the NamedKey
// enum of fleet.type.v1). The zero value is refused.
type Key int

// The named keys.
const (
	KeyUnspecified Key = iota
	KeyEnter
	KeyEscape
	KeyTab
	KeyBackspace
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyCtrlC
	keyEnd // one past the last key
)

// keyNames are tmux's names for the keys, as send-keys reads them.
var keyNames = [...]string{
	KeyEnter:     "Enter",
	KeyEscape:    "Escape",
	KeyTab:       "Tab",
	KeyBackspace: "BSpace",
	KeyUp:        "Up",
	KeyDown:      "Down",
	KeyLeft:      "Left",
	KeyRight:     "Right",
	KeyCtrlC:     "C-c",
}

// String is tmux's name for the key; "" for a value outside the set.
func (k Key) String() string {
	if !k.valid() {
		return ""
	}
	return keyNames[k]
}

func (k Key) valid() bool { return k > KeyUnspecified && k < keyEnd }
