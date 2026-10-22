// Lightweight, deterministic JSX + Hooks runtime for offline compilation in air-gapped environments.
// Allows authoring standard TypeScript + TSX components (`App.tsx`) with `useState` and `useEffect`.

type VNodeChild = VNode | string | number | boolean | null | undefined | VNodeChild[];

export interface VNode {
  tag: any;
  props: Record<string, any>;
  children: VNodeChild[];
}

export const Fragment = '__FRAGMENT__';

export function h(tag: any, props: Record<string, any> | null, ...children: VNodeChild[]): VNode {
  return {
    tag,
    props: props || {},
    children,
  };
}

let hookStates: any[] = [];
let hookDeps: (any[] | undefined)[] = [];
let hookIndex = 0;
let rootFactory: (() => VNode) | null = null;
let rootContainer: HTMLElement | null = null;
let renderScheduled = false;

export function useState<T>(initial: T): [T, (next: T | ((prev: T) => T)) => void] {
  const idx = hookIndex++;
  if (hookStates.length <= idx) {
    hookStates[idx] = initial;
  }
  const setState = (next: T | ((prev: T) => T)) => {
    const prev = hookStates[idx];
    const resolved = typeof next === 'function' ? (next as (p: T) => T)(prev) : next;
    if (resolved !== prev) {
      hookStates[idx] = resolved;
      scheduleRender();
    }
  };
  return [hookStates[idx] as T, setState];
}

export function useEffect(effect: () => void, deps?: any[]): void {
  const idx = hookIndex++;
  const prevDeps = hookDeps[idx];
  let changed = true;
  if (deps && prevDeps && deps.length === prevDeps.length) {
    changed = deps.some((d, i) => d !== prevDeps[i]);
  }
  if (changed) {
    hookDeps[idx] = deps;
    setTimeout(() => {
      effect();
    }, 0);
  }
}

function scheduleRender(): void {
  if (renderScheduled) return;
  renderScheduled = true;
  Promise.resolve().then(() => {
    renderScheduled = false;
    performRender();
  });
}

const SVG_NS = 'http://www.w3.org/2000/svg';
const SVG_TAGS = new Set(['svg', 'path', 'line', 'rect', 'circle', 'g', 'text', 'polyline', 'polygon', 'defs', 'linearGradient', 'stop']);
const SVG_ATTR_MAP: Record<string, string> = {
  viewBox: 'viewBox',
  strokeWidth: 'stroke-width',
  strokeDasharray: 'stroke-dasharray',
  textAnchor: 'text-anchor',
  fontSize: 'font-size',
  fontWeight: 'font-weight',
  fontFamily: 'font-family',
};

function createDOMNode(node: VNodeChild, isSvg = false): Node {
  if (node === null || node === undefined || node === false || node === true) {
    return document.createTextNode('');
  }
  if (typeof node === 'string' || typeof node === 'number') {
    return document.createTextNode(String(node));
  }
  if (Array.isArray(node)) {
    const frag = document.createDocumentFragment();
    for (const child of node) {
      frag.appendChild(createDOMNode(child, isSvg));
    }
    return frag;
  }
  if (node.tag === Fragment) {
    const frag = document.createDocumentFragment();
    for (const child of node.children) {
      frag.appendChild(createDOMNode(child, isSvg));
    }
    return frag;
  }
  if (typeof node.tag === 'function') {
    const rendered = node.tag({ ...node.props, children: node.children });
    return createDOMNode(rendered, isSvg);
  }

  const tagStr = String(node.tag);
  const useSvg = isSvg || SVG_TAGS.has(tagStr);
  const el = useSvg
    ? document.createElementNS(SVG_NS, tagStr)
    : document.createElement(tagStr);

  for (const [k, v] of Object.entries(node.props || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'className') {
      el.setAttribute('class', String(v));
    } else if (k === 'style' && typeof v === 'object') {
      Object.assign((el as HTMLElement).style, v);
    } else if (k.startsWith('on') && typeof v === 'function') {
      const evName = k.slice(2).toLowerCase();
      el.addEventListener(evName, v);
    } else if (!useSvg && k === 'value' && ('value' in el)) {
      (el as HTMLInputElement | HTMLSelectElement).value = String(v);
      el.setAttribute('value', String(v));
    } else if (!useSvg && k === 'selected' && v === true) {
      (el as HTMLOptionElement).selected = true;
      el.setAttribute('selected', 'selected');
    } else {
      const attrName = useSvg && SVG_ATTR_MAP[k] ? SVG_ATTR_MAP[k] : k;
      el.setAttribute(attrName, String(v));
    }
  }

  for (const child of node.children) {
    el.appendChild(createDOMNode(child, useSvg));
  }

  if (tagStr === 'select' && node.props && node.props.value !== undefined) {
    (el as HTMLSelectElement).value = String(node.props.value);
  }

  return el;
}

function performRender(): void {
  if (!rootContainer || !rootFactory) return;
  hookIndex = 0;
  const vtree = rootFactory();
  rootContainer.innerHTML = '';
  rootContainer.appendChild(createDOMNode(vtree, false));
}

export function mountApp(factory: () => VNode, container: HTMLElement): void {
  rootFactory = factory;
  rootContainer = container;
  performRender();
}
