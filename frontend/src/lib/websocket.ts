'use client';

import { getIdToken } from './auth';

export interface WebSocketMessage {
  type:
    | 'transcript'
    | 'translation'
    | 'error'
    | 'connected'
    | 'session_started'
    | 'answer_start'
    | 'answer_delta'
    | 'answer_complete'
    | 'answer_error';
  text?: string;
  isFinal?: boolean;
  targetLang?: string;
  timestamp?: string;
  error?: string;
  // ask_live streaming fields
  sessionId?: string;
  answer?: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
}

type MessageHandler = (msg: WebSocketMessage) => void;

export class RealtimeWebSocket {
  private ws: WebSocket | null = null;
  private url: string;
  private onMessage: MessageHandler;
  private onClose?: () => void;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 3;

  constructor(url: string, onMessage: MessageHandler, onClose?: () => void) {
    this.url = url;
    this.onMessage = onMessage;
    this.onClose = onClose;
  }

  async connect(): Promise<void> {
    const token = getIdToken();
    const wsUrl = `${this.url}?token=${encodeURIComponent(token || '')}`;

    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(wsUrl);

      this.ws.onopen = () => {
        this.reconnectAttempts = 0;
        resolve();
      };

      this.ws.onmessage = (event) => {
        const msg = JSON.parse(event.data) as WebSocketMessage;
        this.onMessage(msg);
      };

      this.ws.onclose = () => {
        this.onClose?.();
      };

      this.ws.onerror = () => {
        reject(new Error('WebSocket connection failed'));
      };
    });
  }

  startSession(meetingId: string, language: string, targetLangs: string[]) {
    this.send({ action: 'start', meetingId, language, targetLangs });
  }

  sendAudio(data: string) {
    // base64 encoded
    this.send({ action: 'audio', data });
  }

  stopSession() {
    this.send({ action: 'stop' });
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
    this.ws?.close();
    this.ws = null;
  }

  get isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  get reconnectCount(): number {
    return this.reconnectAttempts;
  }

  get maxReconnects(): number {
    return this.maxReconnectAttempts;
  }
}
