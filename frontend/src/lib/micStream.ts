/**
 * Microphone stream acquisition with a fallback for a stale saved device.
 *
 * The record page remembers the selected microphone's `deviceId` in
 * localStorage. WebKit and Chromium may issue new device IDs after an app
 * rebuild, a cleared site data store or a device change, so an `exact`
 * constraint on a remembered ID can fail with `OverconstrainedError`
 * ("Invalid constraint" in the Mac app's WebView) and block recording.
 * A missing device falls back to the default microphone; every other
 * failure (for example a denied permission) is rethrown unchanged.
 */

export interface MicStreamResult {
  stream: MediaStream;
  /** True when the requested device was unavailable and the default
   * microphone was opened instead. */
  usedDefault: boolean;
}

/** True for errors meaning the requested input device does not exist. */
export function isMissingDeviceError(err: unknown): boolean {
  if (!err || typeof err !== 'object') return false;
  const name = (err as { name?: unknown }).name;
  return name === 'OverconstrainedError' || name === 'NotFoundError';
}

export async function openMicStream(
  mediaDevices: Pick<MediaDevices, 'getUserMedia'>,
  deviceId?: string,
): Promise<MicStreamResult> {
  if (!deviceId) {
    return { stream: await mediaDevices.getUserMedia({ audio: true }), usedDefault: false };
  }
  try {
    const stream = await mediaDevices.getUserMedia({ audio: { deviceId: { exact: deviceId } } });
    return { stream, usedDefault: false };
  } catch (err) {
    if (!isMissingDeviceError(err)) throw err;
    return { stream: await mediaDevices.getUserMedia({ audio: true }), usedDefault: true };
  }
}
