---
title: Why is dude doing this?
covers: []
---
Common questions about what dude did or did not do, each with the setting, limit or page that answers it. An agent asked "why" can usually answer from the task's records and the `settings` and `limits` tools.

## Delivery stopped and asked me

Delivery stops and asks the task's owner when a loop reaches its limit or a step fails. The banner on the task names the reason:

| Banner | Why | Your choices |
| --- | --- | --- |
| Review stuck | The fixer could not resolve a finding after **Attempts per finding** (2) tries. | Try again, accept the findings and go on, or stop. |
| Review rounds used up | Blocking findings are still open after **Review rounds** (5). | Try again, accept the findings and go on, or stop. |
| No changes made | The implementer finished without changing anything. | Try again, or stop. |
| A reviewer failed | One reviewer's Run failed; the round is not complete without it. | Resume it or try again, go on without it, or stop. |
| Implementer, Fixer or Tester failed | The agent's Run failed; the banner shows its error. | Resume it (while its Run is kept) or try again, or stop. |
| Criteria still failing | The tester still finds criteria failing after 2 fix rounds. | Fix it once more, accept the failure with your reason, or stop. |
| Test blocked | The tester could not check a criterion, or its pass named no evidence. | Test again, waive with your reason, or stop. |
| Checks unverified | An agent's checks report was not backed by the commands it ran. | Send it back once more, waive with your reason, or stop. |
| PR fix rounds used up | **Pull request fix rounds** or **Fix rounds per pull request** ran out. | Try again, or stop. See below. |
| A pull request conflicts | The pull request no longer merges with its base. | Resolve it on GitHub or ask dude to, then wait on it again; or stop. |
| Could not resolve conflicts | dude's own merge of the base still conflicts. | Try again, wait on it again after resolving it yourself, or stop. |
| The pull request moved | Someone pushed to the pull request while dude checked a fix. | Fix again from what the branch holds, wait, or stop. |
| CI is stuck | Checks stayed pending longer than **Ask when CI is stuck after** (60 minutes). | See to CI on GitHub, then wait on it again; or stop. |
| A pull request was closed | The task's pull requests went different ways. | Take what was merged, wait on the rest (if any is open), or stop. |

Accepting a failure or waiving a check needs your reason; the owner or an admin can do it. Every other choice is the owner's.

## It stopped fixing review comments

A pull request gets fixes within two limits: **Pull request fix rounds** (3 per review round, **Settings → Delivery**) and **Fix rounds per pull request** (5 in all, **Settings → GitHub**). When either runs out, dude asks the owner. Under the conductor, it can ask to raise either for this task. See [Limits](help:limits#fix-rounds-on-pull-requests).

Other reasons a comment did not wake a fixer:

- its author is not allowed by **Who can wake a fixer**;
- it is from a bot, from dude's own login, or only says thanks;
- it mentions `@dude`, which goes to the conductor instead.

## It didn't open a pull request

- **GitHub is not connected**, or the project has no repository. See [GitHub](help:github#connecting-github).
- **The conductor decides**, and is waiting for your answer: Open, Draft, Show me the diff, or Another round.
- **Open feedback** from people blocks opening the pull request until it is handled.
- **A test is blocked or failing**, or a reviewer's finding still blocks.
- **The delivery escalated** and waits on the owner.

## It asked me a question

An agent asks when it needs a decision only a person can make, or when a loop's limit is reached. Only the task's owner can answer a task agent's question. See [Questions and feedback](help:questions-and-feedback#when-an-agent-asks-you-a-question).

## A reviewer didn't run

Every round runs the **required reviewers** (Correctness, Tests and Performance by default). The others — Security, Database, Frontend, API — join only when the change touches their paths. See [Delivering a task](help:delivery#which-reviewers-run). Ask the conductor which reviewers a task's changed paths pick; it reads that with `settings`.

A review of the simplifier's changes, or of a pull request fix, covers only that diff and runs the required reviewers.

## The security reviewer ran on a small change

It ran because a changed path matched its globs: `**/auth/**`, `**/payments/**` or `**/*credential*` by default. Change them in **Settings → Delivery**.

## An agent is parked or idle

dude parks an agent — stops it, keeping its workspace and conversation — so it holds no machine while it waits:

- **Waiting on a person** past **Park an agent after** (10 minutes). Your answer resumes it.
- **The conductor**, after **Keep the conductor warm for** (5 minutes) since it answered. Your next message resumes it.
- **Quiet mid-turn** — saying nothing, running nothing — past **Nudge a quiet agent after** (off by default). It is nudged once, then parked after as long again. The task goes to awaiting input, and only your Resume takes it up.

## An agent made no progress

When a phase Run makes no progress for its role's **Time limit without progress** (2 hours by default), the owner is told, with dude's facts about it: its open tool calls, processes, and what changed. Under the conductor, the conductor is told after 30 minutes. You can **Restart it**, **Leave it**, or **Stop the task**. A phase Run is stopped at 4 hours of running time whatever happens.

## A task won't start

- It is **blocked** by another task that has not merged. Use **Start when unblocked** or **Start anyway**. See [Tasks](help:tasks#relations-between-tasks).
- It is **closed** or **archived**. Reopen or unarchive it first.

## An agent can't download a package

The container reaches only the hosts in **Settings → Network**. Add the registry, or a preset for the toolchain. A project's **Refused recently** lists what agents tried and were refused. See [Network](help:network#what-agents-may-reach).

## A Run failed before it started

- The role's tier names no model. See [Models and tiers](help:models-and-tiers#when-a-tier-names-no-model).
- The image failed to build, or its machine size fits no host in its pool. See [Machines and images](help:machines-and-images#machine-sizes).

## A preview isn't starting

- The project has no server marked **Start automatically in branch previews**.
- Its first start may build images: **First wake timeout** (15 minutes) bounds how long the first wake waits.
- A server binds `localhost` instead of `0.0.0.0`, so the URL cannot reach it.
- After a move to another host, servers do not restart; start them again.

See [Servers and previews](help:servers-and-previews#branch-previews).

## My settings change didn't apply

A delivery keeps the policy it started with. **Start over** uses settings as they are now. Network and machine changes reach Runs started after them. See [Limits](help:limits#a-delivery-keeps-the-limits-it-started-with).
