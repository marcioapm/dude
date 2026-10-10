/**
 * Unsent words, kept in this browser the way a chat app keeps them: one
 * localStorage entry per person and place (`dude.draft.<personId>.<place>`,
 * place as `session:<id>`, `task:<id>`, `run:<id>`), so leaving a composer
 * and coming back finds what was being written. Nothing reaches the server.
 *
 * Saved 2 s after the last change, and at once when the composer unmounts
 * or the page is hidden or left: moving to another agent inside those 2 s
 * is the case this exists for. Text only: images in a composer's tray are
 * not drafted. Storage failing (quota, private mode) loses the draft,
 * never the message.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { usePeople } from "../people.tsx";

const PREFIX = "dude.draft.";
const DRAFT_IDLE_MS = 2_000;
export const DRAFT_MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000;

export interface DraftEntry {
  readonly text: string;
  readonly savedAt: number;
}

export const draftKey = (personId: string, place: string) => `${PREFIX}${personId}.${place}`;

function storage(): Storage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/** Drops every draft older than `DRAFT_MAX_AGE_MS`, and any entry that does not parse. */
export function pruneDrafts(now = Date.now()): void {
  const s = storage();
  if (!s) return;
  try {
    const keys: string[] = [];
    for (let i = 0; i < s.length; i++) {
      const k = s.key(i);
      if (k?.startsWith(PREFIX)) keys.push(k);
    }
    for (const k of keys) if (!parse(s.getItem(k), now)) s.removeItem(k);
  } catch {
    // Best-effort: an unreadable store only keeps old drafts longer.
  }
}

function parse(raw: string | null, now: number): DraftEntry | null {
  if (!raw) return null;
  try {
    const e = JSON.parse(raw) as Partial<DraftEntry>;
    if (typeof e.text !== "string" || typeof e.savedAt !== "number" || now - e.savedAt > DRAFT_MAX_AGE_MS) return null;
    return e as DraftEntry;
  } catch {
    return null;
  }
}

let pruned = false;

export function readDraft(key: string): DraftEntry | null {
  if (!pruned) {
    pruned = true;
    pruneDrafts();
  }
  try {
    return parse(storage()?.getItem(key) ?? null, Date.now());
  } catch {
    return null;
  }
}

/** Stores `text` for `key`; blank text removes the entry rather than storing it empty. */
function writeDraft(key: string, text: string): void {
  try {
    if (text.trim()) storage()?.setItem(key, JSON.stringify({ text, savedAt: Date.now() }));
    else storage()?.removeItem(key);
  } catch {
    // Quota or private mode: this draft is not kept.
  }
}

export interface Draft {
  readonly value: string;
  readonly onValueChange: (value: string) => void;
}

/**
 * A composer's text for `place` (null: not drafted), as `ChatComposer`'s
 * `value`/`onValueChange`. The composer sets "" only on a confirmed send,
 * and a blank value removes the entry: that is what clears it.
 */
export function useDraft(place: string | null): Draft {
  const { you } = usePeople();
  const key = you && place ? draftKey(you, place) : null;
  const [value, setValue] = useState(() => (key ? readDraft(key)?.text ?? "" : ""));
  const latest = useRef(value);
  const dirty = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const keyRef = useRef(key);

  const flush = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = undefined;
    if (!dirty.current || !keyRef.current) return;
    dirty.current = false;
    writeDraft(keyRef.current, latest.current);
  }, []);

  const schedule = useCallback(() => {
    dirty.current = true;
    clearTimeout(timer.current);
    timer.current = setTimeout(flush, DRAFT_IDLE_MS);
  }, [flush]);

  // The person is learnt after the first render: their draft is read then,
  // unless something was already typed, which is kept and saved as theirs.
  useEffect(() => {
    const was = keyRef.current;
    if (was === key) return;
    flush();
    keyRef.current = key;
    if (!key) return;
    if (was === null && latest.current.trim()) {
      schedule();
      return;
    }
    const text = readDraft(key)?.text ?? "";
    latest.current = text;
    setValue(text);
  }, [key, flush, schedule]);

  useEffect(() => {
    const hidden = () => {
      if (document.visibilityState === "hidden") flush();
    };
    document.addEventListener("visibilitychange", hidden);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", hidden);
      window.removeEventListener("pagehide", flush);
      flush();
    };
  }, [flush]);

  const cancel = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = undefined;
    dirty.current = false;
  }, []);

  const onValueChange = useCallback((v: string) => {
    latest.current = v;
    setValue(v);
    if (v.trim()) {
      schedule();
      return;
    }
    cancel();
    if (keyRef.current) writeDraft(keyRef.current, "");
  }, [schedule, cancel]);

  return { value, onValueChange };
}
