import type { AccountSummary } from '@/types/meeting';

export const MAX_ACCOUNT_FILTERS = 100;

export interface AccountNode extends AccountSummary {
  children: AccountNode[];
}

export interface AccountTreeRow {
  node: AccountNode;
  depth: number;
  path: string;
  ancestorIds: string[];
}

/** Only caller-visible records are linked; missing/cyclic parents become roots. */
export function buildAccountTree(accounts: AccountSummary[]): AccountNode[] {
  const nodes = new Map(accounts.map(account => [account.accountId, { ...account, children: [] } as AccountNode]));
  const roots: AccountNode[] = [];
  for (const node of nodes.values()) {
    let parent = node.parentAccountId ? nodes.get(node.parentAccountId) : undefined;
    const seen = new Set([node.accountId]);
    let cursor = parent;
    while (cursor) {
      if (seen.has(cursor.accountId)) {
        parent = undefined;
        break;
      }
      seen.add(cursor.accountId);
      cursor = cursor.parentAccountId ? nodes.get(cursor.parentAccountId) : undefined;
    }
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  const compare = (a: AccountNode, b: AccountNode) =>
    a.name.localeCompare(b.name, 'ko') || a.accountId.localeCompare(b.accountId);
  for (const node of nodes.values()) node.children.sort(compare);
  return roots.sort(compare);
}

export function flattenAccountTree(nodes: AccountNode[]): AccountTreeRow[] {
  const rows: AccountTreeRow[] = [];
  const visit = (node: AccountNode, depth: number, names: string[], ancestorIds: string[]) => {
    const path = [...names, node.name];
    rows.push({ node, depth, path: path.join(' / '), ancestorIds });
    for (const child of node.children) visit(child, depth + 1, path, [...ancestorIds, node.accountId]);
  };
  for (const node of nodes) visit(node, 0, [], []);
  return rows;
}

export function accountSubtreeIds(node: AccountNode): string[] {
  const ids: string[] = [];
  const pending = [node];
  while (pending.length) {
    const current = pending.pop()!;
    ids.push(current.accountId);
    pending.push(...current.children);
  }
  return ids;
}

export function accountSelectionChips(tree: AccountNode[], selectedIds: string[]) {
  const selected = new Set(selectedIds);
  const known = new Set<string>();
  const chips: { key: string; label: string; ids: string[] }[] = [];
  const visit = (node: AccountNode) => {
    const ids = accountSubtreeIds(node);
    ids.forEach(id => known.add(id));
    if (ids.every(id => selected.has(id))) {
      chips.push({ key: node.accountId, label: node.name, ids });
      return;
    }
    if (selected.has(node.accountId)) {
      chips.push({ key: node.accountId, label: `${node.name}${node.children.length ? ' (직접)' : ''}`, ids: [node.accountId] });
    }
    node.children.forEach(visit);
  };
  tree.forEach(visit);
  selectedIds.filter(id => !known.has(id)).forEach(id => {
    chips.push({ key: id, label: '목록에 없는 어카운트', ids: [id] });
  });
  return chips;
}
