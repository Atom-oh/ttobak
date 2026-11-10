import type { AccountGraph } from '@/types/meeting';
import { roleLabel } from './accountPeople';

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
    const auto = edge.origin === 'auto';
    const label = edge.kind === 'person' ? escapeLabel(roleLabel(edge.role)) : '';
    const arrow = label ? (auto ? `-. "${label}" .->` : `-- "${label}" -->`) : (auto ? '-.->' : '-->');
    lines.push(`  ${from} ${arrow} ${to}`);
  }
  return lines.join('\n');
}

/** Korean note for the graph: only members with a people edge are drawn. */
export function memberCountNote(memberCount: number | undefined | null): string {
  const count = Number(memberCount) || 0;
  return count > 0 ? `어카운트 멤버 ${count}명 중 미팅에 연결된 멤버만 표시됩니다` : '';
}
