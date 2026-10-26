{{define "refusals/post-to-chat/automations-only"}}post_to_chat is only available to automations{{end}}

{{- define "refusals/post-to-chat/name-a-channel"}}name a channel: #engineering, engineering, or its id (C0123ABCD){{end}}

{{- define "refusals/post-to-chat/text-length"}}text is 1 to %d characters of Slack mrkdwn; this one is %d: shorten it, or split it over more than one post{{end}}

{{- define "refusals/post-to-chat/per-run"}}post_to_chat is limited to %d messages per run; this run has posted them{{end}}

{{- define "refusals/post-to-chat/not-connected"}}Slack is not connected for %s: an organisation admin adds dude's Slack app in Organisation settings → Chat. Nothing was posted{{end}}

{{- define "refusals/post-to-chat/unknown-channel"}}there is no channel %s that dude's Slack bot can see: check the name, or for a private channel ask someone in it to run /invite @dude, then call again{{end}}

{{- define "refusals/post-to-chat/not-in-channel"}}dude's Slack bot is not in %s: ask someone in the channel to run /invite @dude, then call again. Nothing was posted{{end}}

{{- define "refusals/post-to-chat/lookup-cut"}}could not finish looking up the channel %s; pass the channel id (C0123ABCD): the workspace has more channels than one lookup reads. Nothing was posted{{end}}

{{- define "refusals/post-to-chat/slack-busy"}}Slack did not take the post (%s): nothing was posted; try again in a minute{{end}}

{{- define "refusals/post-to-chat/slack-refused"}}Slack refused the post: %s. Nothing was posted{{end}}
