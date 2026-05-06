// Mock of ../src/cli for unit tests. The real cli.ts spawns the moorpost
// binary; tests inject a fixture by mutating __setStatus/__setCwd.
let _status = null;
let _cwd = '/fake/cwd';

export function __setStatus(s) {
  _status = s;
}
export function __setCwd(c) {
  _cwd = c;
}

export function workspaceRoot() {
  return _cwd;
}

export async function getStatus(_cwd) {
  return _status;
}

export async function runJSON() {
  throw new Error('runJSON should not be called in tests');
}
