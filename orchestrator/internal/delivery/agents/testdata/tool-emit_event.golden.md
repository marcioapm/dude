# emit_event

Record an event on your run for the people following it: progress (type progress, data like {"done": 3, "of": 10, "step": "tests"}), a milestone, a measurement. It shows in your chat and the run's events.

```json
{
  "type": "object",
  "properties": {
    "type": {
      "type": "string",
      "description": "what kind of event: lowercase words and dots, like progress or tests.finished"
    },
    "data": {
      "description": "anything to go with it: an object like {\"done\": 3, \"of\": 10} (at most 16 KB)"
    }
  },
  "required": [
    "type"
  ],
  "additionalProperties": false
}
```
