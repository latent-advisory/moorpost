// Bundle src/extension.ts into a single dist/extension.js consumable by VSCode.
// VSCode loads extensions as CommonJS, with `vscode` resolved at runtime via
// a host-provided require — so we mark it external.

const esbuild = require('esbuild');

const production = process.argv.includes('--production');
const watch = process.argv.includes('--watch');

const baseConfig = {
  entryPoints: ['src/extension.ts'],
  bundle: true,
  format: 'cjs',
  platform: 'node',
  target: 'node18',
  outfile: 'dist/extension.js',
  external: ['vscode'],
  sourcemap: !production,
  minify: production,
  logLevel: 'info',
};

async function main() {
  if (watch) {
    const ctx = await esbuild.context(baseConfig);
    await ctx.watch();
    console.log('esbuild: watching for changes...');
  } else {
    await esbuild.build(baseConfig);
    console.log('esbuild: built dist/extension.js');
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
