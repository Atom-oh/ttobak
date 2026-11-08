import assert from 'node:assert/strict';

export function accountComponentHarness() {
  const frames = new Map();
  let activeFrame;
  let cursor = 0;
  let dirty = false;
  let effects = [];
  let lateSets = 0;
  const same = (left, right) => left?.length === right?.length && left.every((value, index) => Object.is(value, right[index]));
  const react = {
    useState(initial) {
      const frame = activeFrame;
      const slot = cursor++;
      if (!(slot in frame.slots)) frame.slots[slot] = typeof initial === 'function' ? initial() : initial;
      return [frame.slots[slot], update => {
        if (!frame.live) { lateSets++; return; }
        const next = typeof update === 'function' ? update(frame.slots[slot]) : update;
        if (!Object.is(next, frame.slots[slot])) { frame.slots[slot] = next; dirty = true; }
      }];
    },
    useRef(initial) {
      const slot = cursor++;
      if (!(slot in activeFrame.slots)) activeFrame.slots[slot] = { current: initial };
      return activeFrame.slots[slot];
    },
    useMemo(callback, dependencies) {
      const slot = cursor++;
      if (!same(activeFrame.slots[slot]?.dependencies, dependencies)) activeFrame.slots[slot] = { dependencies, value: callback() };
      return activeFrame.slots[slot].value;
    },
    useCallback(callback, dependencies) { return react.useMemo(() => callback, dependencies); },
    useEffect(callback, dependencies) {
      const frame = activeFrame;
      const slot = cursor++;
      const previous = frame.slots[slot];
      if (same(previous?.dependencies, dependencies)) return;
      frame.slots[slot] = { dependencies, cleanup: previous?.cleanup };
      effects.push(() => {
        previous?.cleanup?.();
        if (frame.live) frame.slots[slot].cleanup = callback();
      });
    },
  };
  react.useLayoutEffect = react.useEffect;
  const release = frame => {
    frame.live = false;
    frame.slots.forEach(slot => slot?.cleanup?.());
  };
  function render(Component, stop = new Set(), props = {}) {
    let tree;
    let attempts = 0;
    do {
      assert.ok(attempts++ < 30, 'component renders settle');
      dirty = false;
      const seen = new Set();
      const expand = (node, path) => {
        if (!node || typeof node !== 'object') return node;
        if (Array.isArray(node)) return node.map((child, index) => expand(child, `${path}/${child?.key ?? index}`));
        if (typeof node.type === 'function' && !stop.has(node.type)) {
          const identity = `${path}/${node.type.name}/${node.key ?? ''}`;
          seen.add(identity);
          if (!frames.has(identity)) frames.set(identity, { slots: [], live: true });
          activeFrame = frames.get(identity);
          cursor = 0;
          return expand(node.type(node.props), `${identity}/output`);
        }
        return { ...node, props: { ...node.props, children: expand(node.props?.children, `${path}/children`) } };
      };
      tree = expand({ type: Component, props }, 'root');
      for (const [identity, frame] of frames) if (!seen.has(identity)) { release(frame); frames.delete(identity); }
      effects.splice(0).forEach(effect => effect());
    } while (dirty);
    return tree;
  }
  return {
    react, render, lateSets: () => lateSets,
    unmount() { frames.forEach(release); frames.clear(); effects = []; },
  };
}
