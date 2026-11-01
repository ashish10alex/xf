// Whether xf web's editors have vim's keys (the vimMode setting), and the
// words the page's messages use for saving, discarding and undoing in
// each mode. It imports nothing, so any module can ask it without loading
// the editor (editor.js, which switches the editors).

const onMac = /Mac|iPhone|iPad/.test(navigator.platform);
let on = false;

// vimOn says whether the editors have vim's keys.
export const vimOn = () => on;

// noteVimMode records the setting; editor.js's setVimMode switches the
// editors too.
export function noteVimMode(v) { on = !!v; }

// The keys that save, discard unsaved changes and undo, as a message
// names them.
export const savesWith = () => (on ? ':w' : onMac ? '⌘s' : 'ctrl+s');
export const discardsWith = () => (on ? ':e!' : (onMac ? '⇧⌘p' : 'ctrl+shift+p') + ' "discard unsaved changes"');
export const undoesWith = () => (on ? 'u' : onMac ? '⌘z' : 'ctrl+z');

// unsavedIn is the message for unsaved changes in file that stop what was
// asked; staleIn the one for a file changed on disk while they were made.
export const unsavedIn = (file) => 'unsaved changes in ' + file + ': ' + savesWith() + ' saves, ' + discardsWith() + ' discards';
export const staleIn = (file) => file + ' changed on disk: ' + savesWith() + ' to choose what to keep, ' + discardsWith() + ' discards';
