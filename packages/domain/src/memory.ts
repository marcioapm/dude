/**
 * Memory, as the orchestrator serves it (orchestrator/internal/memory) and
 * the settings pages read it: memories, one search over them and the work,
 * and the index behind it (docs/design/memory.md).
 */

export const MEMORY_KINDS = ["fact", "procedure", "note"] as const;
export type MemoryKind = (typeof MEMORY_KINDS)[number];

export const SEARCH_TYPES = ["memory", "task", "epic", "project"] as const;
export type SearchType = (typeof SEARCH_TYPES)[number];

/** What a memory is about, or where it was learned; labelled when read. */
export interface MemoryRef {
  readonly type: "task" | "epic" | "project" | "run";
  readonly id: string;
  /** A task's key (TEXT-12), an epic's title, a project's name. */
  readonly label?: string | undefined;
  /** A task's status. */
  readonly status?: string | undefined;
}

export interface MemoryAuthor {
  readonly kind: "person" | "system" | "agent";
  /** Who wrote it; for an agent, the person it worked for. */
  readonly personId?: string;
  readonly personName?: string;
  /** An agent's role. */
  readonly role?: string;
  readonly runId?: string;
  /** The task an agent was on. */
  readonly taskKey?: string;
  /** Why dude wrote it: "from an answer". */
  readonly reason?: string;
}

export interface Memory {
  readonly id: string;
  /** Absent: the whole organization's. */
  readonly projectId?: string;
  readonly title: string;
  readonly content: string;
  readonly kind: MemoryKind;
  readonly author: MemoryAuthor;
  readonly source?: MemoryRef;
  readonly about: readonly MemoryRef[];
  readonly archivedAt?: string;
  readonly createdAt: string;
  readonly updatedAt: string;
  /** Searchable by meaning, by words only for now, or failed (indexNote says why). */
  readonly index: "embedded" | "waiting" | "failed" | "archived";
  readonly indexNote?: string;
}

export interface MemoryInput {
  /** Null or "": the whole organization. */
  readonly projectId?: string | null;
  readonly title?: string;
  readonly content?: string;
  readonly kind?: MemoryKind;
  readonly about?: readonly Pick<MemoryRef, "type" | "id">[];
}

export interface SearchResult {
  readonly type: SearchType;
  readonly id: string;
  readonly projectId?: string;
  /** A task's key and status. */
  readonly key?: string;
  readonly status?: string;
  readonly title: string;
  /** Matched words are between ⟦ and ⟧. */
  readonly snippet: string;
  /** 1-based; 0 when that search did not find it. */
  readonly textRank: number;
  readonly textScore?: number;
  readonly vectorRank: number;
  readonly distance?: number;
  /** Reciprocal-rank fusion: Σ 1 / (60 + rank). */
  readonly score: number;
  readonly embedded: boolean;
}

export interface SearchOutcome {
  readonly results: readonly SearchResult[];
  /** "words" when meaning was not searched. */
  readonly mode: "hybrid" | "words";
  readonly model?: string;
  /** Why meaning was not searched, when it was meant to be. */
  readonly degraded?: string;
}

export interface IndexStatus {
  /** Absent: no embedder, search is by words. */
  readonly model?: string;
  readonly dimensions?: number;
  readonly endpoint?: string;
  /** The embedder as the indexer last found it. */
  readonly health: { readonly error?: string; readonly since?: string; readonly retry?: string };
  /** Documents that failed on their own, all of them (failures lists the first 50). */
  readonly failed: number;
  readonly kinds: readonly {
    readonly type: SearchType;
    readonly total: number;
    readonly embedded: number;
    readonly waiting: number;
    readonly failed: number;
  }[];
  readonly failures: readonly {
    readonly type: SearchType;
    readonly id: string;
    readonly title: string;
    readonly error: string;
    readonly attempts: number;
    /** When embedding it last failed (ISO 8601). */
    readonly lastTry: string;
  }[];
}
