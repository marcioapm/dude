/**
 * What the tester showed: the latest test's verdict on each acceptance
 * criterion, under the commits it tested and a stale mark once the branch
 * has moved past them. Each piece of evidence opens the file that test
 * published in the artifact viewer — that test's version of it, not a
 * later one of the same name. A waiver or acceptance says who and why.
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
  const files = useMemo(() => filesOf(artifacts.filter((a) => a.runId === latest?.runId)), [artifacts, latest?.runId]);
  const [open, setOpen] = useState<string | null>(null);
  if (!latest) return null;
  const published = new Set(files.map((f) => f.name));
  return (
    <div className="taskVerification">
      <VerificationGroup data-testid="verification" data-run={latest.runId} heads={latest.heads} stale={latest.stale}
        actions={
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(latest.runId)} data-testid="verification-run">
            Open the test
          </Button>
        }>
        {latest.criteria.map((c) => (
          <CriterionRow key={c.criterion} data-testid="criterion" number={c.criterion} text={c.text} status={c.status}
            decision={c.decision} decidedBy={c.decidedBy?.name} decisionNote={c.decisionNote} observed={c.observed || undefined}
            problem={c.problem || undefined}
            evidence={c.evidence.length > 0 ? c.evidence.map((name) => published.has(name) ? (
              <button key={name} type="button" className="linkButton" data-testid="evidence" onClick={() => setOpen(name)}>{name}</button>
            ) : (
              <span key={name} className="evidenceMissing" data-testid="evidence-missing" title="Not a file this test published">{name}</span>
            )) : undefined} />
        ))}
      </VerificationGroup>
      <ArtifactViewer client={client} files={files} open={open} onOpenChange={setOpen} onOpenSession={() => onOpenRun(latest.runId)} />
    </div>
  );
}
