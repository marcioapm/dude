/**
 * Who to ask for a review, picked from what GitHub offers: its suggestions
 * for the pull request before any words, people and teams by name after.
 * Several can be picked; they gather above the field. Used to ask on a pull
 * request, and to name who every pull request asks (no pull request: a
 * search of the organization's repository).
 */

import { useCallback, useMemo } from "react";
import { GitHubUserLine, RemovableList, SearchPicker } from "@dude/design-system/components";
import { Icon, plural } from "@dude/design-system";
import type { ApiClient, ReviewerCandidate } from "../api/client.ts";

const SUGGESTED = "Suggested by GitHub";
const REASON: Record<NonNullable<ReviewerCandidate["reason"]>, string> = {
  changed: "Changed these files recently",
  commented: "Commented on this pull request",
};

// GitHub logins are one person whatever their case: a saved "Ana" is GitHub's "ana".
const loginKey = (login: string) => login.toLowerCase();

const user = (c: ReviewerCandidate) => ({ login: c.login, name: c.name, avatarUrl: c.avatarUrl, team: c.kind === "team" });

export function ReviewerPicker({ client, pullRequestId, asked, picked, onChange, onSubmit, onCancel, autoFocus, size = "sm" }: {
  client: ApiClient;
  /** The pull request to suggest for; null for a search alone. */
  pullRequestId: string | null;
  /** Logins asked already and yet to answer: shown, not picked. */
  asked?: ReadonlySet<string>;
  picked: ReadonlyArray<ReviewerCandidate>;
  onChange: (picked: ReviewerCandidate[]) => void;
  onSubmit?: () => void;
  onCancel?: () => void;
  autoFocus?: boolean;
  size?: "sm" | "md";
}) {
  const find = useCallback((q: string) => client.reviewerCandidates(pullRequestId, q), [client, pullRequestId]);
  const exclude = useMemo(() => new Set(picked.map((p) => loginKey(p.login))), [picked]);
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
        exclude={exclude}
        optionKey={(c) => loginKey(c.login)}
        group={(c) => (c.reason ? SUGGESTED : c.kind === "team" ? "Teams" : "People")}
        renderGroup={(g) => <>{g === SUGGESTED ? <Icon name="github" size={12} /> : null}{g}</>}
        renderOption={(c) => (
          <GitHubUserLine user={user(c)} detail={c.reason ? REASON[c.reason] : c.members ? plural(c.members, "member") : undefined} />
        )}
        optionDisabled={(c) => (asked?.has(loginKey(c.login)) ? "Already asked" : null)}
        empty={(q) => `Nobody who can review matches “${q}”.`}
        onPick={(c) => onChange([...picked, c])}
        onBackspaceEmpty={() => onChange(picked.slice(0, -1))}
        onSubmit={onSubmit}
        onCancel={onCancel}
      />
    </>
  );
}
