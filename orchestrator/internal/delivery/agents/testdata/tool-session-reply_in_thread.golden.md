# reply_in_thread

Post a short message in the chat thread this session was started from, as dude, through the organisation's chat installation. The thread is this session's own; you name no channel. Write in the chat's markup (Slack: *bold*, _italic_, `code`, <https://example.com|links>). At most 3 posts per turn (everything after a person's latest message) and 3,000 characters a post; never a broadcast mention (<!channel>, <!here>, <!everyone>). People outside the organisation may read the thread: post answers, decisions and links, never logs, code or tool output. It answers the message's permalink.

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "the message, 1 to 3000 characters of the chat's markup: a short answer, or what was created with its link"
    }
  },
  "required": [
    "text"
  ],
  "additionalProperties": false
}
```
