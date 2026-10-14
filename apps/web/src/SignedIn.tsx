/**
 * The signed-in app as main.tsx mounts it: the gatekeeper, then the tenant
 * app for the organisation it chose. Sign-out removes this browser's push
 * through the mounted organisation's client from inside the app, and through
 * the session's own client from a front door, where none is mounted.
 */

import type { ApiClient, MeAnswer } from "./api/client.ts";
import { App } from "./App.tsx";
import { Gatekeeper } from "./accounts/Gatekeeper.tsx";
import type { AuthMethod } from "./auth.ts";
import { PeopleProvider } from "./people.tsx";
import { pushOffOnSignOut } from "./push.ts";

export function SignedIn({ client, me, authMethod, reread, onKeyRefused, signOut }: {
  client: ApiClient;
  me: MeAnswer;
  authMethod: AuthMethod;
  reread: () => Promise<MeAnswer | null>;
  onKeyRefused: (client: ApiClient) => void;
  /** AuthSession.signOut, given how to remove push with the session's client. */
  signOut: (unsubscribe: (root: ApiClient) => Promise<unknown>) => void;
}) {
  return (
    <Gatekeeper client={client} me={me} authMethod={authMethod} reread={reread} onSignOut={() => signOut(pushOffOnSignOut())}>
      {(scoped) => (
        <PeopleProvider client={scoped}>
          <App client={scoped} onKeyRefused={onKeyRefused} onSignOut={() => signOut(pushOffOnSignOut(scoped))} />
        </PeopleProvider>
      )}
    </Gatekeeper>
  );
}
