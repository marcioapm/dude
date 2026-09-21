/**
 * Execution-plane interfaces: workers, runtimes, workspaces, artifacts.
 * Plans §46 (worker pools), §51 (worker node model), §61 (workspaces),
 * §66 (artifact upload path).
 */

// ---------------------------------------------------------------------------
// Worker / placement
// ---------------------------------------------------------------------------

export interface WorkerCapacity {
  cpuMillis: number;
  memoryMb: number;
  maxRuns: number;
  activeRuns: number;
}

export type WorkerStatus = "registering" | "ready" | "draining" | "lost";

export interface Worker {
  id: string;
  organizationId: string | null;
  name: string;
  /** "local" for the local provisioner; a pool name otherwise. */
  pool: string;
  status: WorkerStatus;
  capacity: WorkerCapacity;
  /** Runtime images already pulled on this node. */
  cachedImages: string[];
  /** Repositories already mirrored on this node (affinity hint, §61). */
  cachedRepositories: string[];
  lastHeartbeatAt: string;
  registeredAt: string;
}

/**
 * NodeProvisioner acquires and releases worker *nodes*. Distinct from run
 * placement, which the scheduler does — plan §60 warns against conflating
 * these two responsibilities under one "Provisioner" name.
 */
export interface NodeProvisioner {
  readonly name: string;
  ensureCapacity(pool: string, desired: number): Promise<void>;
  release(workerId: string): Promise<void>;
  list(pool: string): Promise<Worker[]>;
}

// ---------------------------------------------------------------------------
// Runtime instance (one container)
// ---------------------------------------------------------------------------

export type RuntimeStatus =
  | "creating"
  | "starting"
  | "running"
  | "stopping"
  | "stopped"
  | "destroyed"
  | "failed";

export interface RuntimeInstance {
  id: string;
  organizationId: string;
  runId: string;
  workerId: string;
  containerId: string | null;
  imageDigest: string | null;
  /** Increments each time the container is replaced for the same Run. */
  generation: number;
  status: RuntimeStatus;
  createdAt: string;
  startedAt: string | null;
  stoppedAt: string | null;
  destroyedAt: string | null;
}

// ---------------------------------------------------------------------------
// Session Workspace
// ---------------------------------------------------------------------------

/**
 * The durable working directory for a Run. It lives on the node outside
 * the container's writable layer, so a dead container does not destroy work:
 *
 *   workspace/
 *     repos/<name>/
 *     agent-state/
 *     scratch/
 *     artifacts-staging/
 *     runtime-manifest.json
 */
export interface SessionWorkspace {
  runId: string;
  organizationId: string;
  /** Absolute path on the worker node. */
  hostPath: string;
  /** Mount point inside the runtime container. */
  containerPath: string;
  repos: Array<{ name: string; path: string; branch: string; headSha: string }>;
}

export const WORKSPACE_LAYOUT = {
  repos: "repos",
  agentState: "agent-state",
  scratch: "scratch",
  artifactsStaging: "artifacts-staging",
  manifest: "runtime-manifest.json",
} as const;

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

export interface Artifact {
  id: string;
  organizationId: string;
  runId: string | null;
  sessionId: string | null;
  kind: "markdown" | "diff" | "log" | "screenshot" | "video" | "trace" | "json" | "other";
  name: string;
  contentType: string;
  sizeBytes: number;
  /** Storage key; never a credentialed URL. */
  storageKey: string;
  sha256: string;
  createdAt: string;
}

/**
 * Agents never receive object-store credentials (plan §66). They stage a
 * file in artifacts-staging/ and ask the runner to publish it; the runner
 * holds the credential and performs the upload.
 */
export interface ArtifactStore {
  readonly name: string;
  put(params: {
    organizationId: string;
    storageKey: string;
    body: Uint8Array | ReadableStream;
    contentType: string;
  }): Promise<{ sizeBytes: number; sha256: string }>;
  get(organizationId: string, storageKey: string): Promise<Uint8Array>;
  /** Short-lived read URL for the UI; scoped to one object. */
  signedReadUrl(organizationId: string, storageKey: string, ttlSeconds: number): Promise<string>;
  delete(organizationId: string, storageKey: string): Promise<void>;
}
