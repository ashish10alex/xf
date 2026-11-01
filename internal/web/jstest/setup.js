// The few browser globals xf web's modules touch when they load, so node
// can import them for tests: no layout, no events fired, nothing drawn.
// Tests of logic need nothing more; a test that needs more sets it up.

const listeners = () => ({ addEventListener() {}, removeEventListener() {} });

const element = () => ({
  ...listeners(),
  style: {}, dataset: {}, children: [], classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
  setAttribute() {}, getAttribute: () => null, removeAttribute() {},
  appendChild(c) { return c; }, removeChild(c) { return c; }, append() {}, remove() {},
  querySelector: () => null, querySelectorAll: () => [], closest: () => null, contains: () => false,
  getBoundingClientRect: () => ({ top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0 }),
  focus() {}, blur() {},
});

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
  clear: () => { store.clear(); },
};
globalThis.sessionStorage = globalThis.localStorage;

globalThis.document = {
  ...listeners(),
  hidden: false,
  documentElement: element(),
  body: element(),
  head: element(),
  activeElement: null,
  createElement: element,
  createTextNode: () => element(),
  getElementById: () => null,
  querySelector: () => null,
  querySelectorAll: () => [],
};

// The same platform on every OS the tests run on: not a Mac, so modifier
// keys are ctrl's (onMac in state.js and editor.js).
Object.defineProperty(globalThis, 'navigator', { value: { platform: 'Linux x86_64', userAgent: 'node' }, configurable: true });

globalThis.window = globalThis;
Object.assign(globalThis, listeners());
globalThis.matchMedia = () => ({ matches: false, ...listeners() });
globalThis.location = new URL('http://127.0.0.1/');
globalThis.HTMLElement = class HTMLElement {};
globalThis.requestAnimationFrame = (f) => setTimeout(f, 0);
globalThis.cancelAnimationFrame = (id) => clearTimeout(id);

// Modules start polls and open channels when they load (state.js's
// unsaved report, windows.js's channel to other windows); they mustn't
// keep a test file's process alive once its tests end.
const setInterval = globalThis.setInterval;
globalThis.setInterval = (...args) => {
  const t = setInterval(...args);
  t.unref();
  return t;
};
globalThis.BroadcastChannel = class extends BroadcastChannel {
  constructor(name) {
    super(name);
    this.unref();
  }
};
