import { useEffect, useRef } from "react";
import type { Operation } from "./types";

type Handlers = { operation?: (o: Operation) => void; log?: (e: { id: string; line: string }) => void };

// One shared EventSource for the whole app; handlers subscribe/unsubscribe.
let source: EventSource | null = null;
const subs = new Set<{ current: Handlers }>();

function ensure() {
  if (source) return;
  source = new EventSource("/api/events");
  source.addEventListener("operation", (e) => subs.forEach((s) => s.current.operation?.(JSON.parse((e as MessageEvent).data))));
  source.addEventListener("log", (e) => subs.forEach((s) => s.current.log?.(JSON.parse((e as MessageEvent).data))));
}

export function useEvents(h: Handlers) {
  const ref = useRef(h);
  ref.current = h;
  useEffect(() => {
    ensure();
    subs.add(ref);
    return () => { subs.delete(ref); };
  }, []);
}
