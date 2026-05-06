// Node loader hook that aliases the vscode API and the cli/sessionList
// modules to test shims, so the treeView module under test can be
// imported without the real vscode runtime or the moorpost CLI binary.

import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, resolve as resolvePath } from 'node:path';
import { existsSync } from 'node:fs';

const here = dirname(fileURLToPath(import.meta.url));

const aliases = new Map([
  ['vscode', resolvePath(here, 'vscode-shim.mjs')],
]);

// Files in src/ that import sibling modules — redirect those siblings.
// Keys are basenames the source code imports (without extension).
const siblingShims = new Map([
  ['cli', resolvePath(here, 'cli-shim.mjs')],
  ['sessionList', resolvePath(here, 'sessionList-shim.mjs')],
]);

export async function resolve(specifier, context, nextResolve) {
  // Bare module 'vscode' → shim
  if (aliases.has(specifier)) {
    return {
      url: pathToFileURL(aliases.get(specifier)).href,
      shortCircuit: true,
      format: 'module',
    };
  }

  // Relative imports from inside src/treeView.ts ('./cli', './sessionList')
  if (specifier.startsWith('./') || specifier.startsWith('../')) {
    // Strip optional .js/.ts extension and any subpath segments
    const stem = specifier.replace(/^\.\.?\//, '').replace(/\.(m?[jt]s)$/, '');
    if (siblingShims.has(stem)) {
      return {
        url: pathToFileURL(siblingShims.get(stem)).href,
        shortCircuit: true,
        format: 'module',
      };
    }
  }

  // tsconfig uses Node16 module resolution which requires explicit '.js'
  // extensions even for TypeScript siblings. At test time we strip-types
  // the .ts source directly, so map './foo.js' → './foo.ts' when the .ts
  // exists alongside the missing .js.
  if (
    (specifier.startsWith('./') || specifier.startsWith('../')) &&
    specifier.endsWith('.js') &&
    context.parentURL
  ) {
    const parentDir = dirname(fileURLToPath(context.parentURL));
    const jsPath = resolvePath(parentDir, specifier);
    if (!existsSync(jsPath)) {
      const tsPath = jsPath.replace(/\.js$/, '.ts');
      if (existsSync(tsPath)) {
        return {
          url: pathToFileURL(tsPath).href,
          shortCircuit: true,
          format: 'module-typescript',
        };
      }
    }
  }

  return nextResolve(specifier, context);
}
