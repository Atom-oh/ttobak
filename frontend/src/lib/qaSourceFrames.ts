import type { WebSocketMessage } from './websocket';

const MAX_SOURCE_BYTES = 512 * 1024;
const MAX_SOURCE_FRAMES = 64;

/** Reassemble attribution before exposing a terminal answer to UI consumers. */
export class QASourceFrames {
  private batch: {
    id: string;
    sessionId?: string;
    count: number;
    sources: string[];
    details: NonNullable<WebSocketMessage['sourceDetails']>;
  } | null = null;

  reset() {
    this.batch = null;
  }

  private invalid(sessionId?: string): WebSocketMessage {
    this.reset();
    return {
      type: 'answer_error',
      sessionId,
      error: '출처 정보를 모두 수신하지 못했습니다. 답변이 완료되지 않았습니다.',
    };
  }

  accept(message: WebSocketMessage): WebSocketMessage | null {
    if (message.type === 'answer_start' || message.type === 'answer_error') {
      this.reset();
      return message;
    }
    if (message.type === 'answer_sources') {
      const { sourceBatchId: id, sourceBatchIndex: index, sources, sourceDetails } = message;
      if (typeof id !== 'string' || !/^[0-9a-f]{64}$/.test(id)
        || typeof index !== 'number' || !Number.isInteger(index) || index < 0 || index >= MAX_SOURCE_FRAMES
        || !Array.isArray(sources) || !sources.every(value => typeof value === 'string')
        || !Array.isArray(sourceDetails)
        || !sourceDetails.every(value => value !== null && typeof value === 'object' && !Array.isArray(value))) {
        return this.invalid(message.sessionId);
      }
      if (!this.batch) {
        if (index !== 0) return this.invalid(message.sessionId);
        this.batch = { id, sessionId: message.sessionId, count: 0, sources: [], details: [] };
      }
      const batch = this.batch;
      if (batch.id !== id || batch.sessionId !== message.sessionId || batch.count !== index) {
        return this.invalid(message.sessionId);
      }
      const combined = { sources: [...batch.sources, ...sources], sourceDetails: [...batch.details, ...sourceDetails] };
      if (new TextEncoder().encode(JSON.stringify(combined)).byteLength > MAX_SOURCE_BYTES) {
        return this.invalid(message.sessionId);
      }
      batch.sources = combined.sources;
      batch.details = combined.sourceDetails;
      batch.count++;
      return null;
    }
    if (message.type === 'answer_complete') {
      const batch = this.batch;
      if (!batch && message.sourceBatchId === undefined && message.sourceBatchCount === undefined) return message;
      if (!batch || batch.id !== message.sourceBatchId || batch.sessionId !== message.sessionId
        || batch.count !== message.sourceBatchCount || message.sources !== undefined || message.sourceDetails !== undefined) {
        return this.invalid(message.sessionId);
      }
      const complete = { ...message, sources: batch.sources, sourceDetails: batch.details };
      delete complete.sourceBatchId;
      delete complete.sourceBatchCount;
      this.reset();
      return complete;
    }
    return message;
  }
}
