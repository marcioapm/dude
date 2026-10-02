/**
 * Images a person sends an agent with a steer, an answer or a task's
 * prompt: the limits the browser, the backend and the orchestrator share.
 * lux's own (feat/input-attachments): at most 10 per input, 5 MiB each
 * decoded, the whole JSON body under 8 MiB — so a message's images stay
 * under ~5.5 MiB in total.
 */

export const ATTACHMENT_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"] as const;
export type AttachmentType = (typeof ATTACHMENT_TYPES)[number];

export const ATTACHMENT_LIMITS = {
  /** Images in one message. */
  perMessage: 6,
  /** An image as picked, before the browser scales it. */
  originalBytes: 10 * 1000 * 1000,
  /** What the agent is sent: one image. */
  deliveredBytes: Math.floor(4.5 * 1024 * 1024),
  /** What the agent is sent: one message's images together. */
  messageBytes: 5 * 1024 * 1024,
  /** The long side of what the agent is sent, in pixels. */
  maxSide: 2000,
} as const;

/** An attachment as the API returns it and events carry it. */
export interface AttachmentInfo {
  id: string;
  name: string;
  /** What the agent got. */
  contentType: AttachmentType;
  width: number;
  height: number;
  bytes: number;
  /** The image as picked. */
  original: { contentType: AttachmentType; width: number; height: number; bytes: number };
}
