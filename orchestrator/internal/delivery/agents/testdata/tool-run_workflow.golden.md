# run_workflow

Start a GitHub Actions workflow (workflow_dispatch) on one of your task's repositories, and have dude follow its run to the end, re-runs included. The workflow must have a workflow_dispatch trigger at that ref. One that already runs for this task's own change (on a push to its branch, or a pull request into its base, as its filters say) is its CI: dispatch it yourself on the task's branch. Any other (a deploy on push to main, a release on tags, a schedule, filters dude cannot read, or another ref) is the person's to decide: run_workflow's refusal names the two choices to ask them with (ask_person); pass their answered question as questionId. One answer authorizes one dispatch.

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
      "description": "for a workflow that is not this task's CI: the id of the person's question on this task answered with the approving choice the refusal named"
    }
  },
  "required": [
    "workflow"
  ],
  "additionalProperties": false
}
```
