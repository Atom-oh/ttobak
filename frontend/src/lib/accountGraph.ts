import type { AccountGraph } from '@/types/meeting';

const MAX_LABEL_CODE_POINTS = 60;

/** Escapes a label for a quoted mermaid node text: no newlines or angle brackets, quotes as entities, bounded length. */
export function escapeLabel(label: string): string {
  const cleaned = Array.from(String(label ?? '')
    .replace(/[\r\n]+/g, ' ')
    .replace(/[<>`%{}]/g, '')
    .replace(/"/g, '#quot;')
    .trim());
  // Count code points of the escaped text; never cut inside an entity.
  let out = '';
  let count = 0;
  for (let i = 0; i < cleaned.length && count < MAX_LABEL_CODE_POINTS; i++) {
    if (cleaned[i] === '#' && cleaned.slice(i, i + 6).join('') === '#quot;') {
      out += '#quot;';
      i += 5;
    } else {
      out += cleaned[i];
    }
    count++;
  }
  return out;
}

function shape(kind: string, text: string): string {
  switch (kind) {
    case 'account': return `(("${text}"))`;
    case 'project': return `["${text}"]`;
    case 'meeting': return `(["${text}"])`;
    case 'news':
    case 'tech': return `>"${text}"]`;
    case 'member':
    case 'contact': return `{{"${text}"}}`;
    default: return `["${text}"]`;
  }
}

/** Builds a mermaid flowchart. Node ids are `n{index}` only; raw ids never reach the source. */
export function toMermaid(graph: AccountGraph): string {
  const lines = ['flowchart LR'];
  const index = new Map<string, string>();
  (graph?.nodes ?? []).forEach((node, i) => {
    const id = `n${i}`;
    index.set(node.id, id);
    lines.push(`  ${id}${shape(node.kind, escapeLabel(node.label))}`);
  });
  for (const edge of graph?.edges ?? []) {
    const from = index.get(edge.from);
    const to = index.get(edge.to);
    if (!from || !to) continue;
    lines.push(`  ${from} ${edge.origin === 'auto' ? '-.->' : '-->'} ${to}`);
  }
  return lines.join('\n');
}
