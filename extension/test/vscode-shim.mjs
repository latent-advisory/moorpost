// Minimal vscode API shim for unit tests.
// Only the surface the extension's unit tests touch is implemented.

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

export const ConfigurationTarget = Object.freeze({
  Global: 1,
  Workspace: 2,
  WorkspaceFolder: 3,
});

export const ProgressLocation = Object.freeze({
  SourceControl: 1,
  Window: 10,
  Notification: 15,
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

// Tracks the most-recent calls so tests can assert on them.
export const __callLog = {
  showInformationMessage: [],
  showErrorMessage: [],
  withProgress: [],
  openExternal: [],
  configUpdate: [],
};

export function __resetCallLog() {
  for (const k of Object.keys(__callLog)) __callLog[k] = [];
}

export const window = {
  showInformationMessage: (...args) => {
    __callLog.showInformationMessage.push(args);
    return Promise.resolve(undefined);
  },
  showErrorMessage: (...args) => {
    __callLog.showErrorMessage.push(args);
    // Default: pretend the user dismissed (no button click).
    return Promise.resolve(undefined);
  },
  withProgress: async (options, body) => {
    __callLog.withProgress.push({ options });
    // Run the body with a no-op progress reporter and a never-cancelling token.
    const progress = { report: () => {} };
    const token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose: () => {} }) };
    return body(progress, token);
  },
};

export const env = {
  openExternal: (uri) => {
    __callLog.openExternal.push(uri);
    return Promise.resolve(true);
  },
};

// Mutable config store: tests can pre-populate values via __setConfig().
const __configValues = new Map();
export function __setConfig(section, key, value) {
  __configValues.set(`${section}.${key}`, value);
}
export function __resetConfig() {
  __configValues.clear();
}

export const workspace = {
  workspaceFolders: undefined,
  getConfiguration: (section) => ({
    get: (key) => __configValues.get(`${section}.${key}`),
    update: (key, value, target) => {
      __callLog.configUpdate.push({ section, key, value, target });
      __configValues.set(`${section}.${key}`, value);
      return Promise.resolve();
    },
  }),
};

export const Uri = {
  file: (p) => ({ fsPath: p, scheme: 'file' }),
  parse: (s) => ({ toString: () => s, scheme: 'https' }),
};

export default {
  ThemeIcon,
  TreeItemCollapsibleState,
  ConfigurationTarget,
  ProgressLocation,
  TreeItem,
  EventEmitter,
  window,
  env,
  workspace,
  Uri,
};
