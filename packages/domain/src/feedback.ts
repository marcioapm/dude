/**
 * A person's feedback on a task (migration 127): items drafted privately,
 * then submitted together as a batch. Kept in dude only.
 *
 * The client names where an item points; the backend builds the anchor
 * kept with it. A code item names a Run, a file of that Run's stored diff,
 * a side and a line range; the excerpt, checksum, base and head are read
 * from the snapshot, never taken from the client. A preview item's page
 * metadata is stored as given, capped here.
 */

import { z } from "zod";

export const FEEDBACK_LIMITS = {
  body: 16_000,
  /** Lines one code item may span. */
  lines: 200,
  previewText: 4_000,
  previewHtml: 16_000,
  /** One person's drafts on one task. */
  drafts: 50,
  /** Items in one submit. */
  batch: 50,
  submissionKey: 200,
} as const;

export const FEEDBACK_STATUSES = ["draft", "open", "delivered", "resolved", "replied"] as const;
export type FeedbackStatus = (typeof FEEDBACK_STATUSES)[number];

/** Blocks the pull request and dude's merge until a person or agent handles it. */
export const FEEDBACK_BLOCKING: readonly FeedbackStatus[] = ["open", "delivered"];

const line = z.number().int().positive().max(1_000_000);

const generalAnchorInput = z.object({ kind: z.literal("general") }).strict();

const codeAnchorInput = z.object({
  kind: z.literal("code"),
  runId: z.string().min(1).max(100),
  /** The file as the Run's diff names it (repository-prefixed with several). */
  path: z.string().min(1).max(1_000),
  side: z.enum(["old", "new"]),
  startLine: line,
  endLine: line,
  /** The snapshot the person was looking at: refused if the stored one has moved on. */
  diffChecksum: z.string().min(1).max(200).optional(),
}).strict().refine((a) => a.startLine <= a.endLine, { message: "startLine must not be after endLine", path: ["endLine"] })
  .refine((a) => a.endLine - a.startLine < FEEDBACK_LIMITS.lines,
    { message: `a code item spans at most ${FEEDBACK_LIMITS.lines} lines`, path: ["endLine"] });

const previewAnchorInput = z.object({
  kind: z.literal("preview"),
  url: z.string().url().max(2_048).refine((u) => /^https?:\/\//.test(u), "an http(s) URL"),
  viewport: z.object({ width: z.number().int().positive().max(20_000), height: z.number().int().positive().max(20_000) }).strict().optional(),
  selector: z.string().max(2_000).optional(),
  rect: z.object({ x: z.number().finite(), y: z.number().finite(), width: z.number().finite().nonnegative(),
    height: z.number().finite().nonnegative() }).strict().optional(),
  text: z.string().max(FEEDBACK_LIMITS.previewText).optional(),
  html: z.string().max(FEEDBACK_LIMITS.previewHtml).optional(),
  component: z.object({ name: z.string().max(200).optional(), file: z.string().max(1_000).optional(),
    line: line.optional() }).strict().optional(),
  /** An image of the task's the same person uploaded. */
  attachmentId: z.string().min(1).max(100).optional(),
}).strict();

export const feedbackAnchorInputSchema = z.union([generalAnchorInput, codeAnchorInput, previewAnchorInput]);
export type FeedbackAnchorInput = z.infer<typeof feedbackAnchorInputSchema>;

export const feedbackDraftSchema = z.object({
  body: z.string().trim().min(1).max(FEEDBACK_LIMITS.body),
  anchor: feedbackAnchorInputSchema,
}).strict();
export type FeedbackDraftInput = z.infer<typeof feedbackDraftSchema>;

export const feedbackSubmitSchema = z.object({
  draftIds: z.array(z.string().min(1).max(100)).min(1).max(FEEDBACK_LIMITS.batch)
    .refine((ids) => new Set(ids).size === ids.length, "each draft once"),
  submissionKey: z.string().trim().min(1).max(FEEDBACK_LIMITS.submissionKey),
}).strict();
export type FeedbackSubmitInput = z.infer<typeof feedbackSubmitSchema>;

export const feedbackReplySchema = z.object({ body: z.string().trim().min(1).max(FEEDBACK_LIMITS.body) }).strict();
export const feedbackResolveSchema = z.object({ note: z.string().trim().max(2_000).optional() }).strict();
export const feedbackReopenSchema = z.object({ body: z.string().trim().max(FEEDBACK_LIMITS.body).optional() }).strict();

/** One line of the excerpt a code anchor keeps: the Run diff's own line. */
export interface FeedbackExcerptLine {
  kind: " " | "+" | "-";
  old: number | null;
  new: number | null;
  text: string;
}

/** A code anchor, as kept. `headSha` null: the lines were uncommitted work of a Run not published. */
export interface FeedbackCodeAnchor {
  kind: "code";
  runId: string;
  repo: string;
  path: string;
  side: "old" | "new";
  startLine: number;
  endLine: number;
  baseSha: string;
  headSha: string | null;
  diffChecksum: string;
  excerpt: FeedbackExcerptLine[];
}

export type FeedbackPreviewAnchor = Extract<FeedbackAnchorInput, { kind: "preview" }>;
export type FeedbackAnchor = { kind: "general" } | FeedbackCodeAnchor | FeedbackPreviewAnchor;

export interface FeedbackHistoryEntry {
  id: string;
  kind: "reply" | "resolve" | "reopen";
  body: string;
  commit: string | null;
  personId: string | null;
  runId: string | null;
  createdAt: string;
}

/** An item as `GET /v1/tasks/:id/feedback` lists it. */
export interface FeedbackItem {
  id: string;
  taskId: string;
  attempt: number;
  batchId: string | null;
  author: { id: string; name: string } | null;
  body: string;
  anchor: FeedbackAnchor;
  status: FeedbackStatus;
  deliveredRunId: string | null;
  resolvedCommit: string | null;
  /** A commit resolution waits for its commit to be published: still blocking. */
  resolutionPending: boolean;
  outdated: boolean;
  routedTo: "conductor" | "steer" | "workflow" | null;
  submittedAt: string | null;
  createdAt: string;
  updatedAt: string;
  history: FeedbackHistoryEntry[];
}

export interface FeedbackList {
  feedback: FeedbackItem[];
  /** Submitted items of the attempt that block the gates. */
  blocking: number;
}
