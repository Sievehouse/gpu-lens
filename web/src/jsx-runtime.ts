// Lightweight JSX + Hooks runtime for offline compilation in air-gapped environments.
// Allows authoring standard TypeScript + TSX components with `useState`, `useEffect`, `useRef` and `useMemo`.
//
// Rendering model: every render expands the component tree into plain host nodes (elements + text) and then
// patches the existing DOM in place. Elements are reused across renders, so keyboard focus, text selection,
// scroll offsets and CSS transitions survive a re-render — a requirement for the live telemetry console, which
// re-renders every few seconds. Hook state is scoped to each mounted component instance.

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

// ---------------------------------------------------------------------------------------------
// Component instances + hooks
// ---------------------------------------------------------------------------------------------

interface EffectSlot {
  deps: any[] | undefined;
  cleanup: (() => void) | undefined;
  hasRun: boolean;
}

interface ComponentInstance {
  fn: Function;
  hooks: any[];
  hookIndex: number;
  seenInRender: number;
  alive: boolean;
}

const instances = new Map<string, ComponentInstance>();
let currentInstance: ComponentInstance | null = null;
let renderCounter = 0;

let rootFactory: (() => VNode) | null = null;
let rootContainer: HTMLElement | null = null;
let renderScheduled = false;
let rendering = false;

function hookSlot(): { inst: ComponentInstance; idx: number } {
  if (!currentInstance) {
    throw new Error('Hooks can only be called while a component is rendering.');
  }
  return { inst: currentInstance, idx: currentInstance.hookIndex++ };
}

export function useState<T>(initial: T): [T, (next: T | ((prev: T) => T)) => void] {
  const { inst, idx } = hookSlot();
  if (inst.hooks.length <= idx) {
    inst.hooks[idx] = initial;
  }
  const setState = (next: T | ((prev: T) => T)) => {
    const prev = inst.hooks[idx];
    const resolved = typeof next === 'function' ? (next as (p: T) => T)(prev) : next;
    if (resolved !== prev) {
      inst.hooks[idx] = resolved;
      if (inst.alive) scheduleRender();
    }
  };
  return [inst.hooks[idx] as T, setState];
}

export function useRef<T>(initial: T): { current: T } {
  const { inst, idx } = hookSlot();
  if (inst.hooks.length <= idx) {
    inst.hooks[idx] = { current: initial };
  }
  return inst.hooks[idx];
}

export function useMemo<T>(factory: () => T, deps: any[]): T {
  const { inst, idx } = hookSlot();
  const slot = inst.hooks[idx] as { deps: any[]; value: T } | undefined;
  if (slot && slot.deps.length === deps.length && !deps.some((d, i) => d !== slot.deps[i])) {
    return slot.value;
  }
  const value = factory();
  inst.hooks[idx] = { deps, value };
  return value;
}

// Runs `effect` after the render in which `deps` changed (or after every render when `deps` is omitted).
// A function returned by `effect` is used as cleanup before the next run and when the component unmounts.
export function useEffect(effect: () => void | (() => void), deps?: any[]): void {
  const { inst, idx } = hookSlot();
  let slot = inst.hooks[idx] as EffectSlot | undefined;
  if (!slot) {
    slot = { deps: undefined, cleanup: undefined, hasRun: false };
    inst.hooks[idx] = slot;
  }
  const prevDeps = slot.deps;
  let changed = true;
  if (slot.hasRun && deps && prevDeps && deps.length === prevDeps.length) {
    changed = deps.some((d, i) => d !== prevDeps[i]);
  }
  if (changed) {
    slot.deps = deps;
    slot.hasRun = true;
    const target = slot;
    setTimeout(() => {
      if (!inst.alive) return;
      runCleanup(target);
      const result = effect();
      target.cleanup = typeof result === 'function' ? result : undefined;
    }, 0);
  }
}

function runCleanup(slot: EffectSlot): void {
  if (slot.cleanup) {
    const fn = slot.cleanup;
    slot.cleanup = undefined;
    try {
      fn();
    } catch (err) {
      console.error('effect cleanup failed', err);
    }
  }
}

function unmountInstance(inst: ComponentInstance): void {
  inst.alive = false;
  for (const slot of inst.hooks) {
    if (slot && typeof slot === 'object' && 'hasRun' in slot && 'cleanup' in slot) {
      runCleanup(slot as EffectSlot);
    }
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

// Lets non-component code (for example a polling data store) ask for a re-render after it mutated its state.
export function requestRender(): void {
  scheduleRender();
}

// ---------------------------------------------------------------------------------------------
// Expansion: component tree -> host tree
// ---------------------------------------------------------------------------------------------

const EL = 0;
const TEXT = 1;

interface HostNode {
  kind: typeof EL | typeof TEXT;
  tag: string;
  props: Record<string, any>;
  children: HostNode[];
  key: any;
  text: string;
  svg: boolean;
  dom: Node | null;
}

const NO_PROPS: Record<string, any> = {};
const NO_CHILDREN: HostNode[] = [];

function textNode(text: string): HostNode {
  return { kind: TEXT, tag: '', props: NO_PROPS, children: NO_CHILDREN, key: undefined, text, svg: false, dom: null };
}

const SVG_NS = 'http://www.w3.org/2000/svg';
const SVG_TAGS = new Set([
  'svg', 'path', 'line', 'rect', 'circle', 'ellipse', 'g', 'text', 'tspan', 'polyline', 'polygon', 'defs',
  'linearGradient', 'radialGradient', 'stop', 'clipPath', 'mask', 'pattern', 'use', 'symbol', 'marker',
]);
const SVG_ATTR_MAP: Record<string, string> = {
  viewBox: 'viewBox',
  strokeWidth: 'stroke-width',
  strokeDasharray: 'stroke-dasharray',
  strokeDashoffset: 'stroke-dashoffset',
  strokeLinecap: 'stroke-linecap',
  strokeLinejoin: 'stroke-linejoin',
  strokeOpacity: 'stroke-opacity',
  fillOpacity: 'fill-opacity',
  textAnchor: 'text-anchor',
  dominantBaseline: 'dominant-baseline',
  fontSize: 'font-size',
  fontWeight: 'font-weight',
  fontFamily: 'font-family',
  clipPath: 'clip-path',
  stopColor: 'stop-color',
  stopOpacity: 'stop-opacity',
  vectorEffect: 'vector-effect',
  pointerEvents: 'pointer-events',
};

function childPathSegment(child: VNodeChild, index: number): string {
  if (child && typeof child === 'object' && !Array.isArray(child) && child.props && child.props.key != null) {
    return 'k:' + String(child.props.key);
  }
  return String(index);
}

function expand(child: VNodeChild, path: string, out: HostNode[], isSvg: boolean): void {
  if (child === null || child === undefined || child === false || child === true) {
    out.push(textNode(''));
    return;
  }
  if (typeof child === 'string' || typeof child === 'number') {
    out.push(textNode(String(child)));
    return;
  }
  if (Array.isArray(child)) {
    for (let i = 0; i < child.length; i++) {
      expand(child[i], path + '/' + childPathSegment(child[i], i), out, isSvg);
    }
    return;
  }
  if (child.tag === Fragment) {
    for (let i = 0; i < child.children.length; i++) {
      expand(child.children[i], path + '/' + childPathSegment(child.children[i], i), out, isSvg);
    }
    return;
  }
  if (typeof child.tag === 'function') {
    const fn = child.tag as Function;
    let inst = instances.get(path);
    if (!inst || inst.fn !== fn) {
      if (inst) unmountInstance(inst);
      inst = { fn, hooks: [], hookIndex: 0, seenInRender: 0, alive: true };
      instances.set(path, inst);
    }
    inst.seenInRender = renderCounter;
    inst.hookIndex = 0;
    const outer = currentInstance;
    currentInstance = inst;
    let rendered: VNodeChild;
    try {
      rendered = fn({ ...child.props, children: child.children });
    } catch (err) {
      // Contain the failure to this component so the rest of the console (and its state) keeps working.
      console.error(`render failed in <${fn.name || 'component'}>`, err);
      rendered = renderErrorTree(err, fn.name);
    } finally {
      currentInstance = outer;
    }
    expand(rendered, path + '>', out, isSvg);
    return;
  }

  const tag = String(child.tag);
  const svg = isSvg || SVG_TAGS.has(tag);
  const node: HostNode = {
    kind: EL,
    tag,
    props: child.props || NO_PROPS,
    children: [],
    key: child.props ? child.props.key : undefined,
    text: '',
    svg,
    dom: null,
  };
  const childSvg = svg && tag !== 'foreignObject';
  for (let i = 0; i < child.children.length; i++) {
    expand(child.children[i], path + '/' + childPathSegment(child.children[i], i), node.children, childSvg);
  }
  out.push(node);
}

// ---------------------------------------------------------------------------------------------
// DOM creation + patching
// ---------------------------------------------------------------------------------------------

type HandlerMap = Record<string, EventListener | undefined>;

function handlersOf(el: Element): HandlerMap {
  const anyEl = el as any;
  if (!anyEl.__glHandlers) anyEl.__glHandlers = {};
  return anyEl.__glHandlers as HandlerMap;
}

function setHandler(el: Element, evName: string, fn: EventListener | undefined): void {
  const map = handlersOf(el);
  if (!(evName in map)) {
    el.addEventListener(evName, (ev: Event) => {
      const current = handlersOf(el)[evName];
      if (current) current.call(el, ev);
    });
  }
  map[evName] = fn;
}

function isHandlerProp(key: string, value: any): boolean {
  return key.length > 2 && key.charCodeAt(0) === 111 && key.charCodeAt(1) === 110 && typeof value === 'function';
}

function applyProp(el: Element, key: string, value: any, svg: boolean, isUpdate: boolean): void {
  if (key === 'ref') return;
  if (key === 'className') {
    el.setAttribute('class', String(value));
  } else if (key === 'style' && typeof value === 'object') {
    Object.assign((el as HTMLElement).style, value);
  } else if (isHandlerProp(key, value)) {
    setHandler(el, key.slice(2).toLowerCase(), value);
  } else if (!svg && key === 'value' && 'value' in el) {
    const str = String(value);
    const control = el as HTMLInputElement | HTMLSelectElement;
    if (!isUpdate || control.value !== str) control.value = str;
    if (el.getAttribute('value') !== str) el.setAttribute('value', str);
  } else if (!svg && key === 'selected' && value === true) {
    (el as HTMLOptionElement).selected = true;
    el.setAttribute('selected', 'selected');
  } else if (!svg && key === 'checked' && 'checked' in el) {
    (el as HTMLInputElement).checked = Boolean(value);
    if (value) el.setAttribute('checked', 'checked');
    else el.removeAttribute('checked');
  } else {
    const attrName = svg && SVG_ATTR_MAP[key] ? SVG_ATTR_MAP[key] : key;
    const str = String(value);
    if (!isUpdate || el.getAttribute(attrName) !== str) el.setAttribute(attrName, str);
  }
}

function removeProp(el: Element, key: string, oldValue: any, svg: boolean): void {
  if (key === 'ref') return;
  if (key === 'className') {
    el.removeAttribute('class');
  } else if (key === 'style' && typeof oldValue === 'object') {
    el.removeAttribute('style');
  } else if (isHandlerProp(key, oldValue)) {
    setHandler(el, key.slice(2).toLowerCase(), undefined);
  } else if (!svg && key === 'selected') {
    (el as HTMLOptionElement).selected = false;
    el.removeAttribute('selected');
  } else if (!svg && key === 'checked' && 'checked' in el) {
    (el as HTMLInputElement).checked = false;
    el.removeAttribute('checked');
  } else {
    el.removeAttribute(svg && SVG_ATTR_MAP[key] ? SVG_ATTR_MAP[key] : key);
  }
}

function isAbsent(v: any): boolean {
  return v === undefined || v === null || v === false;
}

function createDom(node: HostNode): Node {
  if (node.kind === TEXT) {
    return document.createTextNode(node.text);
  }
  const el = node.svg ? document.createElementNS(SVG_NS, node.tag) : document.createElement(node.tag);
  for (const k in node.props) {
    const v = node.props[k];
    if (isAbsent(v)) continue;
    applyProp(el, k, v, node.svg, false);
  }
  patchChildren(el, NO_CHILDREN, node.children);
  finishElement(el, node);
  return el;
}

function finishElement(el: Element, node: HostNode): void {
  if (node.tag === 'select' && node.props.value !== undefined) {
    const str = String(node.props.value);
    if ((el as HTMLSelectElement).value !== str) (el as HTMLSelectElement).value = str;
  }
  if (typeof node.props.ref === 'function') {
    node.props.ref(el);
  }
}

function patchProps(el: Element, oldProps: Record<string, any>, newProps: Record<string, any>, svg: boolean): void {
  for (const k in oldProps) {
    if (isAbsent(oldProps[k])) continue;
    if (!(k in newProps) || isAbsent(newProps[k])) {
      removeProp(el, k, oldProps[k], svg);
    }
  }
  for (const k in newProps) {
    const nv = newProps[k];
    if (isAbsent(nv)) continue;
    const ov = oldProps[k];
    if (k === 'style' && typeof nv === 'object') {
      const style = (el as HTMLElement).style as any;
      if (ov && typeof ov === 'object') {
        for (const sk in ov) {
          if (!(sk in nv)) style[sk] = '';
        }
        for (const sk in nv) {
          if (ov[sk] !== nv[sk]) style[sk] = nv[sk];
        }
      } else {
        Object.assign(style, nv);
      }
      continue;
    }
    if (isHandlerProp(k, nv)) {
      if (ov !== nv) setHandler(el, k.slice(2).toLowerCase(), nv);
      continue;
    }
    if (ov === nv && k !== 'value' && k !== 'checked' && k !== 'selected') continue;
    applyProp(el, k, nv, svg, true);
  }
}

function sameKind(a: HostNode, b: HostNode): boolean {
  return a.kind === b.kind && a.tag === b.tag && a.svg === b.svg;
}

function patchChildren(parent: Node, oldKids: HostNode[], newKids: HostNode[]): void {
  let keyed: Map<any, HostNode> | null = null;
  for (let i = 0; i < oldKids.length; i++) {
    const o = oldKids[i];
    if (o.kind === EL && o.key != null) {
      if (!keyed) keyed = new Map();
      if (!keyed.has(o.key)) keyed.set(o.key, o);
    }
  }

  const matches: (HostNode | null)[] = new Array(newKids.length);
  let used: Set<HostNode> | null = null;
  for (let i = 0; i < newKids.length; i++) {
    const n = newKids[i];
    let match: HostNode | null = null;
    if (keyed && n.kind === EL && n.key != null) {
      const cand = keyed.get(n.key);
      if (cand && sameKind(cand, n) && !(used && used.has(cand))) match = cand;
    }
    if (!match && i < oldKids.length) {
      const cand = oldKids[i];
      const keyCompatible = cand.kind === TEXT || cand.key == null || cand.key === n.key;
      if (keyCompatible && sameKind(cand, n) && !(used && used.has(cand))) match = cand;
    }
    if (match) {
      if (!used) used = new Set();
      used.add(match);
    }
    matches[i] = match;
  }

  for (let i = 0; i < oldKids.length; i++) {
    const o = oldKids[i];
    if (!(used && used.has(o)) && o.dom && o.dom.parentNode === parent) {
      parent.removeChild(o.dom);
    }
  }

  let cursor: Node | null = parent.firstChild;
  for (let i = 0; i < newKids.length; i++) {
    const n = newKids[i];
    const m = matches[i];
    let dom: Node;
    if (m && m.dom) {
      dom = m.dom;
      n.dom = dom;
      if (n.kind === TEXT) {
        if (m.text !== n.text) dom.nodeValue = n.text;
      } else {
        patchProps(dom as Element, m.props, n.props, n.svg);
        patchChildren(dom, m.children, n.children);
        finishElement(dom as Element, n);
      }
    } else {
      dom = createDom(n);
      n.dom = dom;
    }
    if (dom === cursor) {
      cursor = cursor.nextSibling;
    } else {
      parent.insertBefore(dom, cursor);
    }
  }
}

// ---------------------------------------------------------------------------------------------
// Root render loop
// ---------------------------------------------------------------------------------------------

let previousTree: HostNode[] = [];

function renderErrorTree(err: any, where?: string): VNode {
  const message = err && err.message ? String(err.message) : String(err);
  return h(
    'div',
    { className: 'main-container' },
    h(
      'div',
      { className: 'panel', style: { borderColor: 'var(--status-critical)' } },
      h('div', { className: 'panel-title', style: { color: '#fca5a5' } }, where ? `Console Render Error in ${where}` : 'Console Render Error'),
      h('p', { className: 'mono', style: { marginTop: '8px' } }, message),
      h('p', { style: { marginTop: '8px', color: 'var(--text-secondary)' } }, 'Reload the page to retry. The backend keeps running.')
    )
  );
}

function performRender(): void {
  if (!rootContainer || !rootFactory || rendering) return;
  rendering = true;
  try {
    renderCounter++;
    let next: HostNode[] = [];
    try {
      expand(rootFactory(), 'r', next, false);
    } catch (err) {
      console.error('render failed', err);
      currentInstance = null;
      next = [];
      expand(renderErrorTree(err), 'error', next, false);
    }
    for (const [path, inst] of instances) {
      if (inst.seenInRender !== renderCounter) {
        unmountInstance(inst);
        instances.delete(path);
      }
    }
    patchChildren(rootContainer, previousTree, next);
    previousTree = next;
  } finally {
    rendering = false;
  }
}

export function mountApp(factory: () => VNode, container: HTMLElement): void {
  rootFactory = factory;
  rootContainer = container;
  container.innerHTML = '';
  previousTree = [];
  performRender();
}
