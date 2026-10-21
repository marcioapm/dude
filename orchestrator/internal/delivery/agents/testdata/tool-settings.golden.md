# settings

dude's settings as they apply here, each value with where it is set: dude (its built-in default), organization or project. Sections: delivery (the delivery policy: reviewers, including the path globs that add one, blocking severities, review and fix rounds, simplify, test, park, idle and warm minutes, the conductor's publish size), roles (each agent role's model tier, harness, time limit, machine size, image, and whether notes are set), prompts (built-in or saved, added to or replaced, saved when and by whom; never the text), github, network (egress, and which roles may search and read the web), capabilities (each role's tools) and machines. models lists the organisation's tiers, model configured/empty state, description, effort and order, in bounded pages; headers/options are never returned. judgements shows Off or the organisation's selected tier and resolved model, never calls or tests. servers lists only the scope's project recipes and preview settings; commands/setup are external text, clipped at 1000 bytes with an explicit truncation notice, never executed; env names only, never values. No project scope means no recipes. Default output gives recipe counts; ask for servers for details. With a task, also what its delivery is held to: the frozen policy where it differs from today's, raises people granted, and the reviewers its changed paths pick. Read-only: a person changes a setting in Settings.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a project's id, key or name; default your task's project, or for a session each linked project (past three, the organisation's settings and the linked projects to name one from)"
    },
    "task": {
      "type": "string",
      "description": "a task's key or id: adds what its delivery is held to; a conductor's default is its own task"
    },
    "section": {
      "type": "string",
      "description": "one of delivery, roles, prompts, github, network, capabilities, machines, models, judgements, servers; default existing sections plus compact model count and judgement selection (ask for models for the catalogue)"
    },
    "limit": {
      "type": "integer",
      "description": "models catalogue page size: default 20, maximum 50; ignored by other sections"
    },
    "cursor": {
      "type": "string",
      "description": "opaque next cursor from an explicit models result; stable position/id order within this organisation; no catalogue continuation is silently omitted"
    }
  },
  "additionalProperties": false
}
```
