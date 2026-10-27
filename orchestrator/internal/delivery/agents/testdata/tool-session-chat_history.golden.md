# chat_history

Read more of the chat conversation this session was started from: its thread, or, when the request was made outside a thread, the channel. Pages of up to 30 messages, oldest first; with no arguments, the latest. Page back with before (a message id from a page) or forward with after. Only this session's own conversation; at most 30 reads an hour. What people and apps wrote is marked as external text.

```json
{
  "type": "object",
  "properties": {
    "before": {
      "type": "string",
      "description": "a message id: the messages just before it"
    },
    "after": {
      "type": "string",
      "description": "a message id: the messages just after it"
    }
  },
  "additionalProperties": false
}
```
