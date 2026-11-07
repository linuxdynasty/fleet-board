# internal/agent/attach

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- The view never moves the developer's terminal: grouped session, own
  current window, and only the view's window/pane is ever selected.
- Input is dropped for read-only attaches in the pump and on the client;
  output is stripped but never dropped.
- The PTY master handle is read under lock: pumps snapshot it, Close nils
  it, and a nil master means finish already owns the ending.
- A change here updates this README and `doc.go` in the same commit.
