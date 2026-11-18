## multiple-unbacked

Your checks report was not verified. Nothing from this Run has been pushed.

- make test: no call — dude recorded no call of "make test" whose exit status is its own; the recorded calls nearest it whose exit status is their own: "make test fast" (exit 2) (verdict: unbacked)

- make lint: missing evidence — dude recorded no call of "make lint" whose exit status is its own; the recorded calls nearest it are omitted here for space (run_chat reads them) (verdict: unbacked)

If one of the nearest calls is the check you meant, report it exactly as recorded; otherwise run the intended check as one bare command on one line and report it exactly as run, with the same environment assignments and env/assignment partition, no comment or annotation, a pipe only after set -o pipefail, and every pipeline stage and redirect unchanged.

Run the missing or unsuccessful checks as separate commands, read their output, and correct the report. End your next reply with a checks: YAML block. If you cannot proceed, ask a person what you need.

## send-back

Your checks report was not verified. Nothing from this Run has been pushed.

- npx tsc --noEmit: 95 errors on the parent, 94 with the change — not passed by the conductor: no run at the task's base is recorded (verdict: reject)

A reported failure that predates this task passes once this Run's own tool calls show the same command failing the same way at the commit of the target branch this task builds on (api: main at 1111111, web: release/2 at 2222222): run it in a separate checkout of that commit (for example `git worktree add` of it beside your checkout), not by setting your change aside, since the commit you started from holds this task's earlier work, and report both results in its evidence. A failure that commit does not have, or one in code this task changed, must be fixed.

Run the missing or unsuccessful checks as separate commands, read their output, and correct the report. End your next reply with a checks: YAML block. If you cannot proceed, ask a person what you need.

