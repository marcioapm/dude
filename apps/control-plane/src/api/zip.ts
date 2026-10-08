/**
 * A zip archive, written as it streams: stored entries (no compression),
 * each with a data descriptor, so an entry's bytes go out as they are read
 * and nothing is held but the central directory. Artifacts are mostly
 * images, video and text an agent wrote — the first two do not compress,
 * and the archive is for taking them home, not for saving bytes. Bun has
 * no zip writer (node:zlib's crc32 is the one piece it has), and the format is simple enough not to need a dependency.
 *
 * ZIP64 is not written: an archive of the latest versions of a task's
 * files stays far below 4 GiB, and a larger one is refused rather than
 * written wrong.
 */

import { crc32 } from "node:zlib";

export interface ZipEntry {
  /** Its path in the archive; "/"-separated, never absolute. */
  name: string;
  /** When it was made; zip keeps local time to two seconds. */
  modified?: Date;
  /** Its bytes, read only when the archive reaches it. */
  open: () => Promise<ReadableStream<Uint8Array> | Uint8Array>;
}

const LIMIT = 0xffffffff;
// Bit 3: sizes and CRC follow the data. Bit 11: the name is UTF-8.
const FLAGS = 0x0808;

function dosTime(d: Date): { time: number; date: number } {
  const year = Math.max(1980, d.getFullYear());
  return {
    time: (d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1),
    date: ((year - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate(),
  };
}

/**
 * An entry name that cannot escape the directory it is unzipped into:
 * no leading slash, no "..", no backslashes read as separators.
 */
export function safeEntryName(name: string): string {
  const parts = name.replaceAll("\\", "/").split("/").filter((p) => p !== "" && p !== "." && p !== "..");
  return parts.join("/") || "file";
}

/**
 * A stream of the archive holding `entries`, in order. Each pull emits one
 * piece (a header, one chunk of an entry's body, its trailer), so a reader
 * that stops reading stops the entry's source being read too.
 */
export function zipStream(entries: Iterable<ZipEntry> | AsyncIterable<ZipEntry>): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  const central: Uint8Array[] = [];
  let offset = 0;
  let count = 0;
  const iterator = (async function* () {
    yield* entries;
  })();
  // The entry whose body is being read, between pulls.
  let current: {
    name: string; encoded: Uint8Array; time: number; date: number; headerOffset: number;
    reader: ReadableStreamDefaultReader<Uint8Array> | null; crc: number; size: number;
  } | null = null;

  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      const push = (bytes: Uint8Array) => {
        controller.enqueue(bytes);
        offset += bytes.length;
      };
      const finish = () => {
        const e = current!;
        current = null;
        if (e.size > LIMIT) throw new Error(`${e.name} is too large for a zip without ZIP64`);
        const descriptor = new DataView(new ArrayBuffer(16));
        descriptor.setUint32(0, 0x08074b50, true);
        descriptor.setUint32(4, e.crc, true);
        descriptor.setUint32(8, e.size, true);
        descriptor.setUint32(12, e.size, true);
        push(new Uint8Array(descriptor.buffer));

        const record = new Uint8Array(46 + e.encoded.length);
        const cd = new DataView(record.buffer);
        cd.setUint32(0, 0x02014b50, true);
        // Made on Unix, with a file's mode: unzip then takes names as they
        // are, rather than translating them from an MS-DOS code page.
        cd.setUint16(4, (3 << 8) | 20, true);
        cd.setUint16(6, 20, true);
        cd.setUint16(8, FLAGS, true);
        cd.setUint16(10, 0, true);
        cd.setUint16(12, e.time, true);
        cd.setUint16(14, e.date, true);
        cd.setUint32(16, e.crc, true);
        cd.setUint32(20, e.size, true);
        cd.setUint32(24, e.size, true);
        cd.setUint16(28, e.encoded.length, true);
        cd.setUint32(38, (0o100644 << 16) >>> 0, true);
        cd.setUint32(42, e.headerOffset, true);
        record.set(e.encoded, 46);
        central.push(record);
        count++;
      };

      if (current?.reader) {
        const { done, value } = await current.reader.read();
        if (done) return finish();
        current.crc = crc32(value, current.crc);
        current.size += value.length;
        push(value);
        return;
      }

      const next = await iterator.next();
      if (next.done) {
        const start = offset;
        let size = 0;
        for (const record of central) {
          push(record);
          size += record.length;
        }
        if (count > 0xffff || start > LIMIT) throw new Error("the archive is too large for a zip without ZIP64");
        const end = new DataView(new ArrayBuffer(22));
        end.setUint32(0, 0x06054b50, true);
        end.setUint16(8, count, true);
        end.setUint16(10, count, true);
        end.setUint32(12, size, true);
        end.setUint32(16, start, true);
        push(new Uint8Array(end.buffer));
        controller.close();
        return;
      }
      const entry = next.value;
      const encoded = encoder.encode(safeEntryName(entry.name));
      const { time, date } = dosTime(entry.modified ?? new Date());
      const headerOffset = offset;
      if (headerOffset > LIMIT) throw new Error("the archive is too large for a zip without ZIP64");

      const local = new DataView(new ArrayBuffer(30));
      local.setUint32(0, 0x04034b50, true);
      local.setUint16(4, 20, true);
      local.setUint16(6, FLAGS, true);
      local.setUint16(8, 0, true); // stored
      local.setUint16(10, time, true);
      local.setUint16(12, date, true);
      local.setUint16(26, encoded.length, true);
      push(new Uint8Array(local.buffer));
      push(encoded);

      const body = await entry.open();
      current = { name: entry.name, encoded, time, date, headerOffset, reader: null, crc: 0, size: 0 };
      if (body instanceof Uint8Array) {
        // Already in memory: nothing more to hold back.
        current.crc = crc32(body);
        current.size = body.length;
        push(body);
        finish();
      } else {
        current.reader = body.getReader();
      }
    },
    async cancel(reason) {
      await current?.reader?.cancel(reason);
      await iterator.return?.(undefined);
    },
  });
}
