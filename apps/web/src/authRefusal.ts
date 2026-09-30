import { createContext } from "react";
import type { ApiClient } from "./api/client.ts";

/** Reports that `client`'s credential was refused, to the session that issued it. */
export const AuthRefusal = createContext<(client: ApiClient) => void>(() => {});
