/**
 * The signed-in app as main.tsx mounts it: the gatekeeper, then the tenant
 * app for the organisation it chose. Sign-out, from inside the app or from a
 * front door, removes this browser's push registration from every
 * membership the gate's answer lists, through the session's own client.
 */

import type { ApiClient, MeAnswer } from "./api/client.ts";
import { App } from "./App.tsx";
import { Gatekeeper, membershipsOf } from "./accounts/Gatekeeper.tsx";
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
  const leave = () => signOut(pushOffOnSignOut(membershipsOf(me).map((m) => m.id)));
  return (
    <Gatekeeper client={client} me={me} authMethod={authMethod} reread={reread} onSignOut={leave}>
      {(scoped) => (
        <PeopleProvider client={scoped}>
          <App client={scoped} onKeyRefused={onKeyRefused} onSignOut={leave} />
        </PeopleProvider>
      )}
    </Gatekeeper>
  );
}
