## refusals/domain/coordination/say-something-text-required

say something: text is required

## refusals/domain/coordination/message-at-most-kib-say-less

a message is at most %d KiB: say less, or pin a note

## refusals/domain/coordination/no-run-will-read-this-carry

%s is %s: no Run will read this. Carry on with your assumption, or ask a person.

## refusals/domain/coordination/refused-this-run-has-sent-messages

Refused: this Run has sent %d messages in the last hour, the most a Run may send. The next is allowed at %s UTC. Until then, `dude task list --peers`, `dude diff --task %s` and the epic notes may already answer it; if they don't, carry on and ask then.

## refusals/domain/coordination/no-message

no message %s

## refusals/domain/coordination/not-message-between-your-task-and

%s is not a message between your task and %s: a reply goes to the other task of its thread

## refusals/domain/notes/note-needs-its-text

a note needs its text

## refusals/domain/notes/note-at-most-characters-short-working

a note is at most %d characters: a short working fact

## refusals/domain/notes/this-epic-has-no-note

this epic has no note %d

## refusals/domain/notes/note-was-replaced-by-note-at

note %d was replaced by note %d at %s: replace note %d

## refusals/domain/notes/note-was-retired-at-pin-new

note %d was retired at %s: pin a new note instead

## refusals/domain/relations-write/task-can-t-be-related-itself

a task can't be related to itself

## refusals/domain/relations-write/and-are-already-related

%s and %s are already related

## refusals/domain/relations-write/that-would-make-loop-task-can

that would make a loop: %s. A task can't wait on itself

## refusals/domain/relations-write/has-no-relation-with

%s has no %s relation with %s

## refusals/domain/relations-write/you-can-remove-only-relation-this

you can remove only a relation this task's agents added; ask %s, its owner

## refusals/domain/attachments/message-carries-at-most-images

a message carries at most %d images

## refusals/domain/attachments/image-named-twice

image %s is named twice

## refusals/domain/attachments/image-not-one-this-task

image %s is not one of this task's

## refusals/domain/attachments/image-was-already-sent

image %s was already sent

## refusals/domain/attachments/message-images-are-at-most-mib

a message's images are at most %d MiB together

## refusals/domain/attachments/image-another-task-upload-this-one

image %s is another task's; upload it to this one

## refusals/domain/attachments/image-was-sent-with-message-upload

image %s was sent with a message; upload it again to use it here

## refusals/domain/attachments/task-text-shows-at-most-images

a task's text shows at most %d images

## refusals/domain/attachments/task-images-are-at-most-mib

a task's images are at most %d MiB together

## refusals/domain/escalations/task-not-found

task %s not found

## refusals/domain/escalations/delivery-task-not-waiting-for-decision

delivery of task %s is not waiting for a decision

## refusals/domain/escalations/not-way-go-on-after-one

%q is not a way to go on after %s; one of %s

## refusals/domain/escalations/needs-note-say-why-task-may

%s needs a note: say why the task may ship with %s

## refusals/domain/escalations/what-stopped-no-longer-kept-resume

what stopped is no longer kept to resume; try again or start over

## refusals/domain/feedback/draftids-and-submissionkey-are-required

draftIds and a submissionKey are required

## refusals/domain/feedback/drafts-are-not-your-drafts-on

%d of the %d drafts are not your drafts on this task's current attempt: nothing was submitted

## refusals/domain/feedback/submission-key-was-used-for-another

submission key %q was used for another batch: use a new key for new drafts

## refusals/domain/feedback/this-task-done-feedback-on-goes

this task is done: feedback on it goes in a follow-up task. Your drafts are kept.

## refusals/domain/feedback/this-task-recover-first-then-submit

this task is %s: Recover it first, then submit. Your drafts are kept.

## refusals/domain/feedback/this-task-delivery-has-ended-start

this task's delivery has ended: start it over to give it feedback. Your drafts are kept.

## refusals/domain/feedback/feedback-not-found

feedback %s not found

## refusals/domain/feedback/feedback-on-attempt-which-read-only

feedback %s is on attempt %d, which is read-only

## refusals/domain/feedback/reply-needs-words

a reply needs words

## refusals/domain/feedback/feedback-resolved-by-commit-not-yet

feedback %s is resolved by a commit not yet published: reopen it to ask for more, or resolve it to confirm

## refusals/domain/feedback/feedback-only-resolved-or-replied-item

feedback %s is %s: only a resolved or replied item is reopened

## refusals/domain/feedback/say-why-reply-needs-words

say why: a reply needs words

## refusals/domain/feedback/feedback-there-nothing-reply-until-person

feedback %s is %s: there is nothing to reply to until a person reopens it

## refusals/domain/feedback/feedback-not-your-task

feedback %s is not your task's

## refusals/domain/feedback/commit-not-commit-sha-resolve-with

commit %q is not a commit SHA: resolve with the commit that fixed it (`--commit HEAD`)

## refusals/domain/feedback/feedback-already

feedback %s is %s already

## refusals/domain/feedback/only-implementer-or-fixer-whose-commit

only the implementer or fixer whose commit fixed it resolves feedback; reply instead, or start a fix

## refusals/domain/feedback/feedback-on-resolve-with-commit-there

feedback %s is on %s: resolve it with a commit there, not in %s

## refusals/domain/feedback/not-repository-this-task-changes

%s is not a repository this task changes (%s)

## refusals/domain/feedback/this-task-changes-name-repository-your

this task changes %s: name the repository your commit is in (--repo)

## refusals/domain/feedback/at-most-20-ids-at-time

at most 20 ids at a time

## refusals/domain/integration/dude-not-waiting-on-this-pull

dude is not waiting on this pull request: it is busy with the task or has stopped

## refusals/domain/integration/dude-waiting-on-decision-about-something

dude is waiting on a decision about something else

## refusals/domain/integration/dude-already-working-on-task-try

dude is already working on the task; try again once it waits on the pull request

## refusals/domain/steer/steer-at-most-kib-text

a steer is at most %d KiB of text

## refusals/domain/steer/text-or-image-required

text or an image is required

## refusals/domain/steer/run-not-found

run %s not found

## refusals/domain/steer/run-branch-preview-not-agent

run %s is a branch preview, not an agent's

## refusals/domain/steer/run-and-can-no-longer-be

run %s is %s and can no longer be steered

## refusals/domain/steer/message-sent-again-carries-images-had

a message sent again carries the images it had: send new images in a new message

## refusals/domain/lifecycle/agent

%s Nobody can start it anyway until a person does that. Tell the person, and end your turn.

## refusals/domain/lifecycle/archived

%s is archived: unarchive it to start it.

## refusals/domain/lifecycle/closed

%s was closed as %s: reopen it to start it.

## refusals/domain/lifecycle/closed-pick-up

%s was closed: reopen it to pick it back up.

## refusals/domain/attachments/image-not-one-this-session

image %s is not one of this session's

