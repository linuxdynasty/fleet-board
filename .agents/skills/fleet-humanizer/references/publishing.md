# Publishing: PR bodies, review replies, issue closes

Adapted from eshu's humanizer. Everything in the parent skill applies: plain
words, jargon explained, verbatim evidence.

## PR body

1. **What was wrong or missing,** in plain words, including how it showed up.
   "The dashboard showed Claude as 'resets now' because the reset date had
   passed" beats "rollover bug".
2. **What changed and why.** Mention a rejected alternative only when it
   explains a real tradeoff in the final change.
3. **Evidence:** the commands you ran with their exit codes, numbers with what
   they mean, and the limits of what was proven. A table when it helps compare.
4. **What the owner must decide or know,** near the top if it blocks a merge.
5. **Merge danger,** one line: "hard to undo" (a database change, a locked
   contract, a write to GitHub) or "easy to undo" (a revert fixes it), and what
   it touches in plain words: the docs, the agent on each machine, the hub, the
   stored data, the messages between them, or every machine at once.

The title says what changed, not the ticket number: "show a stale reading
instead of 'resets now'" beats "fix #31".

If review changed the approach, rewrite the title and body to match the final
diff.

## Review reply

What changed, where, and what proves it. If the reviewer was right and you had
argued otherwise, say so. Never close a thread with only "fixed".

## Issue close

Someone will read it in a year with no context: what was wrong, what shipped,
and, if the issue's own plan was wrong, what it missed.
