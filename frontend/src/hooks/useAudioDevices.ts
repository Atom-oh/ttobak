'use client';

import { useState, useEffect, useCallback, useRef } from 'react';

const STORAGE_KEY = 'ttobak-mic-deviceId';

async function readAudioDevices(mediaDevices: MediaDevices): Promise<MediaDeviceInfo[] | null> {
  try {
    const devices = await mediaDevices.enumerateDevices();
    return devices.filter((device) => device.kind === 'audioinput');
  } catch {
    // Permission denied or unavailable; retain the last successful list.
    return null;
  }
}

export function useAudioDevices() {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [selectedDeviceId, setSelectedDeviceId] = useState<string>(() => {
    if (typeof window === 'undefined') return '';
    return localStorage.getItem(STORAGE_KEY) || '';
  });
  const refreshRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (typeof navigator === 'undefined' || !navigator.mediaDevices) return;
    const mediaDevices = navigator.mediaDevices;
    let active = true;
    let requestId = 0;

    const enumerate = () => {
      const currentRequestId = ++requestId;
      void readAudioDevices(mediaDevices).then((audioDevices) => {
        if (active && currentRequestId === requestId && audioDevices !== null) {
          setDevices(audioDevices);
        }
      });
    };

    refreshRef.current = enumerate;
    mediaDevices.addEventListener('devicechange', enumerate);
    enumerate();
    return () => {
      active = false;
      refreshRef.current = null;
      mediaDevices.removeEventListener('devicechange', enumerate);
    };
  }, []);

  const selectDevice = useCallback((deviceId: string) => {
    setSelectedDeviceId(deviceId);
    if (deviceId) {
      localStorage.setItem(STORAGE_KEY, deviceId);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  }, []);

  // Re-enumerate after permission is granted (labels become available)
  const refreshDevices = useCallback(() => {
    refreshRef.current?.();
  }, []);

  return { devices, selectedDeviceId, selectDevice, refreshDevices };
}
