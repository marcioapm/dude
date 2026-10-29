import { z } from "zod";
import { FEEDBACK_LIMITS, previewAnchorInput } from "./feedback.ts";

export const previewFeedbackSubmitSchema = z.object({
  body: z.string().trim().min(1).max(FEEDBACK_LIMITS.body),
  anchor: previewAnchorInput,
  submissionKey: z.string().trim().min(1).max(FEEDBACK_LIMITS.submissionKey),
  captureSha: z.string().regex(/^[0-9a-f]{40,64}$/).optional(),
}).strict();

export const previewFeedbackStatusSchema = z.object({ note: z.string().trim().max(2_000).optional() }).strict();

export interface PreviewFeedbackItem {
  id: string;
  projectId: string;
  previewRunId: string;
  serverName: string;
  author: { id: string; name: string };
  body: string;
  anchor: z.infer<typeof previewAnchorInput>;
  captureSha: string | null;
  status: "open" | "resolved";
  createdAt: string;
  resolvedAt: string | null;
  resolvedBy: string | null;
  resolutionNote: string;
  history: Array<{ status: "open" | "resolved"; personId: string; note: string; at: string }>;
}

export interface PreviewFeedbackList {
  feedback: PreviewFeedbackItem[];
  nextCursor: string | null;
}
