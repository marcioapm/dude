/**
 * An automation agent's tools, as its page lists them (the DS AgentTools).
 * A run's calls to them are in its activity feed (automationFeed.tsx).
 */

import { AgentTools, type AgentTool, type AgentToolUnavailable } from "@dude/design-system/components";
import { Card } from "@dude/design-system/primitives";
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
