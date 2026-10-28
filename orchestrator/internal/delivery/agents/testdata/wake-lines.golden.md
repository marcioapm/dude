## decision-waiting-start

Decision waiting: start: nothing has run yet.

## decision-waiting-implemented

Decision waiting: after implement. Run run_impl finished: 2 files changed.

## decision-waiting-reviewed

Decision waiting: after a review round.

## decision-waiting-fixed

Decision waiting: after a fix.

## decision-waiting-before-pr

Decision waiting: before the pull request.

## decision-waiting-pr-feedback

Decision waiting: pull request feedback.

## decision-waiting-unknown

Decision waiting: .

## run-failed

Your review · security Run run_rev failed: lux: the host went away xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…

## escalated-stuck

Escalated to a person: stuck. Its actions: retry, accept, stop. Only the person decides: explain and propose (the banner works too). Ask with ask_person, the actions as choices; a choice whose action needs the person's reason or direction carries the one you propose as its note, which the owner's pick makes theirs. A free answer of the owner lets you decide_escalation. Retry needs direction for the next fix: offer it with the direction you propose as its note.

## escalated-test-failed

Escalated to a person: test failed criteria. Its actions: retry, accept, stop. Only the person decides: explain and propose (the banner works too). Ask with ask_person, the actions as choices; a choice whose action needs the person's reason or direction carries the one you propose as its note, which the owner's pick makes theirs. A free answer of the owner lets you decide_escalation. The test's verdict per criterion and its evidence are on the task; accept or waive ships past a criterion nobody saw work, and needs a reason: the note you propose, or the person's own. Criteria: 2 (fail): Paying sends a receipt.

## escalated-test-blocked

Escalated to a person: test blocked. Its actions: retry, waive, stop. Only the person decides: explain and propose (the banner works too). Ask with ask_person, the actions as choices; a choice whose action needs the person's reason or direction carries the one you propose as its note, which the owner's pick makes theirs. A free answer of the owner lets you decide_escalation. The test's verdict per criterion and its evidence are on the task; accept or waive ships past a criterion nobody saw work, and needs a reason: the note you propose, or the person's own. Criteria: 1 (blocked): The export downloads.

## escalated-checks

Escalated to a person: checks unverified. Its actions: retry, waive, stop. Only the person decides: explain and propose (the banner works too). Ask with ask_person, the actions as choices; a choice whose action needs the person's reason or direction carries the one you propose as its note, which the owner's pick makes theirs. A free answer of the owner lets you decide_escalation. The checks not verified: npm run test:legacy: not-run — not passed by the judge: no later phase runs the legacy Vue jest suite (judgement: no); missing checks: the legacy Vue jest run covers the changed component (judgement: yes).

## escalated-checks-many

Escalated to a person: checks unverified. Its actions: retry, waive, stop. Only the person decides: explain and propose (the banner works too). Ask with ask_person, the actions as choices; a choice whose action needs the person's reason or direction carries the one you propose as its note, which the owner's pick makes theirs. A free answer of the owner lets you decide_escalation. The checks not verified: make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; make check: exit 1; and 3 more.

## escalation-answered

The owner's answer to your question qst_1 decided the escalation: retry. The delivery goes on.

## escalation-on-banner

The person decided the escalation on the banner: accept. Your question qst_2 about it is closed. The delivery goes on.

## escalation-on-banner-said

The person decided the escalation on the banner: retry. The task's owner, Ola Nordmann, said:

Sync to main. Your question qst_2 about it is closed. The delivery goes on.

## escalation-checks-finished

The checks finished, so dude decided to wait on the pull request again. Your question qst_2 about it is closed. The delivery goes on.

## steer-read

Your implement Run run_impl read your steer dir_1.

## steer-failed

Your steer dir_1 to your fix Run run_fix was not delivered: the Run ended yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy…

## pr-merged

Pull request api#12 was merged. Close out: say so in Chat if it helps the people on the task. Start nothing.

## pr-closed

Pull request api#12 was closed without merging. Close out: say so in Chat if it helps the people on the task. Start nothing.

## published

Your publish pub_1 is on the task branch: api@1111111 web@2222222, 40 lines in 3 files. Run a review before the pull request.

## published-in-part

Your publish pub_1 is on the task branch in part: published api@1111111; web: the branch moved. Run a review before the pull request.

## publish-refused

Your publish pub_2 was refused, nothing moved: the checkout is behind zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz…

## publish-stalled

Your publish pub_3 could not be confirmed on the task branch (api to 1111111, web to 2222222): check the branch before you publish again.

## unblocked

GLD-8 merged: GLD-7 can start now, nothing open in front of it. Tell the person; start nothing until they agree.

## unblocked-auto

GLD-8 merged: GLD-7 can start now. Its owner set it to start when unblocked, so it starts on its own: say so if it helps.

## run-asks

Your implement Run run_impl asks the person a question (qst_3): read it in its chat.

## review-question

Reviewer correctness (run_rev) asks rvq_1, needed from product: Do invoices close in UTC or in the organisation's zone? wwwwwwwwwwwww… Answer it with answer_review_question only if you established the fact, else ask the person.

## review-question-no-needed-from

Reviewer security (run_rev) asks rvq_2: Who may export? Answer it with answer_review_question only if you established the fact, else ask the person.

## review-question-answered

The person answered reviewer question rvq_1: In UTC. Always. Decide whether a review should weigh it (start_phase review).

## review-question-withdrawn

The person withdrew reviewer question rvq_1. Decide whether a review should weigh it (start_phase review).

## base-moved

`main` moved: GLD-8 merged (#14). Call sync_base if your work depends on it; while a pull request is open on the task branch, publish is refused, so use start_phase fix to bring it in instead.

## stalled

Your security implement Run run_impl (round 2, fast, running 90 min) has made no progress for 15 min. Its agent has done nothing for 14 min: no output, no tool call. It has 3 tool calls open: `bash` "go test ./...", for 15 min, `task` "explore", for 10 min, `bash` "sleep 999", for 30 s; `bash`'s command is running; `task` runs inside the agent: no separate process is expected; no process runs `bash`'s command. Its `task` call "explore" has a child (subagent · ses_child_01) whose last event was 5 min ago: asked to run make — waiting on a permission nothing can answer: bash for make *, rm *. Processes: go test ./... (99.5 % CPU, up 15:00); sh (0.0 % CPU, up 1:00). CPU over 15 min: 12 s; network: 3.0 MiB. Its files last changed at 10:15 UTC. Its tool calls in the window: 3 bash, 1 read, 0 edit. Its plan: write the export; test it. Its last message: "Running the tests.". Leave it, steer it (interrupt to stop its turn), or restart_run(run, note, tier).

## stalled-quiet

Your review Run run_rev (running 50 s) has made no progress for 2.0 h. Its agent has done nothing for 2.0 h: no output, no tool call. It has 1 tool call open: `bash` "x", for 2.0 h; whether `bash`'s command still runs is unknown. Processes: could not be read (ps: not found). lux reported no CPU or network figures. Leave it, steer it (interrupt to stop its turn), or restart_run(run, note, tier).

## note-one

dude woke you; one thing:
- A decision waits.

Read what you need with your tools; take the decision waiting, or tell the person what you propose.

## note-many

dude woke you; 12 things:
- 1
- 2
- 3
- 4
- 5
- 6
- 7
- 8
- 9
- 10
- … and 2 more

Read what you need with your tools; take the decision waiting, or tell the person what you propose.

