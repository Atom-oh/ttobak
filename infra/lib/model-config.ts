import { Construct } from 'constructs';

export interface ModelConfiguration {
  readonly meetingSummary: string;
  readonly translation: string;
}

export const DEFAULT_MODEL_CONFIGURATION: ModelConfiguration = Object.freeze({
  meetingSummary: 'global.openai.gpt-6.1-sol',
  translation: 'global.openai.gpt-6-luna',
});

const summaryPresets: Readonly<Record<string, string>> = Object.freeze({
  standard: DEFAULT_MODEL_CONFIGURATION.meetingSummary,
  terra: 'global.openai.gpt-6.1-terra',
  sonnet: 'global.anthropic.claude-sonnet-5-5',
});

function optionalText(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== 'string') throw new Error(`${label} must be a string`);
  return value.trim() || undefined;
}

/** Accept only native request families already implemented by the backend. */
function modelId(value: string, purpose: 'meeting summary' | 'translation'): string {
  const supported = /^(?:global\.)?(?:openai\.gpt-[a-z0-9][a-z0-9.-]*|anthropic\.claude-[a-z0-9][a-z0-9.-]*)$/;
  if (value.length > 128 || !supported.test(value)) {
    throw new Error(`${purpose} model must be an exact OpenAI GPT or Anthropic Claude ID, with an optional global. prefix`);
  }
  const normalized = value.startsWith('global.') ? value : `global.${value}`;
  if (purpose === 'translation' && !normalized.startsWith('global.openai.')) {
    throw new Error('translation model must use OpenAI GPT: the native translation request format is OpenAI only');
  }
  return normalized;
}

/** Stack constructors validate too, so direct callers cannot widen IAM via an ID. */
export function validateModelConfiguration(config: ModelConfiguration): ModelConfiguration {
  return Object.freeze({
    meetingSummary: modelId(config.meetingSummary, 'meeting summary'),
    translation: modelId(config.translation, 'translation'),
  });
}

/**
 * Resolve once in the app, then share the result between Lambda and IAM stacks.
 * Explicit CDK context > deployment environment > preset > pinned defaults.
 * Empty fields inherit; malformed nonempty fields fail synthesis.
 */
export function resolveModelConfiguration(
  scope: Construct,
  environment: Readonly<Record<string, string | undefined>> = process.env,
): ModelConfiguration {
  const context = (key: string) => optionalText(scope.node.tryGetContext(key), key);
  const env = (key: string) => optionalText(environment[key], key);
  const mode = context('model-deployment-mode') ?? env('MODEL_DEPLOYMENT_MODE') ?? 'standard';
  if (!Object.hasOwn(summaryPresets, mode)) {
    throw new Error('model deployment mode must be standard, terra or sonnet');
  }

  const summary = context('model-meeting-summary');
  // Compatibility with the spelling in the original deployment request.
  const summaryAlias = context('model-meeting-summery');
  if (summary && summaryAlias && modelId(summary, 'meeting summary') !== modelId(summaryAlias, 'meeting summary')) {
    throw new Error('model-meeting-summary and model-meeting-summery conflict');
  }

  return validateModelConfiguration({
    meetingSummary: summary ?? summaryAlias ?? env('MODEL_MEETING_SUMMARY') ?? summaryPresets[mode],
    translation: context('model-translation') ?? env('MODEL_TRANSLATION') ?? DEFAULT_MODEL_CONFIGURATION.translation,
  });
}
