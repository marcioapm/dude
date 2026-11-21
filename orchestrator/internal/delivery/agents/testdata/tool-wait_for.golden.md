# wait_for

Say what you are waiting on, then end your turn: dude checks it in its own cycle and wakes you once, when it holds or at the deadline, with what held, what did not and what else happened meanwhile, worst first. conditions: runsEnded (Runs of this task; a restarted Run counts by its replacement) and prChecksFinal (a pull request's checks passing or failing on a head, the head moving, or the pull request ending; on an open pull request each fix dude publishes moves its head: wait on the new head) and workflowEnded (a workflow run_workflow dispatched: completed, unknown, or dude cannot see it) and after (a duration from the call, at least 30m and before the deadline) and siblingTask (another task on this task's epic becoming one of becomes after the call: started, stopped, review, ready_to_merge, awaiting_human, done, failed, aborted, closed; none for any change). With no conditions it is a timer. One wait at a time: a new one replaces it. Waiting decides nothing, and dude's own wakes — decisions, escalations, child results, cohort digests — still come. If it already holds you are told so at once. Refused for another task's Run, pull request or dispatch, and for a task off this task's epic.

```json
{
  "type": "object",
  "properties": {
    "conditions": {
      "type": [
        "null",
        "array"
      ],
      "items": {
        "type": "object",
        "properties": {
          "runsEnded": {
            "type": [
              "null",
              "array"
            ],
            "items": {
              "type": "string"
            }
          },
          "prChecksFinal": {
            "type": [
              "null",
              "object"
            ],
            "properties": {
              "pr": {
                "type": "string"
              },
              "head": {
                "type": "string"
              }
            },
            "required": [
              "pr"
            ],
            "additionalProperties": false
          },
          "workflowEnded": {
            "type": "string"
          },
          "after": {
            "type": "string"
          },
          "siblingTask": {
            "type": [
              "null",
              "object"
            ],
            "properties": {
              "task": {
                "type": "string"
              },
              "becomes": {
                "type": [
                  "null",
                  "array"
                ],
                "items": {
                  "type": "string"
                }
              }
            },
            "required": [
              "task"
            ],
            "additionalProperties": false
          }
        },
        "additionalProperties": false
      },
      "description": "up to 8, each one of: {\"runsEnded\": [\"run_…\"]}, {\"prChecksFinal\": {\"pr\": \"repo#12\", \"head\": \"\u003cfull SHA; default its head now\u003e\"}} {\"workflowEnded\": \"wfd_…\"}, {\"after\": \"45m\"} or {\"siblingTask\": {\"task\": \"CP-62\", \"becomes\": [\"done\"]}}"
    },
    "wake": {
      "type": "string",
      "description": "all (default): when every condition holds; any: when the first does"
    },
    "deadline": {
      "type": "string",
      "description": "required: a duration (45m, 2h) or a time, at most 24 hours ahead; you are woken then whatever holds"
    },
    "note": {
      "type": "string",
      "description": "what you mean to do then, told back to you with the wake"
    }
  },
  "required": [
    "deadline"
  ],
  "additionalProperties": false
}
```
