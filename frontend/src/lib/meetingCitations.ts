export const meetingCitationAttributes = ['data-meeting-citation', 'data-meeting-citation-target'] as const;
const [sourceAttribute, targetAttribute] = meetingCitationAttributes;
const citationPattern = /^(?:attachment:\/\/[a-f0-9-]+|transcript:\/\/[a-z0-9_-]+)$/i;

/** Bind only canonical citations; ordinary links sharing a download URL stay ordinary. */
export function bindMeetingCitations(html: string, resolve: (source: string) => string): string {
  const template = document.createElement('template');
  template.innerHTML = html;
  for (const node of template.content.querySelectorAll('a[href], img[src]')) {
    const attribute = node.tagName === 'A' ? 'href' : 'src';
    const source = node.getAttribute(attribute) ?? '';
    node.removeAttribute(sourceAttribute);
    node.removeAttribute(targetAttribute);
    if (!citationPattern.test(source)) continue;
    const resolved = resolve(source);
    const target = resolved === source ? (attribute === 'href' ? '#' : 'about:blank') : resolved;
    node.setAttribute(attribute, target);
    node.setAttribute(sourceAttribute, source);
    node.setAttribute(targetAttribute, target);
  }
  return template.innerHTML;
}

/** Restore IDs only while the edited link/image still has its bound display target. */
export function restoreMeetingCitations(html: string): string {
  const template = document.createElement('template');
  template.innerHTML = html;
  for (const node of template.content.querySelectorAll('a[href], img[src]')) {
    const attribute = node.tagName === 'A' ? 'href' : 'src';
    const source = node.getAttribute(sourceAttribute) ?? '';
    const target = node.getAttribute(targetAttribute);
    if (citationPattern.test(source) && target && node.getAttribute(attribute) === target) {
      node.setAttribute(attribute, source);
    }
    node.removeAttribute(sourceAttribute);
    node.removeAttribute(targetAttribute);
  }
  return template.innerHTML;
}
