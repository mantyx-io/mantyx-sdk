/**
 * Minimal Server-Sent Events parser.
 *
 * Reads a `ReadableStream<Uint8Array>` (the body of a `fetch()` response) and
 * yields parsed events with `id`, `event` and `data` fields. We deliberately
 * keep this dependency-free instead of pulling in `eventsource` / `eventsource-parser`
 * so the SDK has the smallest possible install footprint.
 *
 * Reconnect/replay is handled at a higher layer using `Last-Event-ID` (the
 * default for browsers' `EventSource`) plus a `?lastSeq=` query param so curl
 * users and SSE-via-fetch consumers both work.
 */

export interface SseEvent {
  id?: string;
  event?: string;
  data: string;
}

export interface SseStreamOptions {
  /** AbortSignal for cancellation. */
  signal?: AbortSignal;
}

/**
 * Async generator yielding parsed SSE events from a fetch response body.
 * Comment frames (`:keep-alive`) are dropped.
 */
export async function* readSseStream(
  body: ReadableStream<Uint8Array> | null,
  opts: SseStreamOptions = {},
): AsyncGenerator<SseEvent, void, void> {
  if (!body) return;
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8");
  let buffer = "";

  let cancelled = false;
  const onAbort = (): void => {
    cancelled = true;
    try {
      void reader.cancel();
    } catch {
      // ignore
    }
  };
  if (opts.signal) {
    if (opts.signal.aborted) {
      onAbort();
    } else {
      opts.signal.addEventListener("abort", onAbort, { once: true });
    }
  }

  try {
    while (!cancelled) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      // SSE events are separated by a blank line (\n\n). Some servers emit \r\n.
      let sepIdx: number;
      while ((sepIdx = findSeparator(buffer)) !== -1) {
        const raw = buffer.slice(0, sepIdx);
        buffer = buffer.slice(sepIdx + (buffer.startsWith("\r", sepIdx) ? 4 : 2));
        const ev = parseEventBlock(raw);
        if (ev) yield ev;
      }
    }
  } finally {
    if (opts.signal) opts.signal.removeEventListener("abort", onAbort);
    try {
      reader.releaseLock();
    } catch {
      // ignore
    }
  }
}

function findSeparator(s: string): number {
  const lf = s.indexOf("\n\n");
  const crlf = s.indexOf("\r\n\r\n");
  if (lf === -1) return crlf;
  if (crlf === -1) return lf;
  return Math.min(lf, crlf);
}

function parseEventBlock(block: string): SseEvent | null {
  const lines = block.split(/\r?\n/);
  let id: string | undefined;
  let event: string | undefined;
  const dataLines: string[] = [];
  for (const line of lines) {
    if (line.length === 0) continue;
    if (line.startsWith(":")) continue; // comment / heartbeat
    const colonIdx = line.indexOf(":");
    const field = colonIdx === -1 ? line : line.slice(0, colonIdx);
    let value = colonIdx === -1 ? "" : line.slice(colonIdx + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "id") id = value;
    else if (field === "event") event = value;
    else if (field === "data") dataLines.push(value);
  }
  if (dataLines.length === 0 && id === undefined && event === undefined) {
    return null;
  }
  return {
    ...(id !== undefined ? { id } : {}),
    ...(event !== undefined ? { event } : {}),
    data: dataLines.join("\n"),
  };
}
