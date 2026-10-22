#!/usr/bin/env node
/**
 * Offline TypeScript + TSX Compiler & Bundler for GPU Lens (`web/`).
 * Uses the system-installed `@babel/core`, `@babel/preset-typescript`, and `@babel/preset-react`
 * to compile `src/types.ts`, `src/jsx-runtime.ts`, `src/api.ts`, `src/App.tsx`, and `src/main.tsx`
 * into a self-contained browser bundle in `web/dist/bundle.js` + `web/dist/styles.css` + `web/dist/index.html`.
 */
const fs = require('fs');
const path = require('path');
const babel = require('/usr/share/nodejs/@babel/core');

const verifyOnly = process.argv.includes('--verify-only');
const webRoot = path.resolve(__dirname, '..');
const srcDir = path.join(webRoot, 'src');
const distDir = path.join(webRoot, 'dist');

const modulesOrder = [
  'types.ts',
  'jsx-runtime.ts',
  'api.ts',
  'App.tsx',
  'main.tsx',
];

const compiledModules = {};

for (const file of modulesOrder) {
  const fullPath = path.join(srcDir, file);
  const rawCode = fs.readFileSync(fullPath, 'utf8');
  const result = babel.transformSync(rawCode, {
    filename: fullPath,
    presets: [
      [
        '/usr/share/nodejs/@babel/preset-typescript',
        { isTSX: file.endsWith('.tsx'), allExtensions: true, jsxPragma: 'h', jsxPragmaFrag: 'Fragment' },
      ],
      ['/usr/share/nodejs/@babel/preset-react', { pragma: 'h', pragmaFrag: 'Fragment' }],
    ],
    plugins: [
      '/usr/share/nodejs/@babel/plugin-transform-modules-commonjs',
    ],
  });
  if (!result || typeof result.code !== 'string') {
    throw new Error(`Failed to compile ${file}`);
  }
  const modKey = './' + file.replace(/\.(ts|tsx)$/, '');
  compiledModules[modKey] = result.code;
  console.log(`[TypeScript/TSX Check] Successfully compiled ${file} (${result.code.length} bytes)`);
}

if (verifyOnly) {
  console.log('Frontend TypeScript/TSX verification passed.');
  process.exit(0);
}

fs.mkdirSync(distDir, { recursive: true });

let bundle = `(function() {\n  const __modules = {};\n  const __cache = {};\n  function __require(name) {\n    if (__cache[name]) return __cache[name].exports;\n    const fn = __modules[name];\n    if (!fn) throw new Error("Module not found: " + name);\n    const mod = { exports: {} };\n    __cache[name] = mod;\n    fn(mod, mod.exports, __require);\n    return mod.exports;\n  }\n`;

for (const [modKey, code] of Object.entries(compiledModules)) {
  bundle += `  __modules[${JSON.stringify(modKey)}] = function(module, exports, require) {\n${code}\n  };\n`;
}

bundle += `  const rt = __require("./jsx-runtime");\n  window.h = rt.h;\n  window.Fragment = rt.Fragment;\n  __require("./main");\n})();\n`;

fs.writeFileSync(path.join(distDir, 'bundle.js'), bundle, 'utf8');
fs.copyFileSync(path.join(srcDir, 'styles.css'), path.join(distDir, 'styles.css'));
fs.copyFileSync(path.join(webRoot, 'index.html'), path.join(distDir, 'index.html'));

console.log(`Built production frontend bundle in ${distDir}/bundle.js (${bundle.length} bytes)`);
