# stop_run

Stop a child agent you started (start_agent, start_agents, verify) that is stuck or no longer needed. It is aborted, and its result comes back to you as aborted, or in its cohort's digest. Its uncommitted work is lost. A phase Run the delivery waits on is not stopped here: restart_run replaces it in its step, and stopping the step is the person's Stop. Refused for yourself, another task's Run, a Run that has ended, or another conductor's child.

```json
{
  "type": "object",
  "properties": {
    "run": {
      "type": "string",
      "description": "your child agent's Run (run_…), still running"
    },
    "reason": {
      "type": "string",
      "description": "why it is stopped: shown in Chat, and told to nobody else"
    }
  },
  "required": [
    "run",
    "reason"
  ],
  "additionalProperties": false
}
```
