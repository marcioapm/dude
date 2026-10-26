## automations-only

post_to_chat is only available to automations

## name-a-channel

name a channel: #engineering, engineering, or its id (C0123ABCD)

## text-length

text is 1 to 40000 characters of Slack mrkdwn; this one is 40001: shorten it, or split it over more than one post

## per-run

post_to_chat is limited to 10 messages per run; this run has posted them

## not-connected

Slack is not connected for Acme: an organisation admin adds dude's Slack app in Organisation settings → Chat. Nothing was posted

## unknown-channel

there is no channel #nowhere that dude's Slack bot can see: check the name, or for a private channel ask someone in it to run /invite @dude, then call again

## not-in-channel

dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude, then call again. Nothing was posted

## lookup-cut

could not finish looking up the channel #team-59; pass the channel id (C0123ABCD): the workspace has more channels than one lookup reads. Nothing was posted

## slack-busy

Slack did not take the post (slack chat.postMessage answered 503): nothing was posted; try again in a minute

## slack-refused

Slack refused the post: slack chat.postMessage: msg_too_long. Nothing was posted

