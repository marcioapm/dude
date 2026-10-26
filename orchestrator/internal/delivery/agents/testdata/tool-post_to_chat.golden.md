# post_to_chat

Post one message to a Slack channel, as dude's Slack bot, through this organisation's Slack installation: any channel the bot is in (someone in the channel adds it with /invite @dude). It answers the channel, the message's ts and a permalink to it. A run posts at most 10 messages; there is no thread and no edit, so write the whole message before you post it.

```json
{
  "type": "object",
  "properties": {
    "channel": {
      "type": "string",
      "description": "the channel: its name (#engineering or engineering) or its id (C0123ABCD)"
    },
    "text": {
      "type": "string",
      "description": "the message in Slack mrkdwn (*bold*, _italic_, `code`, ```blocks```, \u003chttps://example.com|links\u003e), 1 to 40000 characters"
    }
  },
  "required": [
    "channel",
    "text"
  ],
  "additionalProperties": false
}
```
