/**
 * The backend's one background loop: attachment storage upkeep.
 *
 * Images uploaded and never sent are removed a day later, and every object
 * whose attachment row is gone — swept, removed by a person, or taken with
 * its task (ON DELETE CASCADE) — is deleted from storage. The rows' trigger
 * queues the keys (migration 070); this drains the queue. A key whose
 * delete fails stays queued for the next pass.
 *
 * It looks across organizations, so it runs as dude_sweeper, which may see
 * attachments and the queue and nothing else of a tenant.
 */

import { withoutTenant } from "./db/client.ts";
import { removeObject, storageConfigured } from "./storage.ts";

/** How long an upload may wait to be sent. */
export const UNATTACHED_TTL_HOURS = 24;
/** Queued keys one pass deletes at a time. */
export const SWEEP_BATCH = 200;
/** How long one pass drains the queue before leaving the rest to the next. */
export const DRAIN_BUDGET_MS = 60_000;

export interface SweepResult {
  /** Unsent uploads removed. */
  expired: number;
  /** Objects deleted from storage. */
  deleted: number;
  /** Objects that could not be deleted, left queued. */
  failed: number;
}

export async function sweepAttachments(now: Date = new Date(), budgetMs = DRAIN_BUDGET_MS): Promise<SweepResult> {
  const cutoff = new Date(now.getTime() - UNATTACHED_TTL_HOURS * 3600_000);
  const expired = await withoutTenant(async ({ sql }) => {
    await sql`SET LOCAL ROLE dude_sweeper`;
    const rows = await sql`DELETE FROM attachments WHERE attached_at IS NULL AND created_at < ${cutoff} RETURNING id`;
    return rows.length as number;
  });
  let deleted = 0;
  let failed = 0;
  // Without a bucket nothing was ever stored; the queue waits for one.
  if (!storageConfigured()) return { expired, deleted, failed };
  // Batch after batch until the queue is empty, the pass's time is spent,
  // or a whole batch fails. A key that failed is skipped for the rest of
  // the pass, so it cannot be taken again and again ahead of the others.
  const deadline = Date.now() + budgetMs;
  const skip: string[] = [];
  while (Date.now() < deadline) {
    const keys = await withoutTenant(async ({ sql }) => {
      await sql`SET LOCAL ROLE dude_sweeper`;
      return (await sql`SELECT object_key FROM attachment_object_deletions WHERE NOT (object_key = ANY(${sql.array(skip, "TEXT")}))
        ORDER BY queued_at LIMIT ${SWEEP_BATCH}`) as Array<{ object_key: string }>;
    });
    if (keys.length === 0) break;
    const done: string[] = [];
    for (const { object_key: key } of keys) {
      try {
        await removeObject(key);
        done.push(key);
      } catch (err) {
        failed++;
        skip.push(key);
        console.error(`attachments: could not delete an object: ${(err as Error).message}`);
      }
    }
    if (done.length > 0) {
      await withoutTenant(async ({ sql }) => {
        await sql`SET LOCAL ROLE dude_sweeper`;
        await sql`DELETE FROM attachment_object_deletions WHERE object_key = ANY(${sql.array(done, "TEXT")})`;
      });
    }
    deleted += done.length;
    // Storage refused every delete of a batch: it will refuse the next
    // too, and each later batch scans past a longer skip list.
    if (done.length === 0) break;
  }
  return { expired, deleted, failed };
}

/** Sweep every `everyMs` until stopped; a failed pass is logged and the next one tries again. */
export function startAttachmentSweeper(everyMs = 10 * 60_000): () => void {
  let running = false;
  const pass = async () => {
    if (running) return;
    running = true;
    try {
      await sweepAttachments();
    } catch (err) {
      console.error(`attachments: sweep failed: ${(err as Error).message}`);
    } finally {
      running = false;
    }
  };
  const timer = setInterval(() => void pass(), everyMs);
  void pass();
  return () => clearInterval(timer);
}
