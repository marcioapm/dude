---
title: Servers and previews
covers: [settings:servers]
---
A server is a port an agent's container exposes at its own URL, with the command that starts it. This page covers defining servers, starting one in a task, branch previews, and commenting on a preview.

## Server definitions

A project's servers are defined in **Settings → Servers → Definitions**. Each one has:

- **Name** — lowercase letters, digits and dashes; it is the first label of the URL.
- **Port** — 1 to 65535. Health is a TCP check on it.
- **Command** — a shell line, run in the working directory, like `PORT=3000 npm start` or `cd web && npm run dev`. Bind `0.0.0.0`, not `localhost`: the URL reaches it from outside the container.
- **Working directory** — relative to the repository.
- **Setup command** — optional; runs once before the command, in the same directory.
- **Start automatically in branch previews** — whether a preview starts it.

Only an organisation admin changes a project's server definitions. Once defined, anyone can start a server in a task, or preview a branch with it.

Every server gets its own URL, behind your sign-in. Nothing is public. Previews open in a new tab.

## Starting a server in a task

On a task's **Servers**, add one of the project's servers, or an ad hoc one — a port and command just for this run. It runs in the agent's container, on its checkout.

Servers live on a Run. When an agent's Run ends, its servers end with it. Servers stop when a Run pauses or moves to another host, and do not restart by themselves: a person starts them again.

## Branch previews

**Preview branch** brings up the project's servers on a lightweight Run of their own — no agent, just the checkout of the task's branch. A task has at most one preview. **Stop preview** ends it; it also ends when its task is done, failed or stopped.

A preview follows its branch: when the branch moves, the preview syncs to it.

A preview sleeps when nobody uses it, and wakes when someone opens its URL. Its settings, in **Settings → Servers → Branch previews**:

| Setting | What it does | Default |
| --- | --- | --- |
| Machine | The size a preview runs on | the default size |
| Image | What a preview starts in | the project's runtime image |
| Egress allowlist | Hosts a preview may reach, beyond the repository; `*` allows anywhere | — |
| Secrets | Project secrets given to previews reach their servers and setup scripts | — |
| Idle timeout | With no request for this long, the preview is parked | 15 minutes |
| First wake timeout | How long a wake waits until the preview has served once; its first start may build images | 15 minutes |
| Wake timeout | How long every later wake waits | 5 minutes |

## Commenting on a preview

On a preview, you can leave feedback on the page itself: **Comment on preview**. Your comments are saved as drafts on the task and submitted like any other feedback. See [Questions and feedback](help:questions-and-feedback#feedback-on-a-task).

Commenting needs the deployment's public URL configured. A server started before commenting was turned on needs a **Restart** to show it.

## Project secrets

**Settings → Servers → Secrets** holds environment variables given only to the previews and agent roles each one names. dude shows a secret's name, who gets it and its last characters, never its value. A change reaches Runs started after it.

## What agents can read

Agents read a project's server definitions and preview settings with `settings`, section `servers`. Commands and setup lines are shown as typed but never run by the read. Secret values are never shown; only variable names.
