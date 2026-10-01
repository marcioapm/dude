/**
 * Who to ask for a review, picked from what GitHub offers: its suggestions
 * for the pull request before any words, people and teams by name after.
 * Several can be picked; they gather above the field. Used to ask on a pull
 * request, and to name who every pull request asks (no pull request: a
 * search of the organization's repository).
 */

import { useCallback } from "react";
import { GitHubUserLine, RemovableList, SearchPicker } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import type { ApiClient, ReviewerCandidate } from "../api/client.ts";

const SUGGESTED = "Suggested by GitHub";
const REASON: Record<NonNullable<ReviewerCandidate["reason"]>, string> = {
  changed: "Changed these files recently",
  commented: "Commented on this pull request",
};

export function ReviewerPicker({ client, pullRequestId, picked, onChange, onSubmit, onCancel, autoFocus, size = "sm" }: {
  client: ApiClient;
  /** The pull request to suggest for; null for a search alone. */
  pullRequestId: string | null;
  picked: ReadonlyArray<ReviewerCandidate>;
  onChange: (picked: ReviewerCandidate[]) => void;
  onSubmit?: () => void;
  onCancel?: () => void;
  autoFocus?: boolean;
  size?: "sm" | "md";
}) {
  // Who is picked is not offered again.
  const key = picked.map((p) => p.login.toLowerCase()).join(" ");
  const find = useCallback(async (q: string) => {
    const logins = new Set(key.split(" "));
    return (await client.reviewerCandidates(pullRequestId, q)).filter((c) => !logins.has(c.login.toLowerCase()));
  }, [client, pullRequestId, key]);
  const user = (c: ReviewerCandidate) => ({ login: c.login, name: c.name, avatarUrl: c.avatarUrl, team: c.kind === "team" });
  return (
    <>
      {picked.length > 0 ? (
        <RemovableList data-testid="reviewer-picks" onRemove={(login) => onChange(picked.filter((p) => p.login !== login))}
          items={picked.map((c) => ({ id: c.login, label: c.login, content: <GitHubUserLine user={user(c)} size={20} inline /> }))} />
      ) : null}
      <SearchPicker<ReviewerCandidate>
        label="Who to ask for a review"
        placeholder={picked.length ? "Anyone else?" : "Name or GitHub login"}
        size={size}
        autoFocus={autoFocus}
        findOnEmpty={pullRequestId !== null}
        clearOnPick
        find={find}
        optionKey={(c) => c.login}
        group={(c) => (c.reason ? SUGGESTED : c.kind === "team" ? "Teams" : "People")}
        renderGroup={(g) => <>{g === SUGGESTED ? <Icon name="github" size={12} /> : null}{g}</>}
        renderOption={(c) => (
          <GitHubUserLine user={user(c)}
            detail={c.reason ? REASON[c.reason] : c.members ? `${c.members} ${c.members === 1 ? "member" : "members"}` : undefined} />
        )}
        optionDisabled={(c) => (c.requested ? "Already asked" : null)}
        empty={(q) => `Nobody who can review matches “${q}”.`}
        onPick={(c) => onChange([...picked, c])}
        onBackspaceEmpty={() => onChange(picked.slice(0, -1))}
        onSubmit={onSubmit}
        onCancel={onCancel}
      />
    </>
  );
}
