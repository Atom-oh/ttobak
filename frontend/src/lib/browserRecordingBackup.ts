export interface BrowserRecordingMetadata {
  userId: string;
  id: string;
  title: string;
  notes: string;
  mimeType: string;
  meetingId?: string;
  uploadKey?: string;
  createdAt: number;
  savedAt: number;
  chunkCount: number;
  byteSize: number;
  finalized: boolean;
}

type RecordingPatch = Partial<Pick<BrowserRecordingMetadata, 'title' | 'notes' | 'meetingId' | 'uploadKey' | 'finalized'>>;

const DATABASE = 'ttobak-recording-backups';
const RECORDINGS = 'recordings';
const CHUNKS = 'chunks';

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DATABASE, 1);
    request.onupgradeneeded = () => {
      request.result.createObjectStore(RECORDINGS, { keyPath: ['userId', 'id'] });
      request.result.createObjectStore(CHUNKS, { keyPath: ['userId', 'id', 'index'] });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    request.onblocked = () => reject(new Error('다른 탭을 닫고 다시 시도해 주세요.'));
  });
}

async function transaction<Result>(
  stores: string[],
  mode: IDBTransactionMode,
  operation: (transaction: IDBTransaction, result: (value: Result) => void) => void,
): Promise<Result> {
  const database = await openDatabase();
  return new Promise((resolve, reject) => {
    const pending = database.transaction(stores, mode);
    let result: Result;
    pending.oncomplete = () => { database.close(); resolve(result); };
    pending.onabort = () => { database.close(); reject(pending.error ?? new Error('기기 저장에 실패했습니다.')); };
    pending.onerror = () => {};
    try {
      operation(pending, (value) => { result = value; });
    } catch (error) {
      pending.abort();
      database.close();
      reject(error);
    }
  });
}

function chunkRange(userId: string, id: string): IDBKeyRange {
  return IDBKeyRange.bound([userId, id, 0], [userId, id, Number.MAX_SAFE_INTEGER]);
}

async function lockRecording(userId: string, id: string): Promise<() => void> {
  if (!navigator.locks) throw new Error('이 브라우저는 안전한 기기 복구를 지원하지 않습니다.');
  return new Promise((resolve, reject) => {
    void navigator.locks.request(`${DATABASE}:${userId}:${id}`, { ifAvailable: true }, (lock) => {
      if (!lock) {
        reject(new Error('다른 탭에서 이 녹음을 사용 중입니다. 해당 탭에서 먼저 종료해 주세요.'));
        return;
      }
      return new Promise<void>((release) => { resolve(release); });
    }).catch(reject);
  });
}

export function listBrowserRecordings(userId: string): Promise<BrowserRecordingMetadata[]> {
  return transaction([RECORDINGS], 'readonly', (pending, result) => {
    const request = pending.objectStore(RECORDINGS).getAll(IDBKeyRange.bound([userId, ''], [userId, '\uffff']));
    request.onsuccess = () => result((request.result as BrowserRecordingMetadata[])
      .filter((recording) => recording.chunkCount > 0)
      .sort((left, right) => right.savedAt - left.savedAt));
  });
}

export class BrowserRecordingBackup {
  private queue: Promise<unknown> = Promise.resolve();
  private failure: unknown;
  private released = false;

  private constructor(
    public metadata: BrowserRecordingMetadata,
    private unlock: () => void,
  ) {}

  static async create(userId: string, title: string, notes: string, mimeType: string): Promise<BrowserRecordingBackup> {
    const now = Date.now();
    const metadata: BrowserRecordingMetadata = {
      userId, id: crypto.randomUUID(), title, notes, mimeType,
      createdAt: now, savedAt: now, chunkCount: 0, byteSize: 0, finalized: false,
    };
    const unlock = await lockRecording(userId, metadata.id);
    try {
      await transaction([RECORDINGS], 'readwrite', (pending, result) => {
        pending.objectStore(RECORDINGS).add(metadata);
        result(undefined);
      });
      return new BrowserRecordingBackup(metadata, unlock);
    } catch (error) {
      unlock();
      throw error;
    }
  }

  static async open(userId: string, id: string): Promise<BrowserRecordingBackup> {
    const unlock = await lockRecording(userId, id);
    try {
      const metadata = await transaction<BrowserRecordingMetadata | undefined>([RECORDINGS], 'readonly', (pending, result) => {
        const request = pending.objectStore(RECORDINGS).get([userId, id]);
        request.onsuccess = () => result(request.result);
      });
      if (!metadata) throw new Error('이 계정의 저장된 녹음을 찾을 수 없습니다.');
      return new BrowserRecordingBackup(metadata, unlock);
    } catch (error) {
      unlock();
      throw error;
    }
  }

  private enqueue<Result>(operation: () => Promise<Result>): Promise<Result> {
    const next = this.queue.then(() => {
      if (this.failure) throw this.failure;
      if (this.released) throw new Error('기기 녹음 보관이 종료되었습니다.');
      return operation();
    });
    this.queue = next.catch((error) => { this.failure = error; });
    return next;
  }

  append(blob: Blob): Promise<void> {
    if (!blob.size) return Promise.resolve();
    return this.enqueue(async () => {
      const previous = this.metadata;
      const metadata = { ...previous, chunkCount: previous.chunkCount + 1, byteSize: previous.byteSize + blob.size, savedAt: Date.now() };
      await transaction([RECORDINGS, CHUNKS], 'readwrite', (pending, result) => {
        pending.objectStore(CHUNKS).add({ userId: previous.userId, id: previous.id, index: previous.chunkCount, blob });
        pending.objectStore(RECORDINGS).put(metadata);
        result(undefined);
      });
      this.metadata = metadata;
    });
  }

  update(patch: RecordingPatch): Promise<void> {
    return this.enqueue(async () => {
      const metadata = { ...this.metadata, ...patch };
      await transaction([RECORDINGS], 'readwrite', (pending, result) => {
        pending.objectStore(RECORDINGS).put(metadata);
        result(undefined);
      });
      this.metadata = metadata;
    });
  }

  async readBlob(): Promise<Blob> {
    await this.flush();
    const { userId, id, chunkCount, byteSize, mimeType } = this.metadata;
    const chunks = await transaction<{ index: number; blob: Blob }[]>([CHUNKS], 'readonly', (pending, result) => {
      const request = pending.objectStore(CHUNKS).getAll(chunkRange(userId, id));
      request.onsuccess = () => result(request.result);
    });
    if (!chunkCount || chunks.length !== chunkCount || chunks.some((chunk, index) => chunk.index !== index)) {
      throw new Error('녹음 조각이 누락되어 복구할 수 없습니다.');
    }
    const blob = new Blob(chunks.map((chunk) => chunk.blob), { type: mimeType });
    if (blob.size !== byteSize) throw new Error('저장된 녹음 크기가 일치하지 않습니다.');
    return blob;
  }

  async flush(): Promise<void> {
    await this.queue;
    if (this.failure) throw this.failure;
  }

  remove(): Promise<void> {
    const next = this.queue.then(async () => {
      if (this.released) throw new Error('기기 녹음 보관이 종료되었습니다.');
      const { userId, id } = this.metadata;
      await transaction([RECORDINGS, CHUNKS], 'readwrite', (pending, result) => {
        pending.objectStore(RECORDINGS).delete([userId, id]);
        pending.objectStore(CHUNKS).delete(chunkRange(userId, id));
        result(undefined);
      });
      this.released = true;
      this.unlock();
    });
    this.queue = next.catch((error) => { this.failure = error; });
    return next;
  }

  release(): void {
    void this.queue.finally(() => {
      this.released = true;
      this.unlock();
    });
  }
}
