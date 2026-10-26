/**
 * An automation agent's tools, as its page lists them (the DS AgentTools),
 * and a run's calls to them, as its log shows them (ToolCallRow).
 */

import { AgentTools, ToolCallCard, type AgentTool, type AgentToolUnavailable } from "@dude/design-system/components";
import { formatTimestamp, Icon } from "@dude/design-system";
import { Card } from "@dude/design-system/primitives";
import type { AutomationToolCall } from "@dude/domain";
import { formatPlace } from "../place.ts";

/** What an automation's agent can call, besides the shell in its machine (orchestrator agenttools, Caller.tools). */
function automationTools(orgName: string): AgentTool[] {
  // The orchestrator's list, in its order: orchestrator/internal/delivery/agents/testdata/tool-lists-automation.golden.md.
  return [
    { name: "post_to_chat", fresh: true,
      what: `Posts a message (Slack mrkdwn) to a channel through ${orgName}'s Slack installation: a channel the dude bot is in; invite it with /invite @dude.` },
    { name: "create_task", what: "Files a task in a project it works in; a person decides when it starts." },
    { name: "github_comment", what: "Comments on an issue or pull request as the dude App, 20 a run at most." },
    { name: "github_search", what: "Searches issues and pull requests, 30 results at most." },
    { name: "github_pull_request", what: "Reads one pull request: description, files, reviews." },
    { name: "github_issue", what: "Reads one issue and its comments." },
    { name: "github_check_log", what: "Reads a failed check's log." },
  ];
}

/** Why post_to_chat cannot post here, when the organisation has no Slack installation. */
function slackMissing(orgName: string): Record<string, AgentToolUnavailable> {
  return { post_to_chat: { why: `Slack is not connected for ${orgName}`,
    fix: { label: "Connect Slack in Organisation settings → Chat", href: formatPlace({ view: "orgSettings", page: "chat" }) } } };
}

/** The Tools row's card: every tool, post_to_chat flagged when Slack is missing (null while unknown). */
export function AutomationToolList({ orgName, slackInstalled }: { orgName: string; slackInstalled: boolean | null }) {
  return (
    <Card variant="raised" className="automationToolsCard">
      <AgentTools tools={automationTools(orgName)} unavailable={slackInstalled === false ? slackMissing(orgName) : undefined} />
    </Card>
  );
}

/** One call in a run's log: its time in mono, then the call's card; a post links its message in Slack. */
function ToolCallRow({ call }: { call: AutomationToolCall }) {
  const post = call.tool === "post_to_chat";
  const channel = typeof call.args["channel"] === "string" ? (call.args["channel"] as string) : null;
  const { posted } = call;
  return (
    <div className="automationToolCall" data-testid="tool-call" data-tool={call.tool}>
      <time className="automationToolCallAt ds-mono ds-hint" dateTime={call.at}>{formatTimestamp(call.at, "time")}</time>
      <ToolCallCard name={call.tool} status={call.status} durationMs={call.durationMs ?? undefined}
        {...(post ? { icon: "slack" as const, summary: <span className="ds-mono">{posted?.channel ?? channel ?? ""}</span> } : { args: call.args })}
        {...(posted ? { result: JSON.stringify(posted, null, 2) } : {})}
        {...(call.error ? { error: call.error } : {})}
        note={posted ? (
          <p className="automationToolCallPosted ds-hint" data-testid="posted-link">
            <Icon name="slack" size={12} /> Posted to <b>{posted.channel}</b> ·{" "}
            <a href={posted.permalink} target="_blank" rel="noreferrer">Open the message in Slack</a>
          </p>
        ) : undefined} />
    </div>
  );
}

/** A run's calls, oldest first. */
export function ToolCallList({ calls }: { calls: readonly AutomationToolCall[] }) {
  return (
    <ol className="automationToolCalls" aria-label="Tool calls">
      {calls.map((c) => <li key={c.id}><ToolCallRow call={c} /></li>)}
    </ol>
  );
}
