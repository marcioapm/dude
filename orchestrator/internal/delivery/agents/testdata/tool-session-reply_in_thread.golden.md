# reply_in_thread

Post a short message, as dude, through the organisation's chat installation, in the chat thread the turn's message came from: this session's own thread, or the direct message or other thread a person wrote to it from. You name no channel. A message typed in dude gets no thread reply: answer it in this session. Write plain text with *bold*, _italic_ and `code`; paste links bare. At most 3 posts per turn (everything after a person's latest message) and 3,000 characters a post; never a special mention (<!channel>, <!here>, <!everyone>, a user group). People outside the organisation may read the thread: post answers, decisions and links, never logs, code or tool output. It returns the post's permalink.

```json
{
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "the message, 1 to 3000 characters of plain text with *bold*, _italic_ and `code`: a short answer, or what was created with its link"
    }
  },
  "required": [
    "text"
  ],
  "additionalProperties": false
}
```
