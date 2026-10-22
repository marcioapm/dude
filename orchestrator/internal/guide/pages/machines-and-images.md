---
title: Machines and images
covers: [settings:machines, settings:images, tool:list_machine_sizes, tool:request_machine]
---
Every agent runs in a container of some size, built from some image. This page explains machine sizes, how an agent moves to a bigger machine, and the image library.

## Machine sizes

**Settings → Machines** lists the organisation's sizes: each one's CPUs, memory, disk and pool. One size is the **default**.

Each agent role names a size in its **Machine** field in **Settings → Agents**, on the organisation or overridden on a project. A role that names none runs on the default. The Fixer follows the Implementer's size unless you give it its own. Branch previews have their own **Machine** in a project's **Servers** settings.

Every member sees the sizes. Only an organisation admin adds, changes or removes them. A size must fit one host of its pool.

## When a size change applies

A change applies to sessions that start after it, and to a stopped one when it resumes. A running agent keeps its machine until then. A parked agent resumes on the size its settings name now.

## Moving to a bigger machine

An agent that runs out of room — memory for a build, CPUs for a test suite — can move itself to a larger size:

- `list_machine_sizes` lists the larger sizes it may move to;
- `request_machine` moves it, with a reason saying what ran out.

The move restarts the agent on the new size. Its conversation and checkout are kept, committed or not, with everything else in its workspace. Running processes are not. A Run can move at most 3 times.

Servers do not restart after a move. Start them again from the task. See [Servers and previews](help:servers-and-previews#starting-a-server-in-a-task).

A size a Run raised also applies to that role's later Runs on the task (an implementer's raise reaches the fixer too, unless the fixer names its own size). You can reset it from the task.

## Images

An **image** is what an agent, server or preview works in: its operating system, toolchains and tools. **Settings → Images** is the organisation's image library.

Each image has a Containerfile. dude adds its own layer under it. Editing an image starts a build; publishing a version makes it the one everything uses. Everything that uses an image gets the new version once it builds. Each image's history shows every version with its changes, and **Publish again** brings an older one back.

One published image can be **the default base**. Without any other choice, agents work in it.

Every member sees the library. Only an organisation admin adds, edits, builds and publishes images.

## Which image an agent works in

From most specific to least:

1. the role's **Image** in **Settings → Agents** (on the project, then the organisation);
2. the project's **Runtime image** in its **General** settings;
3. the organisation's default base.

The Fixer follows the Implementer's image unless you give it its own. A preview has its own **Image** in **Servers**; without one, it uses the project's runtime image.

Some images can run containers. An agent in one can start containers itself.

## What agents can read

Agents read machine sizes with `settings`, section `machines`, and each role's size with `roles`. Section `images`, asked for alone, lists the library: each image's name, description, whether it is the default, its published version and whether it can run containers. Agents never see Containerfiles or build logs.
