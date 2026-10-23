/**
 * What the tester showed: the latest test's verdict on each acceptance
 * criterion, under the commits it tested and a stale mark once the branch
 * has moved past them — or, for a pull request's fix still being checked,
 * a mark that the pull request does not hold it yet. Each piece of
 * evidence opens the file that test published in the artifact viewer —
 * that test's version of it, not a later one of the same name; a criterion
 * carried from an earlier round opens that round's. A waiver or acceptance
 * says who and why.
 */

import { useMemo, useState } from "react";
import { Button } from "@dude/design-system/primitives";
import { CriterionRow, VerificationGroup } from "@dude/design-system/components";
import type { ApiClient, Artifact, Verification } from "../api/client.ts";
import { ArtifactViewer, filesOf } from "./FilesSection.tsx";

export function VerificationSection({ client, verifications, artifacts, onOpenRun }: {
  client: ApiClient;
  /** The attempt's tests, the latest first. */
  verifications: readonly Verification[];
  /** The task's published files: the evidence is looked up among them. */
  artifacts: readonly Artifact[];
  onOpenRun: (runId: string) => void;
}) {
  const latest = verifications[0];
  // The files of each Run the evidence is from: this test's, and each round a criterion was carried from.
  const filesByRun = useMemo(() => {
    const runs = new Set([latest?.runId, ...(latest?.criteria ?? []).map((c) => c.carriedFrom)].filter((r): r is string => !!r));
    return new Map([...runs].map((r) => [r, filesOf(artifacts.filter((a) => a.runId === r))]));
  }, [artifacts, latest]);
  const [open, setOpen] = useState<{ runId: string; name: string } | null>(null);
  if (!latest) return null;
  const viewing = open?.runId ?? latest.runId;
  return (
    <div className="taskVerification">
      <VerificationGroup data-testid="verification" data-run={latest.runId} heads={latest.heads} stale={latest.stale}
        candidate={latest.candidate}
        actions={
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(latest.runId)} data-testid="verification-run">
            Open the test
          </Button>
        }>
        {latest.criteria.map((c) => {
          const from = c.carriedFrom ?? latest.runId;
          const published = new Set((filesByRun.get(from) ?? []).map((f) => f.name));
          return (
            <CriterionRow key={c.criterion} data-testid="criterion" number={c.criterion} text={c.text} status={c.status}
              decision={c.decision} decidedBy={c.decidedBy?.name} decisionNote={c.decisionNote} observed={c.observed || undefined}
              problem={c.problem || undefined} carried={!!c.carriedFrom}
              evidence={c.evidence.length > 0 ? c.evidence.map((name) => published.has(name) ? (
                <button key={name} type="button" className="linkButton" data-testid="evidence" onClick={() => setOpen({ runId: from, name })}>{name}</button>
              ) : (
                <span key={name} className="evidenceMissing" data-testid="evidence-missing" title="Not a file this test published">{name}</span>
              )) : undefined} />
          );
        })}
      </VerificationGroup>
      <ArtifactViewer client={client} files={filesByRun.get(viewing) ?? []} open={open?.name ?? null}
        onOpenChange={(name) => setOpen(name ? { runId: viewing, name } : null)} onOpenSession={() => onOpenRun(viewing)} />
    </div>
  );
}
