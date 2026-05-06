// Registers the test loader hook with Node's module-resolution pipeline.
// Used as: node --import ./test/loader-register.mjs ...
import { register } from 'node:module';
register('./loader.mjs', import.meta.url);
