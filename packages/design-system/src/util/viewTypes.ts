/**
 * Shapes the view models read, kept apart from the components that render
 * them so pure functions (navModel.ts) import no React and can run on the
 * server: the backend counts what waits on someone with the page's own rule.
 */

import type { PrDisplayInput, PrDisplayState } from "@dude/domain";

/** Anyone with a face: a name, and a photo when there is one. */
export interface Person {
  /** Stable identity for colour and keys; falls back to `name`. */
  readonly id?: string | undefined;
  readonly name: string;
  /** A photo. Without one, initials on the person's identity colour. */
  readonly photoUrl?: string | null | undefined;
  /** Acted in the last five minutes. Unknown (undefined) draws no ring. */
  readonly online?: boolean | undefined;
}

/** A pull request as the chip reads it: the display state's inputs, where it is, and what it is called. */
export interface PrChipPullRequest extends PrDisplayInput {
  readonly number: number;
  readonly url: string;
  /** "acme/dashboard", or the repository's name; in the tooltip. */
  readonly repositoryName?: string | undefined;
  readonly baseBranch?: string | undefined;
  readonly behindBy?: number | null | undefined;
  /** The state, when the API computed it (`display`); otherwise it is computed here. */
  readonly display?: PrDisplayState | undefined;
}
