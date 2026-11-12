# run_workflow

Start a GitHub Actions workflow (workflow_dispatch) on one of your task's repositories, and have dude follow its run to the end. The workflow must have a workflow_dispatch trigger at that ref. One that also runs on push or pull_request is CI this change already runs: dispatch it yourself. Any other (release, deploy, schedule-only) is the person's to decide: ask them (ask_person) naming the workflow, and pass their answered question as questionId.

```json
{
  "type": "object",
  "properties": {
    "repository": {
      "type": "string",
      "description": "the repository, by name: one of your task's; may be left out when it has one"
    },
    "workflow": {
      "type": "string",
      "description": "the workflow's file name under .github/workflows (ci.yml)"
    },
    "ref": {
      "type": "string",
      "description": "the branch or tag to run it on; default the task's branch"
    },
    "inputs": {
      "type": "object",
      "description": "the workflow's inputs, by name, as strings",
      "additionalProperties": {
        "type": "string"
      }
    },
    "questionId": {
      "type": "string",
      "description": "for a workflow that is not CI: the id of the person's answered question on this task that named it"
    }
  },
  "required": [
    "workflow"
  ],
  "additionalProperties": false
}
```
