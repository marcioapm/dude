import { ApiClient, ApiError, type MeAnswer } from "./api/client.ts";

export const KEY_STORAGE = "dude.apiKey";
export const ACCESS_LOGOUT = "/cdn-cgi/access/logout";

export type AuthMethod = "api_key" | "cloudflare_access";

export type AuthState =
  | { kind: "checking" }
  // `me` is the gate's answer for this credential, before any organisation is named.
  | { kind: "authenticated"; client: ApiClient; authMethod: AuthMethod; me: MeAnswer }
  | { kind: "key-prompt"; refused: boolean }
  | { kind: "network-error" };

/** How a credential signed in, from its `/v1/me`: a key is always a member; anyone else came through Access. */
export function authMethodOf(me: MeAnswer): AuthMethod {
  if ("authMethod" in me && me.authMethod) return me.authMethod;
  return me.gate && me.gate !== "member" ? "cloudflare_access" : "api_key";
}

export function authRefused(cause: unknown): boolean {
  return cause instanceof ApiError && (cause.status === 401 || cause.status === 403);
}

export class AuthSession {
  #state: AuthState = { kind: "checking" };
  #listeners = new Set<() => void>();
  #pending: Promise<void> | undefined;
  #refused = false;
  #reprobed = false;
  #signedOut = false;

  constructor(
    private readonly storage: Pick<Storage, "getItem" | "removeItem">,
    private readonly makeClient = (apiKey?: string) => new ApiClient({ apiKey }),
  ) {}

  snapshot = (): AuthState => this.#state;
  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  };

  #set(state: AuthState): void {
    this.#state = state;
    for (const listener of this.#listeners) listener();
  }

  check = (): Promise<void> => {
    if (this.#signedOut || this.#state.kind === "authenticated" || this.#state.kind === "key-prompt") return Promise.resolve();
    if (this.#pending) return this.#pending;
    this.#set({ kind: "checking" });
    this.#pending = this.#check().finally(() => { this.#pending = undefined; });
    return this.#pending;
  };

  async #check(): Promise<void> {
    const key = this.storage.getItem(KEY_STORAGE) || undefined;
    let client = this.makeClient(key);
    try {
      let me: MeAnswer;
      try {
        me = await client.gate();
      } catch (cause) {
        if (!key || !authRefused(cause)) throw cause;
        this.storage.removeItem(KEY_STORAGE);
        this.#refused = true;
        client = this.makeClient();
        me = await client.gate();
      }
      if (!this.#signedOut) this.#set({ kind: "authenticated", client, authMethod: authMethodOf(me), me });
    } catch (cause) {
      if (!this.#signedOut) this.#set(authRefused(cause)
        ? { kind: "key-prompt", refused: this.#refused }
        : { kind: "network-error" });
    }
  }

  // `client` is the one whose request was refused: a refusal still in flight from a
  // client this session has already replaced says nothing about the current one. An
  // organisation's client (`inOrganization`) reports as the session's own.
  refused = (given: ApiClient): void => {
    const client = given.root;
    if (this.#signedOut || this.#state.kind !== "authenticated" || this.#state.client !== client) return;
    this.storage.removeItem(KEY_STORAGE);
    // A session accepted by /v1/me but refused elsewhere gets one reprobe, not a loop.
    if (this.#reprobed) {
      this.#set({ kind: "key-prompt", refused: true });
      return;
    }
    this.#reprobed = true;
    this.#refused = true;
    this.#set({ kind: "checking" });
    void this.check();
  };

  /** Read the gate's answer again for the signed-in credential (after joining, or Check again). */
  async reread(): Promise<MeAnswer | null> {
    if (this.#signedOut || this.#state.kind !== "authenticated") return null;
    const { client } = this.#state;
    const me = await client.gate();
    if (!this.#signedOut && this.#state.kind === "authenticated" && this.#state.client === client) {
      this.#set({ ...this.#state, me });
    }
    return me;
  }

  async signOut(
    unsubscribe: (client: ApiClient) => Promise<unknown>,
    assign: (url: string) => void,
    unmount: (update: () => void) => void = (update) => update(),
  ): Promise<void> {
    if (this.#signedOut || this.#state.kind !== "authenticated") return;
    this.#signedOut = true;
    const { client, authMethod } = this.#state;
    try {
      await unsubscribe(client);
    } catch {
      // Local sign-out must still finish when push cleanup fails.
    }
    this.storage.removeItem(KEY_STORAGE);
    if (authMethod === "cloudflare_access") {
      unmount(() => this.#set({ kind: "checking" }));
      assign(ACCESS_LOGOUT);
    } else {
      this.#set({ kind: "key-prompt", refused: false });
    }
  }
}
