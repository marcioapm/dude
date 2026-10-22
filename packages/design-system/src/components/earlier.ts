import { createContext, useContext } from "react";

/**
 * Whether what renders is inside `EarlierTurns`: an earlier attempt's
 * conversation, there to read. Components with actions (`ChatRunLine`,
 * `QuestionCard`) render without them inside it.
 */
const EarlierContext = createContext(false);

export const EarlierProvider = EarlierContext.Provider;

export function useEarlier(): boolean {
  return useContext(EarlierContext);
}
