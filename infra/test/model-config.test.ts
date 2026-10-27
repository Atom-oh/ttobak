import * as cdk from 'aws-cdk-lib';
import { resolveModelConfiguration, validateModelConfiguration } from '../lib/model-config';

const resolve = (context: Record<string, unknown> = {}, environment: Record<string, string | undefined> = {}) =>
  resolveModelConfiguration(new cdk.App({ context }), environment);

describe('deployment model configuration', () => {
  test.each([
    ['standard', 'global.openai.gpt-6.1-sol'],
    ['terra', 'global.openai.gpt-6.1-terra'],
    ['sonnet', 'global.anthropic.claude-sonnet-5-5'],
  ])('%s selects a final-note model and keeps Luna translation', (mode, meetingSummary) => {
    expect(resolve({}, { MODEL_DEPLOYMENT_MODE: mode })).toEqual({
      meetingSummary, translation: 'global.openai.gpt-6-luna',
    });
  });

  test('no deployment selection preserves the existing production defaults', () => {
    expect(resolve()).toEqual({
      meetingSummary: 'global.openai.gpt-6.1-sol', translation: 'global.openai.gpt-6-luna',
    });
  });

  test('role-specific environment overrides beat the preset and normalize bare IDs', () => {
    expect(resolve({}, {
      MODEL_DEPLOYMENT_MODE: 'sonnet',
      MODEL_MEETING_SUMMARY: ' openai.gpt-6.1-terra ',
      MODEL_TRANSLATION: 'openai.gpt-6.1-sol',
    })).toEqual({
      meetingSummary: 'global.openai.gpt-6.1-terra', translation: 'global.openai.gpt-6.1-sol',
    });
  });

  test('context overrides environment for each selection', () => {
    expect(resolve({
      'model-deployment-mode': 'sonnet',
      'model-meeting-summary': 'openai.gpt-6.1-sol',
      'model-translation': 'global.openai.gpt-6.1-terra',
    }, {
      MODEL_DEPLOYMENT_MODE: 'terra',
      MODEL_MEETING_SUMMARY: 'anthropic.claude-sonnet-5-5',
      MODEL_TRANSLATION: 'openai.gpt-6-luna',
    })).toEqual({
      meetingSummary: 'global.openai.gpt-6.1-sol', translation: 'global.openai.gpt-6.1-terra',
    });
  });

  test('blank dispatch/context fields inherit lower-precedence configuration', () => {
    expect(resolve({
      'model-deployment-mode': '', 'model-meeting-summary': ' ', 'model-translation': null,
    }, { MODEL_DEPLOYMENT_MODE: 'terra', MODEL_TRANSLATION: ' ' })).toEqual({
      meetingSummary: 'global.openai.gpt-6.1-terra', translation: 'global.openai.gpt-6-luna',
    });
  });

  test('the user spelling summery is a supported context alias', () => {
    expect(resolve({ 'model-meeting-summery': 'openai.gpt-6.1-terra' }).meetingSummary)
      .toBe('global.openai.gpt-6.1-terra');
  });

  test('conflicting spellings fail instead of ignoring one model selection', () => {
    expect(() => resolve({
      'model-meeting-summary': 'openai.gpt-6.1-sol',
      'model-meeting-summery': 'openai.gpt-6.1-terra',
    })).toThrow(/conflict/i);
  });

  test('equivalent normalized aliases do not conflict', () => {
    expect(resolve({
      'model-meeting-summary': 'global.openai.gpt-6.1-terra',
      'model-meeting-summery': 'openai.gpt-6.1-terra',
    }).meetingSummary).toBe('global.openai.gpt-6.1-terra');
  });

  test('unknown deployment mode fails even when model overrides are supplied', () => {
    expect(() => resolve({}, {
      MODEL_DEPLOYMENT_MODE: 'cheap', MODEL_MEETING_SUMMARY: 'openai.gpt-6.1-terra',
    })).toThrow(/mode/i);
  });

  test.each([
    'openai.*', 'global.openai.gpt-6*', 'openai.gpt-6/other', 'openai.gpt-6\n*',
    'arn:aws:bedrock:us-west-2::foundation-model/openai.gpt-6.1-terra',
    'us.openai.gpt-6.1-terra', 'meta.llama-4', 'OPENAI.gpt-6.1-terra', 'openai.' + 'a'.repeat(200),
  ])('rejects unsupported or unsafe final-note selection %s', (model) => {
    expect(() => resolve({}, { MODEL_MEETING_SUMMARY: model })).toThrow(/model/i);
  });

  test('translation rejects Anthropic before deployment because the native codec is OpenAI only', () => {
    expect(() => resolve({}, { MODEL_TRANSLATION: 'anthropic.claude-sonnet-5-5' }))
      .toThrow(/translation.*OpenAI/i);
  });

  test.each([true, 42, {}, ['terra']])('context must be text, received %p', (value) => {
    expect(() => resolve({ 'model-meeting-summary': value })).toThrow(/string/i);
  });

  test('constructor-level validation also rejects a wildcard role grant', () => {
    expect(() => validateModelConfiguration({
      meetingSummary: 'global.openai.*', translation: 'global.openai.gpt-6-luna',
    })).toThrow(/model/i);
  });

  test('resolved configuration cannot be mutated between stack constructors', () => {
    expect(Object.isFrozen(resolve())).toBe(true);
  });
});
