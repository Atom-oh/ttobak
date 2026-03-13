'use client';

import { useEffect, useRef, useState, useCallback } from 'react';

interface MicSelectorProps {
  devices: MediaDeviceInfo[];
  selectedDeviceId: string;
  onSelect: (deviceId: string) => void;
  disabled?: boolean;
  analyser?: AnalyserNode | null;
}

function getDeviceType(label: string): string {
  const lower = label.toLowerCase();
  if (lower.includes('bluetooth')) return 'Bluetooth';
  if (lower.includes('usb')) return 'USB';
  if (lower.includes('airpods')) return 'Bluetooth';
  if (lower.includes('built-in') || lower.includes('internal') || lower.includes('default')) return 'Built-in';
  if (lower.includes('headset') || lower.includes('headphone')) return 'Headset';
  return 'External';
}

function getDeviceIcon(label: string): string {
  const lower = label.toLowerCase();
  if (lower.includes('bluetooth') || lower.includes('airpods')) return 'bluetooth';
  if (lower.includes('usb')) return 'usb';
  if (lower.includes('headset') || lower.includes('headphone')) return 'headset_mic';
  return 'mic';
}

const SEGMENT_COUNT = 14;

export function MicSelector({ devices, selectedDeviceId, onSelect, disabled, analyser }: MicSelectorProps) {
  const [level, setLevel] = useState(0);
  const animationRef = useRef<number | null>(null);
  const dataArrayRef = useRef<Float32Array<ArrayBuffer> | null>(null);

  const updateLevel = useCallback(() => {
    if (!analyser) return;

    if (!dataArrayRef.current || dataArrayRef.current.length !== analyser.fftSize) {
      dataArrayRef.current = new Float32Array(analyser.fftSize);
    }

    analyser.getFloatTimeDomainData(dataArrayRef.current);

    // RMS calculation
    let sum = 0;
    for (let i = 0; i < dataArrayRef.current.length; i++) {
      sum += dataArrayRef.current[i] * dataArrayRef.current[i];
    }
    const rms = Math.sqrt(sum / dataArrayRef.current.length);

    // Map RMS to 0-1 range (typical speech RMS is 0.01-0.3)
    const normalized = Math.min(1, rms / 0.25);
    setLevel(normalized);

    animationRef.current = requestAnimationFrame(updateLevel);
  }, [analyser]);

  useEffect(() => {
    if (analyser) {
      animationRef.current = requestAnimationFrame(updateLevel);
    } else {
      setLevel(0);
    }
    return () => {
      if (animationRef.current) {
        cancelAnimationFrame(animationRef.current);
        animationRef.current = null;
      }
    };
  }, [analyser, updateLevel]);

  const activeSegments = Math.round(level * SEGMENT_COUNT);

  return (
    <div className="w-full max-w-md">
      {/* Device List */}
      <div className="bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700 overflow-hidden mb-4">
        {devices.length === 0 ? (
          <div className="px-4 py-3 flex items-center gap-3">
            <span className="material-symbols-outlined text-slate-400 text-lg">mic</span>
            <span className="text-sm text-slate-500 dark:text-slate-400">Default Microphone</span>
          </div>
        ) : (
          devices.map((device, i) => {
            const isSelected = device.deviceId === selectedDeviceId || (!selectedDeviceId && i === 0);
            const label = device.label || `Microphone ${i + 1}`;
            const type = getDeviceType(label);
            const icon = getDeviceIcon(label);

            return (
              <button
                key={device.deviceId}
                onClick={() => onSelect(device.deviceId)}
                disabled={disabled}
                className={`w-full px-4 py-3 flex items-center gap-3 transition-colors text-left
                  ${i > 0 ? 'border-t border-slate-100 dark:border-slate-700/50' : ''}
                  ${isSelected
                    ? 'bg-primary/5 dark:bg-primary/10'
                    : 'hover:bg-slate-50 dark:hover:bg-slate-700/50'
                  }
                  disabled:opacity-50 disabled:cursor-not-allowed`}
              >
                <span className={`material-symbols-outlined text-lg ${isSelected ? 'text-primary' : 'text-slate-400'}`}>
                  {icon}
                </span>
                <div className="flex-1 min-w-0">
                  <p className={`text-sm font-medium truncate ${isSelected ? 'text-primary' : 'text-slate-700 dark:text-slate-300'}`}>
                    {label}
                  </p>
                  <p className="text-xs text-slate-400 dark:text-slate-500">{type}</p>
                </div>
                {isSelected && (
                  <span className="material-symbols-outlined text-primary text-lg">check_circle</span>
                )}
              </button>
            );
          })
        )}
      </div>

      {/* Input Level Meter */}
      <div className="bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700 px-4 py-3">
        <div className="flex items-center gap-3">
          <span className="text-xs font-medium text-slate-500 dark:text-slate-400 w-16 shrink-0">Input level</span>
          <div className="flex-1 flex gap-[3px]">
            {Array.from({ length: SEGMENT_COUNT }, (_, i) => {
              const isActive = i < activeSegments;
              // Green for first 8, yellow for 9-11, red for 12-14
              let colorClass: string;
              if (i < 8) {
                colorClass = isActive ? 'bg-green-500' : 'bg-slate-200 dark:bg-slate-600';
              } else if (i < 11) {
                colorClass = isActive ? 'bg-yellow-400' : 'bg-slate-200 dark:bg-slate-600';
              } else {
                colorClass = isActive ? 'bg-red-500' : 'bg-slate-200 dark:bg-slate-600';
              }
              return (
                <div
                  key={i}
                  className={`h-3 flex-1 rounded-sm transition-colors duration-75 ${colorClass}`}
                />
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}
