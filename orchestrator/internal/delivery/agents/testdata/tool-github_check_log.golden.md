# github_check_log

Read a failed check's bounded failure excerpt, or what its check run reported when it is not a GitHub Actions job or GitHub cannot provide its log. The excerpt can retain failure sites from the middle of the log as well as its end; omission markers mean intervening log text was left out, not that no other failures occurred. Use the check id from github_pull_request before deciding whether to rerun or fix it. Read only; the result is marked as external text. External-text marking is not secret redaction: this tool does not load credentials to redact returned logs; caller-supplied known-secret redaction is separate.

```json
{
  "type": "object",
  "properties": {
    "repo": {
      "type": "string",
      "description": "the repository, owner/name, one of your projects'"
    },
    "checkId": {
      "type": "integer",
      "description": "the check id from github_pull_request"
    }
  },
  "required": [
    "repo",
    "checkId"
  ],
  "additionalProperties": false
}
```
