# pull_request_note

Write your own section of one of your task's pull requests' description, on GitHub: for what a criterion or a person asks to be in the description (a benchmark's line, a link, a note for reviewers). It writes or replaces the one section that is yours, between hidden markers; the person's own text and dude's other sections are kept as they are. Each call replaces your section whole, so give everything it should hold. Refused for another task's pull request, for one dude did not open (its description is its author's), over the size bound or the room GitHub's limit leaves, and for text with an HTML comment (<!--) in it. A refresh of dude's own sections at the same moment can drop yours; if a later read of the description lacks it, write it again.

```json
{
  "type": "object",
  "properties": {
    "pr": {
      "type": "string",
      "description": "the pull request: its number, repo#number, owner/repo#number or URL, one of your task's"
    },
    "text": {
      "type": "string",
      "description": "your section, in Markdown: everything it should hold, at most 4000 bytes; no HTML comments"
    }
  },
  "required": [
    "pr",
    "text"
  ],
  "additionalProperties": false
}
```
