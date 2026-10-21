import { defaultUrlTransform } from 'react-markdown';
import { restoreMarkdownIPv6Authority } from './markdownUrl';

/** Preserve the Markdown library's protocol policy after repairing IPv6 hosts. */
export function safeMarkdownUrl(url: string): string {
  return defaultUrlTransform(restoreMarkdownIPv6Authority(url));
}
