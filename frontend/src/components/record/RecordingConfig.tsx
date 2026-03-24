'use client';

interface RecordingConfigProps {
  summaryInterval: number;
  onSummaryIntervalChange: (val: number) => void;
  translationEnabled: boolean;
  onTranslationToggle: (enabled: boolean) => void;
  targetLang: string;
  onTargetLangChange: (lang: string) => void;
  isRecording: boolean;
  sttProvider: 'transcribe' | 'nova-sonic';
  onSttProviderChange: (provider: 'transcribe' | 'nova-sonic') => void;
}

export function RecordingConfig({
  summaryInterval,
  onSummaryIntervalChange,
  translationEnabled,
  onTranslationToggle,
  targetLang,
  onTargetLangChange,
  isRecording,
  sttProvider,
  onSttProviderChange,
}: RecordingConfigProps) {
  return (
    <div className="flex items-center gap-2">
      <select
        value={summaryInterval}
        onChange={(e) => onSummaryIntervalChange(Number(e.target.value))}
        className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-[var(--color-text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--color-primary)]/30"
      >
        <option value={100}>100w</option>
        <option value={200}>200w</option>
        <option value={500}>500w</option>
        <option value={1000}>1000w</option>
      </select>
      <label className="flex items-center gap-1.5 text-sm text-[var(--color-text-secondary)]">
        <input
          type="checkbox"
          checked={translationEnabled}
          onChange={(e) => onTranslationToggle(e.target.checked)}
          className="rounded border-slate-300 text-[var(--color-primary)] focus:ring-[var(--color-primary)]"
        />
        번역
      </label>
      {translationEnabled && (
        <select
          value={targetLang}
          onChange={(e) => onTargetLangChange(e.target.value)}
          className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-[var(--color-text-secondary)] focus:outline-none focus:ring-2 focus:ring-[var(--color-primary)]/30"
        >
          <option value="en">EN</option>
          <option value="ja">JA</option>
          <option value="zh">ZH</option>
          <option value="es">ES</option>
          <option value="fr">FR</option>
          <option value="de">DE</option>
        </select>
      )}
    </div>
  );
}

export function SttProviderSelector({
  sttProvider,
  onSttProviderChange,
  isRecording,
}: Pick<RecordingConfigProps, 'sttProvider' | 'onSttProviderChange' | 'isRecording'>) {
  return (
    <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
      <button
        onClick={() => onSttProviderChange('transcribe')}
        disabled={isRecording}
        className={`px-4 py-1.5 text-xs font-medium transition-colors ${
          sttProvider === 'transcribe'
            ? 'bg-[#3211d4] text-white'
            : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
        } disabled:opacity-50 disabled:cursor-not-allowed`}
      >
        Transcribe
      </button>
      <button
        onClick={() => onSttProviderChange('nova-sonic')}
        disabled={isRecording}
        className={`px-4 py-1.5 text-xs font-medium transition-colors border-l border-slate-200 dark:border-slate-700 ${
          sttProvider === 'nova-sonic'
            ? 'bg-[#3211d4] text-white'
            : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
        } disabled:opacity-50 disabled:cursor-not-allowed`}
      >
        Nova Sonic V2
      </button>
    </div>
  );
}
