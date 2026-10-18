# rerun_failed_checks

Run the failed checks on one of your task's pull requests again, on GitHub: once per head, for a failure that is not the code's (a flaky runner, an outage). A real failure is a fix's.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, or repo#number, one of your task's"
    }
  },
  "required": [
    "pr"
  ],
  "additionalProperties": false
}
```
