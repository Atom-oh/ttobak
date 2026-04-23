'use client';

import { getIdToken } from './auth';

export interface WebSocketMessage {
  type:
    | 'answer_start'
    | 'answer_delta'
    | 'answer_complete'
    | 'answer_error'
    | 'error';
  text?: string;
  sessionId?: string;
  answer?: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  error?: string;
}

type MessageHandler = (msg: WebSocketMessage) => void;

const MAX_RECONNECT_ATTEMPTS = 5;
const BASE_RECONNECT_DELAY = 1000;
const MAX_RECONNECT_DELAY = 15000;

export class RealtimeWebSocket {
  private ws: WebSocket | null = null;
  private url: string;
  private onMessage: MessageHandler;
  private onClose?: () => void;
  private onReconnect?: () => void;
  private reconnectAttempts = 0;
  private intentionalClose = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    url: string,
    onMessage: MessageHandler,
    onClose?: () => void,
    onReconnect?: () => void,
  ) {
    this.url = url;
    this.onMessage = onMessage;
    this.onClose = onClose;
    this.onReconnect = onReconnect;
  }

  async connect(): Promise<void> {
    const token = getIdToken();
    if (!token) throw new Error('No auth token');
    const wsUrl = `${this.url}?token=${encodeURIComponent(token)}`;

    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(wsUrl);
      this.intentionalClose = false;

      this.ws.onopen = () => {
        this.reconnectAttempts = 0;
        resolve();
      };

      this.ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data) as WebSocketMessage;
          this.onMessage(msg);
        } catch {
          // Ignore unparseable messages
        }
      };

      this.ws.onclose = () => {
        this.ws = null;
        if (this.intentionalClose) {
          this.onClose?.();
          return;
        }
        this.scheduleReconnect();
      };

      this.ws.onerror = () => {
        if (this.reconnectAttempts === 0) {
          reject(new Error('WebSocket connection failed'));
        }
      };
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
    this.send({
      action: 'ask_live',
      question,
      context: ctx,
      meetingId,
      sessionId,
    });
  }

  private send(data: unknown) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(data));
    }
  }

  disconnect() {
    this.intentionalClose = true;
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
