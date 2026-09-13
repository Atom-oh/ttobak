'use client';

import { getIdToken } from './auth';
import { runtimeWebSocketUrl } from './runtimeConfig';

export interface WebSocketMessage {
  type:
    | 'answer_start'
    | 'answer_delta'
    | 'answer_complete'
    | 'answer_error'
    | 'tool_progress'
    | 'error';
  text?: string;
  sessionId?: string;
  answer?: string;
  sources?: string[];
  sourceDetails?: import('@/types/meeting').QASourceDetail[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  error?: string;
}

type MessageHandler = (msg: WebSocketMessage) => void;

const MAX_RECONNECT_ATTEMPTS = 5;
const BASE_RECONNECT_DELAY = 1000;
const MAX_RECONNECT_DELAY = 15000;
const CONNECT_TIMEOUT_MS = 10_000;
const MAX_FRAME_BYTES = 30_000;

export class RealtimeWebSocket {
  private ws: WebSocket | null = null;
  private url: string;
  private onMessage: MessageHandler;
  private onClose?: () => void;
  private onReconnect?: () => void;
  private reconnectAttempts = 0;
  private intentionalClose = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private cancelConnect: (() => void) | null = null;
  private autoReconnect: boolean;

  constructor(
    url: string,
    onMessage: MessageHandler,
    onClose?: () => void,
    onReconnect?: () => void,
    options?: { autoReconnect?: boolean },
  ) {
    this.url = url;
    this.onMessage = onMessage;
    this.onClose = onClose;
    this.onReconnect = onReconnect;
    this.autoReconnect = options?.autoReconnect ?? true;
  }

  async connect(): Promise<void> {
    const endpoint = runtimeWebSocketUrl(this.url);
    if (!endpoint) throw new Error('WebSocket endpoint unavailable');
    const token = getIdToken();
    if (!token) throw new Error('No auth token');
    const wsUrl = `${endpoint}?token=${encodeURIComponent(token)}`;

    return new Promise((resolve, reject) => {
      const socket = new WebSocket(wsUrl);
      this.ws = socket;
      this.intentionalClose = false;
      let settled = false;
      const finish = (error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (this.cancelConnect === cancel) this.cancelConnect = null;
        if (error) reject(error); else resolve();
      };
      const cancel = () => finish(new Error('WebSocket connection cancelled'));
      const timer = setTimeout(() => {
        finish(new Error('WebSocket connection timed out'));
        if (this.ws === socket) this.disconnect();
      }, CONNECT_TIMEOUT_MS);
      this.cancelConnect = cancel;

      socket.onopen = () => {
        if (this.ws !== socket || this.intentionalClose) return;
        this.reconnectAttempts = 0;
        finish();
      };

      socket.onmessage = (event) => {
        if (this.ws !== socket || this.intentionalClose) return;
        try {
          const msg = JSON.parse(event.data) as WebSocketMessage;
          this.onMessage(msg);
        } catch {
          // Ignore unparseable messages
        }
      };

      socket.onclose = () => {
        finish(new Error('WebSocket closed before connecting'));
        if (this.ws !== socket) return;
        this.ws = null;
        if (this.intentionalClose || !this.autoReconnect) {
          this.onClose?.();
          return;
        }
        this.scheduleReconnect();
      };

      socket.onerror = () => finish(new Error('WebSocket connection failed'));
    });
  }

  private scheduleReconnect() {
    if (this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      this.onClose?.();
      return;
    }
    this.reconnectAttempts++;
    const delay = Math.min(
      BASE_RECONNECT_DELAY * Math.pow(2, this.reconnectAttempts - 1),
      MAX_RECONNECT_DELAY,
    );
    this.reconnectTimer = setTimeout(async () => {
      try {
        await this.connect();
        this.onReconnect?.();
      } catch {
        // connect failed — onclose will trigger another scheduleReconnect
      }
    }, delay);
  }

  async reconnect(): Promise<boolean> {
    if (this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) return false;
    this.reconnectAttempts++;
    const delay = Math.min(
      BASE_RECONNECT_DELAY * Math.pow(2, this.reconnectAttempts - 1),
      MAX_RECONNECT_DELAY,
    );
    await new Promise((r) => setTimeout(r, delay));
    try {
      await this.connect();
      return true;
    } catch {
      return false;
    }
  }

  askLive(question: string, ctx?: string, meetingId?: string, sessionId?: string) {
    return this.send({
      action: 'ask_live',
      question,
      context: ctx,
      meetingId,
      sessionId,
    });
  }

  private send(data: unknown): boolean {
    const body = JSON.stringify(data);
    if (this.ws?.readyState !== WebSocket.OPEN || new TextEncoder().encode(body).length > MAX_FRAME_BYTES) return false;
    try {
      this.ws.send(body);
      return true;
    } catch {
      return false;
    }
  }

  disconnect() {
    this.intentionalClose = true;
    this.cancelConnect?.();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.ws?.close();
    this.ws = null;
  }

  get isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
}
