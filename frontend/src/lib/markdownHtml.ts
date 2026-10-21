import { Marked, Renderer, type RendererObject } from 'marked';
import { restoreMarkdownIPv6Authority } from './markdownUrl';

/** Keep marked's attribute escaping, then repair only its encoded IPv6 host. */
export const markdownLinkRenderer: RendererObject = {
  link(token) {
    return Renderer.prototype.link.call(this, token).replace(/^<a href="([^"]*)"/, (_attribute: string, href: string) =>
      `<a href="${restoreMarkdownIPv6Authority(href)}"`);
  },
};

const parser = new Marked({ renderer: markdownLinkRenderer });

/** Consumers still sanitize this HTML before rendering or editing it. */
export function markdownToHtml(content: string): string {
  return parser.parse(content, { async: false });
}
