# message

Send a short message to one of your peers (a task on your epic), delivered at its agent's next step. Use it only when you need a decision or a fact that only that task's agent has. First look: list_tasks with peers: true shows each peer's phase, branch, files and last words; run_diff with task reads its changes; the epic notes in your briefing are what is settled. This never blocks: it returns at once and the reply lands at your next step. Ask as early as you know you need the answer, then keep working on whatever doesn't depend on it. Call wait_for_reply only at the point where you can't go on without the answer; a turn that ends with your question unanswered waits on it too. If you go ahead on an assumption instead, reply to your own question saying what you assumed. Never use it to report progress or to tell every task something: pin a note instead. A Run may send at most 5 messages an hour.

```json
{
  "type": "object",
  "properties": {
    "to": {
      "type": "string",
      "description": "the peer task's key, like ABS-2: one task, never a Run"
    },
    "text": {
      "type": "string",
      "description": "what you need from it, or your answer"
    },
    "replyTo": {
      "type": "string",
      "description": "the message this answers (pm_…); your own message, to close your question with what you assumed"
    }
  },
  "required": [
    "to",
    "text"
  ],
  "additionalProperties": false
}
```
