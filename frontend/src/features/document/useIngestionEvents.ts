import { useEffect, useRef, useState } from "react";
import { wsURL } from "@/shared/api/client";
import { useAuth } from "@/features/auth";
import type { IngestionEvent } from "./api";

/** Подписка на живой прогресс индексации по /ws: события приходят только для
 *  документов текущего пользователя; reconnect с экспоненциальным backoff.
 *  onEvent читается через ref — сокет не пересоздаётся при каждом рендере. */
export function useIngestionEvents(onEvent: (ev: IngestionEvent) => void): { live: boolean } {
  const { auth } = useAuth();
  const token = auth?.access_token;
  const [live, setLive] = useState(false);
  const handler = useRef(onEvent);
  handler.current = onEvent;

  useEffect(() => {
    if (!token) return;
    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let stopped = false;

    const connect = () => {
      const socket = new WebSocket(wsURL(token));
      ws = socket;
      socket.addEventListener("open", () => {
        attempt = 0;
        setLive(true);
      });
      socket.addEventListener("message", (e: MessageEvent) => {
        let ev: IngestionEvent;
        try {
          ev = JSON.parse(String(e.data)) as IngestionEvent;
        } catch {
          return;
        }
        if (typeof ev?.document_id !== "string" || typeof ev?.status !== "string") return;
        handler.current(ev);
      });
      socket.addEventListener("close", () => {
        setLive(false);
        if (stopped) return;
        attempt += 1;
        const delay = Math.min(30_000, 1000 * 2 ** Math.min(attempt - 1, 5));
        timer = setTimeout(connect, delay);
      });
      socket.addEventListener("error", () => {
        socket.close();
      });
    };
    connect();

    return () => {
      stopped = true;
      clearTimeout(timer);
      ws?.close();
      setLive(false);
    };
  }, [token]);

  return { live };
}
