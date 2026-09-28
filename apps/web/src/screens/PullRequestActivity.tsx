/**
 * What happened to a task's pull requests, newest first, by name: dude
 * opening them, reviews and comments on GitHub, CI by the check's name, a
 * person's push, conflicts, and what a person did from here.
 *
 * Read from the ledger (the task's `pull_request.*` events), re-read when
 * the task's page reloads. A person who acted through dude is named from
 * the organization's people; one who acted on GitHub, by their login.
 */

import { useEffect, useMemo, useState } from "react";
import type { PersistedEvent } from "@dude/domain";
import { AgentAvatar, HumanAvatar } from "@dude/design-system/components";
import { formatDuration } from "@dude/design-system";
import { Section } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { pullRequestActivity } from "../pullRequests.ts";

export function PullRequestActivitySection({ client, taskId, named, version }: {
  client: ApiClient;
  taskId: string;
  /** More than one pull request: each is named by its repository. */
  named: boolean;
  /** Bumped when the page reloads. */
  version: number;
}) {
  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const [people, setPeople] = useState<Map<string, string>>(new Map());

  useEffect(() => {
    void client.listPeople().then((p) => setPeople(new Map(p.people.map((x) => [x.id, x.name]))), () => undefined);
  }, [client]);

  useEffect(() => {
    let live = true;
    void (async () => {
      const out: PersistedEvent[] = [];
      let after = 0;
      for (let page = 0; page < 20; page++) {
        const { events: batch, nextCursor } = await client.events({ taskId, after, limit: 1000 });
        out.push(...batch.filter((e) => e.eventType.startsWith("pull_request.")));
        if (batch.length < 1000) break;
        after = nextCursor;
      }
      if (live) setEvents(out);
    })().catch(() => undefined);
    return () => {
      live = false;
    };
  }, [client, taskId, version]);

  const lines = useMemo(
    () => events.flatMap((e) => {
      const line = pullRequestActivity(e, named);
      return line ? [{ ...line, id: e.eventId, at: e.occurredAt }] : [];
    }).reverse(),
    [events, named],
  );
  if (lines.length === 0) return null;
  const now = Date.now();
  return (
    <Section title="Pull request activity">
      <ul className="activity" data-testid="pr-activity">
        {lines.map((l) => {
          const name = l.actorId ? (people.get(l.actorId) ?? "Someone") : l.who;
          return (
            <li key={l.id} data-testid="pr-activity-item">
              {name ? <HumanAvatar person={{ id: l.actorId ?? name, name }} size="md" /> : <AgentAvatar role="integration" size="md" />}
              <div className="activityBody">
                {l.actorId ? <><b>{name}</b> {l.text}</> : l.text}
                {l.quote ? <blockquote>{l.quote}</blockquote> : null}
              </div>
              <span className="activityWhen ds-tnum">{formatDuration(Math.max(0, now - Date.parse(l.at)))} ago</span>
            </li>
          );
        })}
      </ul>
    </Section>
  );
}
