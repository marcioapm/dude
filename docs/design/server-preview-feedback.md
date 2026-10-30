# Server Preview Feedback

Taskless branch previews accept feedback through the existing injected overlay.
Connect to dude opens an authenticated, submission-only relay. The relay keeps
the preview's exact window and origin, uses the person's normal organization
membership, and binds to the looked-up preview Run ID and server name. An
account without membership is not authorized. No credentials enter the preview
snippet, and there are no new anonymous, invitation or CORS paths.

Task feedback is unchanged. Server feedback never creates tasks, sends agent
directives, or appears in a web inbox. Branch overlay sessions contain no saved
feedback or pins. The connection page shows connection/submission state only.

## Identity And Lifetime

`preview_feedback` belongs to an organization, project, preview Run ID and
`preview_servers.name`. Neither a branch name, hostname nor Lux Run ID is its
identity. The immutable preview anchor records the captured URL, selector,
component and other bounded context. `captureSha` is nullable: the relay does
not infer a captured page's commit from a later branch HEAD.

Pause, idle sleep, wake, process crash and recoverable Lux Run replacement keep
feedback. Terminal logical Run transitions, server deletion and server
tombstoning delete feedback and screenshot attachment metadata in the same
database transaction. Branch deletion and idle expiry use those existing
logical termination paths. A new preview ID, even of the same branch, starts
empty. Database live-owner checks and server/Run locks serialize submissions,
upload completion and resolution against termination.

Screenshots use the existing multipart image validation, byte/dimension limits,
storage bucket, authenticated attachment serving and asynchronous deletion
queue. Server image responses use `private, no-store`. Once metadata is deleted,
new authorized reads return 404; physical blob deletion is asynchronous and
retried by the existing sweeper, not distributed-atomic with database deletion.
Previously downloaded bytes cannot be recalled.

## API

All routes require normal dude authentication and tenant access. Existing
policy grants project reads to active organization members; there is no
separate project-reader ACL. Submission and status changes require a person
behind the credential, as existing feedback does.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/v1/branch-previews/:id/feedback` | Aggregate list, no per-server N+1 |
| GET | `/v1/branch-previews/:id/servers/:name/feedback` | Server-filtered list |
| POST | `/v1/branch-previews/:id/servers/:name/feedback` | Overlay submission |
| POST | `/v1/branch-previews/:id/servers/:name/attachments` | Existing multipart screenshot pipeline |
| GET | `/v1/preview-feedback/:id` | Full item including resolution history |
| POST | `/v1/preview-feedback/:id/resolve` | Resolve, optional `note` |
| POST | `/v1/preview-feedback/:id/reopen` | Reopen, optional `note` |

Lists accept `server`, `status=open|resolved|all` (default `open`), `after`
(opaque item-ID cursor) and `limit` (1-200, default 50), returning
`{feedback, nextCursor}`. Ordering is stable by sortable item ID, oldest first;
`order=desc` lists newest first, and its next page is `before=<nextCursor>`
(the Servers page's fold reads this way, so new feedback is on its first page).
IDs are never silently resolved by branch name. Missing and cross-tenant items return 404.

Submissions take `{body, anchor, submissionKey, captureSha?}`. `anchor` reuses
the bounded preview-anchor schema, with a screenshot attachment owned by the
same person and logical server. Its URL must match the server's exact origin.
The key is unique per logical server and author; retries return the same item.
A reconnect may upload the same screenshot bytes under a new attachment ID;
matching bytes and identical content remain the same submission. The unused
retry upload is swept normally. Changed content with a reused key is 409.
Resolve/reopen repeats do not append duplicate history or replace prior notes.

## CLI

```sh
dude preview list-feedback PREVIEW --server web --status open --limit 50
dude preview list-feedback PREVIEW --after CURSOR
dude preview list-feedback PREVIEW --order desc --before CURSOR
dude preview show-feedback ITEM
dude preview resolve-feedback ITEM --note 'Checked on the preview'
dude preview reopen-feedback ITEM --note 'Still reproducible'
```

`PREVIEW` is always a preview ID, not a branch reference. Commands return the
API's JSON using the existing CLI output and error handling. There is no public
feedback send command for branch previews. Route coverage is mapped in
`orchestrator/cmd/dude-cli/routes.json`.
