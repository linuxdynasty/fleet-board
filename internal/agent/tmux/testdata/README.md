# Recorded control-mode transcripts

`control-3.2a.txt` and `control-3.7c.txt` are the raw stdout of one
`tmux -C attach` client, byte for byte, recorded on 2026-10-10 on macOS
(Apple silicon). tmux 3.2a was built from the release tarball (SHA-256
`551553a4f82beaa8dadc9256800bcc284d7c000081e47aa6ecbb6ff36eacd05f`, the hash
in `spikes/tmuxtyping/results/README.md`); 3.7c is Homebrew's.

How they were made, so a new version can be added the same way:

- A throwaway server only: `TMUX_TMPDIR` set to a fresh short `mktemp -d`,
  `-S <that dir>/s`, `-f /dev/null`, and a clean environment
  (`env -i PATH=/usr/bin:/bin HOME=<that dir> TERM=xterm-256color`). Never the
  developer's own server.
- `new-session -d -s fx -x 80 -y 24 'exec cat'`, then the control client was
  fed, with short pauses: `list-panes -a -F`, two `refresh-client -B`
  subscriptions (`fleet-input` over `#{S:...}`, and `pane-size` whose value
  holds ` : `), a `new-window` running `printf` with a tab, a backslash, an
  escape sequence and UTF-8, `send-keys -l`, `refresh-client -f
  pause-after=1`, `refresh-client -A '%0:pause'` and `'%0:continue'`,
  commands that fail (`kill-pane -t %99`, an unknown command), `kill-pane`
  then `list-panes -t` of the gone pane, `display-message -p` of lines that
  look like guards and notifications, and `kill-server`.
- After `kill-server`, `has-session` failed (rc 1) and the folder was
  removed.

Pane text is only the fixture's own `printf` and `send-keys` strings: no user
name, host name or path. The session name is `fx`. `.gitattributes` keeps the
trailing space tmux writes after a `#{S:...}` value.

`fuzz/FuzzParse/` holds hand-made seed inputs for `FuzzParse`; the two
transcripts are added as seeds in the test.
