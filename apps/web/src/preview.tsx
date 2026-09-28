/**
 * What the preview frame shows in place of a server's URL, when there is
 * something to show without the network: nothing in the app — the frame
 * loads the URL — and the mockup's page in the fixtures, which have no
 * server to reach. A context, so the fixtures stay out of the screens.
 */

import { createContext, useContext } from "react";

export const PreviewDocumentContext = createContext<string | undefined>(undefined);

export function usePreviewDocument(): string | undefined {
  return useContext(PreviewDocumentContext);
}
