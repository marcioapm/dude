# preview_branch

Start a preview of a branch of a project linked to this session, for a person to look at: no task needed, each preview server at a URL of its own, waking when someone opens it. The branch must be on GitHub in the project's first repository. One preview per project branch: one already live is given back as it is, not started again. Answers the preview's id, its state, the branch, and each server's name and URL (once lux has given it one). It never previews a task, nor a project the session is not linked to.

```json
{
  "type": "object",
  "properties": {
    "project": {
      "type": "string",
      "description": "a linked project's key (like BL); optional when only one is linked"
    },
    "branch": {
      "type": "string",
      "description": "the branch to preview, as GitHub names it"
    }
  },
  "required": [
    "branch"
  ],
  "additionalProperties": false
}
```
