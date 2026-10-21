# settings

dude's settings as they apply here, each value with where it is set: dude (its built-in default), organization or project. Sections: delivery (the delivery policy: reviewers, including the path globs that add one, blocking severities, review and fix rounds, simplify, test, park, idle and warm minutes, the conductor's publish size), roles (each agent role's model tier, harness, time limit, machine size, image, and whether notes are set), prompts (built-in or saved, added to or replaced, saved when and by whom; never the text), github, network (egress, and which roles may search and read the web), capabilities (each role's tools) and machines. With a task, also what its delivery is held to: the frozen policy where it differs from today's, raises people granted, and the reviewers its changed paths pick. Read-only: a person changes a setting in Settings.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a project's id, key or name; default your task's project, or for a session each linked project"
    },
    "task": {
      "type": "string",
      "description": "a task's key or id: adds what its delivery is held to; a conductor's default is its own task"
    },
    "section": {
      "type": "string",
      "description": "one of delivery, roles, prompts, github, network, capabilities, machines; default all"
    }
  },
  "additionalProperties": false
}
```
