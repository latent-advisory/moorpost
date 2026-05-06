// Mock of ../src/sessionList for unit tests.
let _sessions = [];

export function __setSessions(s) {
  _sessions = s;
}

export async function listLocalSessions(_cwd) {
  return _sessions;
}

// Stub the other exports — unused in treeView.test but kept for type parity.
export function encodeCwd(s) {
  return s.replace(/[^a-zA-Z0-9-]/g, '-');
}
export function sessionsDir(s) {
  return s;
}
