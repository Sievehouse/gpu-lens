// JSX typing for the built-in runtime (`h` / `Fragment` from ./jsx-runtime).
// Intrinsic elements are intentionally permissive: the runtime forwards any prop as a DOM attribute.
declare namespace JSX {
  type Element = import('./jsx-runtime').VNode;
  interface IntrinsicElements {
    [elemName: string]: any;
  }
  interface IntrinsicAttributes {
    key?: string | number;
  }
  interface ElementChildrenAttribute {
    children: {};
  }
}
