/** A string's steady place among `slots` (djb2): the same string, the same place. */
export function stringSlot(key: string, slots: number): number {
  let h = 5381;
  for (let i = 0; i < key.length; i++) h = ((h << 5) + h + key.charCodeAt(i)) | 0;
  return Math.abs(h) % slots;
}
