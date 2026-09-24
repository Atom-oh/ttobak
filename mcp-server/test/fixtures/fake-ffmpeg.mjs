import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Recorder } from '../../dist/recorder.js';

// A stand-in for ffmpeg: lists devices, reports volume, or "records" a WAV
// until it reads 'q' on stdin, which is how Recorder stops real ffmpeg.
export const FAKE_FFMPEG = `#!/usr/bin/env node
const fs = require('node:fs');
const args = process.argv.slice(2);
if (args.includes('-list_devices')) {
  process.stderr.write('[AVFoundation indev @ 0x1] AVFoundation video devices:\\n[AVFoundation indev @ 0x1] [0] FaceTime HD Camera\\n' +
    '[AVFoundation indev @ 0x1] AVFoundation audio devices:\\n[AVFoundation indev @ 0x1] [0] MacBook Pro Microphone\\n' +
    '[AVFoundation indev @ 0x1] [1] External USB Mic\\n');
  process.exit(1);
}
if (args.includes('volumedetect')) {
  process.stderr.write('[Parsed_volumedetect_0 @ 0x2] max_volume: ' + (process.env.FAKE_MAX_VOLUME || '-12.5') + ' dB\\n');
  process.exit(0);
}
if (process.env.FAKE_FAIL) { process.stderr.write('Input/output error\\n'); process.exit(1); }
const out = args[args.length - 1];
fs.writeFileSync(out, Buffer.alloc(44 + 32000));
process.stdin.on('data', (d) => { if (d.toString().includes('q')) process.exit(0); });
setInterval(() => fs.appendFileSync(out, Buffer.alloc(3200)), 50);
`;

/** A Recorder over a temporary directory and the fake ffmpeg; cleaned up after the test. */
export function recorderFixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'ttobak-rec-'));
  const ffmpegPath = join(root, 'ffmpeg');
  writeFileSync(ffmpegPath, FAKE_FFMPEG);
  chmodSync(ffmpegPath, 0o755);
  const dir = join(root, 'recordings');
  const recorder = new Recorder({ ffmpegPath, dir, platform: 'darwin', startupGraceMs: 300, stopTimeoutMs: 2000 });
  t.after(async () => {
    await recorder.shutdown();
    rmSync(root, { recursive: true, force: true });
  });
  return { root, dir, recorder };
}
