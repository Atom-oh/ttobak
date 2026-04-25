import { visit } from 'unist-util-visit';

const CALLOUT_REGEX = /^\[!(summary|warning|tip|danger|info)\]\s*(.*)/i;

export function remarkCallout() {
  return (tree: any) => {
    visit(tree, 'blockquote', (node: any) => {
      const firstChild = node.children?.[0];
      if (!firstChild || firstChild.type !== 'paragraph') return;

      const textNode = firstChild.children?.[0];
      if (!textNode || textNode.type !== 'text') return;

      const match = CALLOUT_REGEX.exec(textNode.value);
      if (!match) return;

      const calloutType = match[1].toLowerCase();
      const title = match[2] || calloutType.charAt(0).toUpperCase() + calloutType.slice(1);
      const remaining = textNode.value.slice(match[0].length).replace(/^\n/, '');

      if (remaining) {
        textNode.value = remaining;
      } else {
        firstChild.children.shift();
        if (firstChild.children.length === 0) node.children.shift();
      }

      node.data = {
        hName: 'div',
        hProperties: {
          'data-callout': calloutType,
          'data-callout-title': title,
          className: 'callout',
        },
      };
    });
  };
}
