'use client';

import { useState, useEffect, useCallback } from 'react';

const STORAGE_KEY = 'ttobak-mic-deviceId';

export function useAudioDevices() {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [selectedDeviceId, setSelectedDeviceId] = useState<string>(() => {
    if (typeof window === 'undefined') return '';
    return localStorage.getItem(STORAGE_KEY) || '';
  });

  const enumerate = useCallback(async () => {
    try {
      const allDevices = await navigator.mediaDevices.enumerateDevices();
      setDevices(allDevices.filter((d) => d.kind === 'audioinput'));
    } catch {
      // Permission denied or unavailable
    }
  }, []);

  useEffect(() => {
    enumerate();
    navigator.mediaDevices.addEventListener('devicechange', enumerate);
    return () => {
      navigator.mediaDevices.removeEventListener('devicechange', enumerate);
    };
  }, [enumerate]);

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
    enumerate();
  }, [enumerate]);

  return { devices, selectedDeviceId, selectDevice, refreshDevices };
}
