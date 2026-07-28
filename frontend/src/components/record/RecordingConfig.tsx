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
}: Pick<RecordingConfigProps, 'summaryInterval' | 'onSummaryIntervalChange' | 'translationEnabled' | 'onTranslationToggle' | 'targetLang' | 'onTargetLangChange'>) {
  return (
    <div className="flex items-center gap-2">
      <select
        value={summaryInterval}
        onChange={(e) => onSummaryIntervalChange(Number(e.target.value))}
        className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-slate-600 dark:text-gray-400 focus:outline-none focus:ring-2 focus:ring-primary/30"
      >
        <option value={50}>50w</option>
        <option value={100}>100w</option>
        <option value={200}>200w</option>
        <option value={500}>500w</option>
      </select>
      <label className="flex items-center gap-1.5 text-sm text-slate-600 dark:text-gray-400">
        <input
          type="checkbox"
          checked={translationEnabled}
          onChange={(e) => onTranslationToggle(e.target.checked)}
          className="rounded border-slate-300 text-primary focus:ring-primary"
        />
        번역
      </label>
      {translationEnabled && (
        <select
          value={targetLang}
          onChange={(e) => onTargetLangChange(e.target.value)}
          className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-slate-600 dark:text-gray-400 focus:outline-none focus:ring-2 focus:ring-primary/30"
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
            ? 'bg-primary text-white'
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
            ? 'bg-primary text-white'
            : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
        } disabled:opacity-50 disabled:cursor-not-allowed`}
      >
        Nova Sonic V2
      </button>
    </div>
  );
}

/** Live STT engine selector + active engine badge */
export function LiveSttSelector({
  liveSttProvider,
  onLiveSttProviderChange,
  activeProvider,
  isRecording,
}: {
  liveSttProvider: string;
  onLiveSttProviderChange: (provider: 'transcribe-streaming' | 'web-speech') => void;
  activeProvider: string;
  isRecording: boolean;
}) {
  return (
    <div className="flex items-center gap-2">
      {/* Provider selector (disabled during recording) */}
      <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
        <button
          onClick={() => onLiveSttProviderChange('transcribe-streaming')}
          disabled={isRecording}
          className={`px-3 py-1.5 text-xs font-medium transition-colors ${
            liveSttProvider === 'transcribe-streaming'
              ? 'bg-primary text-white'
              : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
          } disabled:opacity-50 disabled:cursor-not-allowed`}
        >
          AWS Streaming
        </button>
        <button
          onClick={() => onLiveSttProviderChange('web-speech')}
          disabled={isRecording}
          className={`px-3 py-1.5 text-xs font-medium transition-colors border-l border-slate-200 dark:border-slate-700 ${
            liveSttProvider === 'web-speech'
              ? 'bg-primary text-white'
              : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-400 hover:bg-slate-50 dark:hover:bg-slate-700'
          } disabled:opacity-50 disabled:cursor-not-allowed`}
        >
          Browser
        </button>
      </div>

      {/* Active engine badge (shown during recording) */}
      {isRecording && (
        <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold uppercase tracking-wider ${
          activeProvider === 'transcribe-streaming'
            ? 'bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400'
            : 'bg-amber-100 dark:bg-amber-900/30 text-amber-700 dark:text-amber-400'
        }`}>
          <span className={`w-1.5 h-1.5 rounded-full ${
            activeProvider === 'transcribe-streaming' ? 'bg-green-500' : 'bg-amber-500'
          }`} />
          {activeProvider === 'transcribe-streaming' ? 'AWS' : 'Browser'}
        </span>
      )}
    </div>
  );
}
