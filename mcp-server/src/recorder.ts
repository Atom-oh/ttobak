import { spawn, type ChildProcess } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { existsSync, linkSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { basename, dirname, join } from 'node:path';

// Local microphone capture for the stdio transport only (ADR-045: the HTTP
// transport never touches the server's filesystem or processes). ffmpeg's
// avfoundation input records the Mac microphone; the child inherits the MCP
// host app's macOS microphone (TCC) permission, and a host without it records
// silence rather than failing, so stop() callers run silenceCheck().
//
// WAV (16 kHz mono PCM, ~1.9 MB/min) is used rather than m4a: it is the format
// the Mac app already uploads, and an ungraceful exit leaves a decodable file
// instead of an m4a missing its moov atom.
//
// Several MCP hosts (Claude Desktop, Codex, Kiro) may each run a stdio server
// over the same directory. Each recording therefore has a sidecar with a
// persisted state, and every upload holds an exclusive <id>.lock file:
// - state 'recording' is never offered for upload while its owner process or
//   ffmpeg is alive, so another process cannot take an unfinalized WAV;
// - the lock serializes meeting creation, upload and cleanup per recording,
//   in-process and across processes;
// - upload progress (meetingId, uploadKey, uploadPut) is persisted before each
//   irreversible step, so a retry resumes rather than repeating it.

export const DEFAULT_MAX_MINUTES = 240;
// The Transcribe fallback caps media at four hours; the Whisper path does not.
export const MAX_MAX_MINUTES = 240;
const SAMPLE_RATE = 16_000;
const BYTES_PER_SECOND = SAMPLE_RATE * 2; // mono s16le
const WAV_HEADER_BYTES = 44;
const SILENCE_THRESHOLD_DB = -60;
const STDERR_TAIL_BYTES = 4096;
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MEETING_ID_PATTERN = /^[\w-]{1,128}$/;
// audio/{userId}/{meetingId}/{ts}_mcp_upload_{ts}.{ext} (UploadService.sanitizeFileName adds {ts}_).
const UPLOAD_KEY_PATTERN = /^audio\/[\w-]{1,128}\/[\w-]{1,128}\/(?:\d{1,16}_)?mcp_upload_\d{1,16}\.[a-z0-9]{1,5}$/;

export interface RecordingInfo {
  id: string;
  title: string;
  file: string;
  device: string;
  startedAt: string;
  maxMinutes: number;
  /** Meeting this adapter created for the recording, reused on upload retry. */
  meetingId?: string;
  /** S3 key issued for the audio; reused so a retry never creates a second object. */
  uploadKey?: string;
  /** True once the PUT of uploadKey succeeded; a retry then only completes it. */
  uploadPut?: boolean;
}

export type UploadProgress = Pick<RecordingInfo, 'meetingId' | 'uploadKey' | 'uploadPut'>;

interface Sidecar extends RecordingInfo {
  state: 'recording' | 'kept';
  ownerPid?: number;
  ffmpegPid?: number;
}

export interface RecorderOptions {
  ffmpegPath?: string;
  dir?: string;
  platform?: NodeJS.Platform;
  startupGraceMs?: number;
  stopTimeoutMs?: number;
}

export interface StoppedRecording extends RecordingInfo {
  bytes: number;
  durationSeconds: number;
  warning?: string;
  /** stop() returns holding the recording's upload lock; always call release(). */
  release(): void;
}

interface Active {
  info: RecordingInfo;
  child: ChildProcess;
  exited: Promise<number | null>;
  exitCode?: number | null;
  stderr: string;
  /** Set once stop() begins; the recording stays reserved until it resolves. */
  stopping?: Promise<StoppedRecording>;
}

export function defaultRecordingsDir(): string {
  // Not a dot-directory: guardUploadPath refuses hidden paths under $HOME.
  return process.env.TTOBAK_RECORDINGS_DIR || join(homedir(), 'Library', 'Application Support', 'ttobak', 'recordings');
}

export function validateDevice(device: unknown): string {
  if (device === undefined || device === null || device === '') return 'default';
  if (typeof device === 'number' && Number.isInteger(device) && device >= 0 && device < 1000) return String(device);
  // ':' separates avfoundation's video:audio selectors; control characters
  // never belong in a device name.
  if (typeof device !== 'string' || device.length > 200 || /[:\x00-\x1f\x7f]/.test(device)) {
    throw new Error('device must be an audio device index or name from ttobak_list_audio_devices');
  }
  return device.trim() || 'default';
}

export function validateMaxMinutes(value: unknown): number {
  if (value === undefined || value === null) return DEFAULT_MAX_MINUTES;
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > MAX_MAX_MINUTES) {
    throw new Error(`maxMinutes must be an integer between 1 and ${MAX_MAX_MINUTES}`);
  }
  return value;
}

/** Parses the audio section of `ffmpeg -f avfoundation -list_devices true -i ""`. */
export function parseAudioDevices(stderr: string): { index: number; name: string }[] {
  const devices: { index: number; name: string }[] = [];
  let inAudio = false;
  for (const line of stderr.split(/\r?\n/)) {
    if (/AVFoundation audio devices:/.test(line)) { inAudio = true; continue; }
    if (/AVFoundation video devices:/.test(line)) { inAudio = false; continue; }
    const match = inAudio && /\[(\d+)\]\s*(.+?)\s*$/.exec(line);
    if (match) devices.push({ index: Number(match[1]), name: match[2] });
  }
  return devices;
}

export function parseMaxVolume(stderr: string): number | null {
  const match = /max_volume:\s*(-?(?:\d+(?:\.\d+)?|inf))\s*dB/.exec(stderr);
  if (!match) return null;
  return match[1] === '-inf' ? Number.NEGATIVE_INFINITY : Number(match[1]);
}

function fileSize(path: string): number | null {
  try {
    return statSync(path).size;
  } catch {
    return null;
  }
}

function lockPid(content: string): number {
  return Number(content.split(':')[0]);
}

/** True when a process exists (EPERM still means it exists). */
export function isProcessAlive(pid: unknown): boolean {
  if (typeof pid !== 'number' || !Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === 'EPERM';
  }
}

export class Recorder {
  private active: Active | null = null;
  // Reserved synchronously so overlapping start() calls cannot both spawn ffmpeg.
  private starting: Promise<unknown> | null = null;
  private readonly ffmpegPath: string;
  private readonly dir: string;
  private readonly platform: NodeJS.Platform;
  private readonly startupGraceMs: number;
  private readonly stopTimeoutMs: number;

  constructor(options: RecorderOptions = {}) {
    this.ffmpegPath = options.ffmpegPath ?? process.env.TTOBAK_FFMPEG ?? 'ffmpeg';
    this.dir = options.dir ?? defaultRecordingsDir();
    this.platform = options.platform ?? process.platform;
    this.startupGraceMs = options.startupGraceMs ?? 2000;
    this.stopTimeoutMs = options.stopTimeoutMs ?? 10_000;
  }

  async listDevices(): Promise<{ index: number; name: string }[]> {
    this.requireMac();
    // list_devices always exits non-zero after printing; only stderr matters.
    const { stderr } = await this.run(['-hide_banner', '-f', 'avfoundation', '-list_devices', 'true', '-i', '']);
    return parseAudioDevices(stderr);
  }

  async start(input: { title: string; device?: unknown; maxMinutes?: unknown }): Promise<RecordingInfo> {
    this.requireMac();
    if (this.active) throw new Error(`A recording is already running (${this.active.info.id}); stop it first`);
    if (this.starting) throw new Error('A recording is already starting');
    const starting = this.startChild(input);
    this.starting = starting;
    try {
      return await starting;
    } finally {
      this.starting = null;
    }
  }

  private async startChild(input: { title: string; device?: unknown; maxMinutes?: unknown }): Promise<RecordingInfo> {
    const title = input.title.trim();
    if (!title || title.length > 200) throw new Error('title must be 1-200 characters');
    const device = validateDevice(input.device);
    const maxMinutes = validateMaxMinutes(input.maxMinutes);
    mkdirSync(this.dir, { recursive: true, mode: 0o700 });

    const id = randomUUID();
    const info: RecordingInfo = {
      id, title, device, maxMinutes, file: join(this.dir, `${id}.wav`), startedAt: new Date().toISOString(),
    };
    // Written before spawn so a host crash at any point leaves a recoverable
    // identity; state 'recording' stays hidden while this process lives.
    this.writeSidecar({ ...info, state: 'recording', ownerPid: process.pid });
    const child = spawn(this.ffmpegPath, [
      '-hide_banner', '-nostats', '-loglevel', 'error',
      '-f', 'avfoundation', '-i', `:${device}`,
      '-ac', '1', '-ar', String(SAMPLE_RATE), '-c:a', 'pcm_s16le',
      '-t', String(maxMinutes * 60), '-y', info.file,
    ], { stdio: ['pipe', 'ignore', 'pipe'] });
    const active: Active = { info, child, stderr: '', exited: Promise.resolve(null) };
    active.exited = new Promise((resolve) => {
      child.on('error', (err) => { active.stderr += `\n${err.message}`; active.exitCode = -1; resolve(-1); });
      child.on('exit', (code) => { active.exitCode = code; resolve(code); });
    });
    child.stderr?.on('data', (chunk: Buffer) => {
      active.stderr = (active.stderr + chunk.toString()).slice(-STDERR_TAIL_BYTES);
    });
    // Keep an ignored EPIPE on stdin from crashing the server if ffmpeg exits first.
    child.stdin?.on('error', () => {});

    const early = await Promise.race([
      active.exited.then(() => true),
      new Promise<false>((resolve) => setTimeout(() => resolve(false), this.startupGraceMs)),
    ]);
    if (early) {
      rmSync(info.file, { force: true });
      this.removeSidecar(id);
      const hint = /ENOENT/.test(active.stderr) ? ' Install ffmpeg (brew install ffmpeg) or set TTOBAK_FFMPEG.' : '';
      throw new Error(`ffmpeg exited during startup: ${active.stderr.trim() || `code ${active.exitCode}`}.${hint}`);
    }
    try {
      this.writeSidecar({ ...info, state: 'recording', ownerPid: process.pid, ffmpegPid: child.pid });
    } catch (e) {
      child.kill('SIGKILL');
      await active.exited;
      rmSync(info.file, { force: true });
      this.removeSidecar(id);
      throw new Error(`Could not save recording metadata: ${e instanceof Error ? e.message : String(e)}`);
    }
    this.active = active;
    return info;
  }

  status(): (RecordingInfo & { running: boolean; elapsedSeconds: number; bytes: number }) | null {
    if (!this.active) return null;
    const { info } = this.active;
    return {
      ...info,
      running: this.active.exitCode === undefined,
      elapsedSeconds: Math.round((Date.now() - Date.parse(info.startedAt)) / 1000),
      bytes: existsSync(info.file) ? statSync(info.file).size : 0,
    };
  }

  /** Stops and finalizes the active recording. It returns holding the
   * recording's upload lock, so nothing else can take it before the caller
   * uploads or keeps it; the caller must call release(). */
  async stop(): Promise<StoppedRecording> {
    const active = this.active;
    if (!active) throw new Error('No recording is running');
    if (active.stopping) throw new Error(`Recording ${active.info.id} is already stopping`);
    // Keep the recording reserved (hidden from listSaved/getSaved and blocking
    // start) until ffmpeg has finalized the file.
    active.stopping = this.finalize(active);
    try {
      return await active.stopping;
    } finally {
      this.active = null;
    }
  }

  private async finalize(active: Active): Promise<StoppedRecording> {
    if (active.exitCode === undefined) {
      // 'q' lets ffmpeg finalize the WAV header; signals are fallbacks only.
      active.child.stdin?.end('q\n');
      if (!(await this.waitExit(active, this.stopTimeoutMs))) {
        active.child.kill('SIGINT');
        if (!(await this.waitExit(active, 5000))) {
          active.child.kill('SIGKILL');
          await active.exited;
        }
      }
    }
    const { id, file } = active.info;
    const bytes = existsSync(file) ? statSync(file).size : 0;
    if (bytes <= WAV_HEADER_BYTES) {
      rmSync(file, { force: true });
      this.removeSidecar(id);
      throw new Error(`Recording produced no audio: ${active.stderr.trim() || 'empty file'}`);
    }
    // Lock before publishing state 'kept', so no other request or process can
    // pick the recording up between finalization and this caller's upload.
    const release = this.acquire(id);
    try {
      this.writeSidecar({ ...active.info, state: 'kept' });
    } catch (e) {
      release();
      throw new Error(`Recording ${id} was saved but its metadata could not be updated: ${e instanceof Error ? e.message : String(e)}`);
    }
    const warning = active.exitCode !== 0 && active.exitCode !== 255
      ? `ffmpeg exited with code ${active.exitCode}: ${active.stderr.trim()}` : undefined;
    return {
      ...active.info, bytes, durationSeconds: Math.round((bytes - WAV_HEADER_BYTES) / BYTES_PER_SECOND),
      ...(warning ? { warning } : {}), release,
    };
  }

  /** Reports whether the file is effectively silent (e.g. host lacks mic permission). */
  async silenceCheck(file: string): Promise<{ maxVolumeDb: number | null; silent: boolean }> {
    const name = basename(file);
    if (dirname(file) !== this.dir || !name.endsWith('.wav') || !UUID_PATTERN.test(name.slice(0, -'.wav'.length))) {
      throw new Error('silenceCheck accepts only a recording in the recordings directory');
    }
    const { stderr } = await this.run(['-hide_banner', '-nostats', '-i', file, '-af', 'volumedetect', '-f', 'null', '-']);
    const maxVolumeDb = parseMaxVolume(stderr);
    return { maxVolumeDb, silent: maxVolumeDb !== null && maxVolumeDb < SILENCE_THRESHOLD_DB };
  }

  /** Finalized recordings kept on disk (e.g. after an upload failure or host
   * restart). A recording still being captured by a live process is omitted. */
  listSaved(): (RecordingInfo & { bytes: number; uploading: boolean })[] {
    if (!existsSync(this.dir)) return [];
    const saved: (RecordingInfo & { bytes: number; uploading: boolean })[] = [];
    for (const name of readdirSync(this.dir)) {
      if (!name.endsWith('.json')) continue;
      const id = name.slice(0, -'.json'.length);
      if (!UUID_PATTERN.test(id) || id === this.active?.info.id) continue;
      const info = this.readSidecar(id);
      const bytes = info && fileSize(info.file);
      if (!info || bytes === null) continue; // e.g. discarded concurrently
      saved.push({ ...info, bytes, uploading: this.lockHolder(id) !== null });
    }
    return saved.sort((a, b) => a.startedAt.localeCompare(b.startedAt));
  }

  /** Resolves a kept recording by id without accepting a caller-supplied path. */
  getSaved(id: string): RecordingInfo & { bytes: number } {
    if (typeof id !== 'string' || !UUID_PATTERN.test(id) || id === this.active?.info.id) {
      throw new Error(`No saved recording ${id}; see ttobak_recording_status`);
    }
    const info = this.readSidecar(id);
    const bytes = info && fileSize(info.file);
    if (!info || bytes === null) throw new Error(`No saved recording ${id}; see ttobak_recording_status`);
    return { ...info, bytes };
  }

  /** Takes the exclusive per-recording upload lock. The lock file is linked
   * into place from a complete temporary file, so it never exists without its
   * token. A lock whose holder process exited is replaced only while holding a
   * short-lived takeover lock and only if its content is still the stale token
   * observed, so two contenders cannot both remove each other's fresh lock.
   * Unreadable or empty lock content counts as live. Returns a release function. */
  acquire(id: string): () => void {
    if (!UUID_PATTERN.test(id)) throw new Error('Invalid recording id');
    const lock = join(this.dir, `${id}.lock`);
    const token = `${process.pid}:${randomUUID()}`;
    const busy = () => new Error(`Recording ${id} is being uploaded by another request; check ttobak_recording_status`);
    if (!this.createExclusive(lock, token)) {
      const observed = this.readLock(lock);
      if (observed === null || isProcessAlive(lockPid(observed))) throw busy();
      const takeover = `${lock}.takeover`;
      if (!this.createExclusive(takeover, token)) {
        throw new Error(`Recording ${id} lock is being recovered; if this persists, delete ${takeover}`);
      }
      try {
        if (this.readLock(lock) === observed) rmSync(lock, { force: true });
      } finally {
        rmSync(takeover, { force: true });
      }
      if (!this.createExclusive(lock, token)) throw busy();
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      // Only a dead holder's lock is ever taken over, so while this process
      // lives the lock still carries this token.
      if (this.readLock(lock) === token) rmSync(lock, { force: true });
    };
  }

  /** Persists upload progress for a kept recording; the caller holds its lock. */
  saveProgress(id: string, progress: UploadProgress): void {
    if (!UUID_PATTERN.test(id)) throw new Error('Invalid recording id');
    if (progress.meetingId !== undefined && !MEETING_ID_PATTERN.test(progress.meetingId)) throw new Error('Invalid meeting id');
    if (progress.uploadKey !== undefined && !UPLOAD_KEY_PATTERN.test(progress.uploadKey)) throw new Error('Invalid upload key');
    const info = this.readSidecar(id);
    if (!info) throw new Error(`No saved recording ${id}`);
    this.writeSidecar({ ...info, ...progress, state: 'kept' });
  }

  /** Deletes a recording and its sidecar after a confirmed upload. */
  discard(id: string): void {
    if (!UUID_PATTERN.test(id)) throw new Error('Invalid recording id');
    rmSync(join(this.dir, `${id}.wav`), { force: true });
    this.removeSidecar(id);
  }

  async shutdown(): Promise<void> {
    await this.starting?.catch(() => {});
    const active = this.active;
    if (!active) return;
    const stopped = await (active.stopping ?? this.stop()).catch(() => null);
    stopped?.release();
  }

  private requireMac(): void {
    if (this.platform !== 'darwin') throw new Error('Local recording is supported only on macOS');
  }

  private async waitExit(active: Active, ms: number): Promise<boolean> {
    return Promise.race([
      active.exited.then(() => true),
      new Promise<false>((resolve) => setTimeout(() => resolve(false), ms)),
    ]);
  }

  private run(args: string[]): Promise<{ code: number | null; stderr: string }> {
    return new Promise((resolve, reject) => {
      const child = spawn(this.ffmpegPath, args, { stdio: ['ignore', 'ignore', 'pipe'] });
      let stderr = '';
      child.stderr?.on('data', (chunk: Buffer) => { stderr = (stderr + chunk.toString()).slice(-64 * 1024); });
      child.on('error', (err) => reject(new Error(`${err.message}. Install ffmpeg (brew install ffmpeg) or set TTOBAK_FFMPEG.`)));
      child.on('exit', (code) => resolve({ code, stderr }));
    });
  }

  /** Reads a sidecar as a finalized recording, or null when it is not one. */
  private readSidecar(id: string): RecordingInfo | null {
    let parsed: Partial<Sidecar>;
    try {
      parsed = JSON.parse(readFileSync(join(this.dir, `${id}.json`), 'utf8'));
    } catch {
      return null; // missing or corrupt: not a recording
    }
    if (typeof parsed.title !== 'string' || typeof parsed.startedAt !== 'string') return null;
    // A capture is offered only once finalized, or once both its owner server
    // and its ffmpeg are gone (a crashed host leaves a usable WAV).
    if (parsed.state !== 'kept' && (isProcessAlive(parsed.ownerPid) || isProcessAlive(parsed.ffmpegPid))) return null;
    const { meetingId, uploadKey, uploadPut } = parsed;
    return {
      id, title: parsed.title, startedAt: parsed.startedAt,
      device: typeof parsed.device === 'string' ? parsed.device : 'default',
      maxMinutes: typeof parsed.maxMinutes === 'number' ? parsed.maxMinutes : DEFAULT_MAX_MINUTES,
      // Never trust a path from disk: the file is always <dir>/<uuid>.wav.
      file: join(this.dir, `${id}.wav`),
      ...(typeof meetingId === 'string' && MEETING_ID_PATTERN.test(meetingId) ? { meetingId } : {}),
      ...(typeof uploadKey === 'string' && UPLOAD_KEY_PATTERN.test(uploadKey) ? { uploadKey, uploadPut: uploadPut === true } : {}),
    };
  }

  /** PID of a live lock holder, or null when unlocked or stale. */
  private lockHolder(id: string): number | null {
    const lock = join(this.dir, `${id}.lock`);
    if (!existsSync(lock)) return null;
    const content = this.readLock(lock);
    if (content === null) return -1; // unreadable: treat as held
    const pid = lockPid(content);
    return isProcessAlive(pid) ? pid : null;
  }

  /** Lock content, or null when missing, empty or unreadable. */
  private readLock(path: string): string | null {
    try {
      return readFileSync(path, 'utf8') || null;
    } catch {
      return null;
    }
  }

  /** Atomically creates path holding content; false if it already exists. */
  private createExclusive(path: string, content: string): boolean {
    const tmp = `${path}.${randomUUID()}.tmp`;
    writeFileSync(tmp, content, { mode: 0o600 });
    try {
      linkSync(tmp, path);
      return true;
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'EEXIST') return false;
      throw e;
    } finally {
      rmSync(tmp, { force: true });
    }
  }

  private writeSidecar(info: Sidecar): void {
    const { id } = info;
    const stored: Record<string, unknown> = { ...info };
    delete stored.bytes;
    delete stored.release;
    // Write-then-rename so a reader never sees a partial sidecar.
    const tmp = join(this.dir, `${id}.json.${process.pid}.tmp`);
    writeFileSync(tmp, JSON.stringify(stored), { mode: 0o600 });
    renameSync(tmp, join(this.dir, `${id}.json`));
  }

  private removeSidecar(id: string): void {
    rmSync(join(this.dir, `${id}.json`), { force: true });
    rmSync(join(this.dir, `${id}.lock`), { force: true });
  }
}
