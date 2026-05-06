// Minimal vscode API shim for unit tests.
// Only the surface the tree-view code touches is implemented.

export class ThemeIcon {
  constructor(id, color) {
    this.id = id;
    if (color !== undefined) this.color = color;
  }
}

export const TreeItemCollapsibleState = Object.freeze({
  None: 0,
  Collapsed: 1,
  Expanded: 2,
});

export class TreeItem {
  constructor(label, collapsibleState) {
    this.label = label;
    this.collapsibleState = collapsibleState ?? TreeItemCollapsibleState.None;
  }
}

export class EventEmitter {
  constructor() {
    this._listeners = [];
    this.event = (listener) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
  }
  fire(arg) {
    for (const l of this._listeners) l(arg);
  }
  dispose() {
    this._listeners = [];
  }
}

export const window = {
  showInformationMessage: () => Promise.resolve(undefined),
  showErrorMessage: () => Promise.resolve(undefined),
};

export const workspace = {
  workspaceFolders: undefined,
  getConfiguration: () => ({ get: () => undefined }),
};

export const Uri = {
  file: (p) => ({ fsPath: p, scheme: 'file' }),
};

export default {
  ThemeIcon,
  TreeItemCollapsibleState,
  TreeItem,
  EventEmitter,
  window,
  workspace,
  Uri,
};
