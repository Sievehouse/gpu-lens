#!/usr/bin/env node
/**
 * Offline TypeScript + TSX compiler & bundler for the GPU Lens operator console (`web/`).
 *
 * Compiles every `src/**\/*.ts(x)` module to CommonJS and wraps them in one self-contained browser
 * bundle: `dist/bundle.js` + `dist/*.css` + `dist/index.html`. No network access is needed at build time.
 *
 * The compiler is resolved from whatever is installed, in this order:
 *   1. `typescript`            (web/node_modules after `npm install`, or a global install)
 *   2. `@babel/core` + presets (web/node_modules)
 *   3. Debian/Ubuntu system Babel packages under /usr/share/nodejs (the original build path)
 *
 * Usage:
 *   node scripts/build_offline.js                build into web/dist
 *   node scripts/build_offline.js --verify-only  compile every module, write nothing
 *   node scripts/build_offline.js --watch        rebuild whenever a file in src/ changes
 *   node scripts/build_offline.js --if-available exit 0 (keeping the committed dist) when no compiler is installed
 */
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const args = new Set(process.argv.slice(2));
const verifyOnly = args.has('--verify-only');
const watchMode = args.has('--watch');
const ifAvailable = args.has('--if-available');

const webRoot = path.resolve(__dirname, '..');
const srcDir = path.join(webRoot, 'src');
const distDir = path.join(webRoot, 'dist');
const ENTRY_MODULE = 'main';

// ---------------------------------------------------------------------------------------------
// Compiler discovery
// ---------------------------------------------------------------------------------------------

function tryResolve(request, extraPaths) {
  const searchPaths = [webRoot, ...(extraPaths || []), ...(require.resolve.paths(request) || [])];
  try {
    return require.resolve(request, { paths: searchPaths });
  } catch {
    return null;
  }
}

function findCompiler() {
  const tsPath = tryResolve('typescript');
  if (tsPath) {
    const ts = require(tsPath);
    return {
      name: `typescript ${ts.version}`,
      compile(code, fileName) {
        const out = ts.transpileModule(code, {
          fileName,
          reportDiagnostics: true,
          compilerOptions: {
            module: ts.ModuleKind.CommonJS,
            target: ts.ScriptTarget.ES2020,
            jsx: ts.JsxEmit.React,
            jsxFactory: 'h',
            jsxFragmentFactory: 'Fragment',
            isolatedModules: true,
            sourceMap: false,
            removeComments: false,
          },
        });
        const errors = (out.diagnostics || []).filter((d) => d.category === ts.DiagnosticCategory.Error);
        if (errors.length > 0) {
          const first = errors[0];
          const msg = ts.flattenDiagnosticMessageText(first.messageText, '\n');
          let where = fileName;
          if (first.file && typeof first.start === 'number') {
            const pos = first.file.getLineAndCharacterOfPosition(first.start);
            where = `${fileName}:${pos.line + 1}:${pos.character + 1}`;
          }
          throw new Error(`${where}: ${msg}`);
        }
        return out.outputText;
      },
    };
  }

  const babelCandidates = [
    { core: tryResolve('@babel/core'), base: null },
    { core: fs.existsSync('/usr/share/nodejs/@babel/core') ? '/usr/share/nodejs/@babel/core' : null, base: '/usr/share/nodejs' },
  ];
  for (const cand of babelCandidates) {
    if (!cand.core) continue;
    const resolvePreset = (name) => (cand.base ? path.join(cand.base, name) : tryResolve(name));
    const presetTs = resolvePreset('@babel/preset-typescript');
    const presetReact = resolvePreset('@babel/preset-react');
    const pluginCjs = resolvePreset('@babel/plugin-transform-modules-commonjs');
    if (!presetTs || !presetReact || !pluginCjs) continue;
    if (cand.base && !(fs.existsSync(presetTs) && fs.existsSync(presetReact) && fs.existsSync(pluginCjs))) continue;
    const babel = require(cand.core);
    return {
      name: `@babel/core ${babel.version || ''}`.trim(),
      compile(code, fileName) {
        const result = babel.transformSync(code, {
          filename: fileName,
          babelrc: false,
          configFile: false,
          presets: [
            [presetTs, { isTSX: fileName.endsWith('.tsx'), allExtensions: true, jsxPragma: 'h', jsxPragmaFrag: 'Fragment' }],
            [presetReact, { pragma: 'h', pragmaFrag: 'Fragment' }],
          ],
          plugins: [pluginCjs],
        });
        if (!result || typeof result.code !== 'string') {
          throw new Error(`Failed to compile ${fileName}`);
        }
        return result.code;
      },
    };
  }
  return null;
}

// ---------------------------------------------------------------------------------------------
// Module discovery + bundling
// ---------------------------------------------------------------------------------------------

function listSourceModules(dir, prefix) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      out.push(...listSourceModules(path.join(dir, entry.name), rel));
    } else if (/\.(ts|tsx)$/.test(entry.name) && !entry.name.endsWith('.d.ts')) {
      out.push(rel);
    }
  }
  return out;
}

function listStylesheets() {
  return fs
    .readdirSync(srcDir)
    .filter((n) => n.endsWith('.css'))
    .sort();
}

const BUNDLE_PRELUDE = `(function() {
  const __modules = {};
  const __cache = {};
  function __resolve(fromId, request) {
    if (request.charAt(0) !== '.') throw new Error("Unsupported bare import: " + request + " (from " + fromId + ")");
    const parts = fromId.split('/');
    parts.pop();
    for (const seg of request.split('/')) {
      if (seg === '.' || seg === '') continue;
      if (seg === '..') { parts.pop(); continue; }
      parts.push(seg);
    }
    return parts.join('/');
  }
  function __require(id) {
    if (__cache[id]) return __cache[id].exports;
    const fn = __modules[id];
    if (!fn) throw new Error("Module not found: " + id);
    const mod = { exports: {} };
    __cache[id] = mod;
    fn(mod, mod.exports, function(request) { return __require(__resolve(id, request)); });
    return mod.exports;
  }
`;

function buildOnce(compiler) {
  const files = listSourceModules(srcDir, '');
  const compiled = [];
  for (const rel of files) {
    const fullPath = path.join(srcDir, rel);
    const code = compiler.compile(fs.readFileSync(fullPath, 'utf8'), fullPath);
    compiled.push({ id: rel.replace(/\.(ts|tsx)$/, ''), code });
  }
  if (!compiled.some((m) => m.id === ENTRY_MODULE)) {
    throw new Error(`Entry module src/${ENTRY_MODULE}.tsx not found`);
  }
  console.log(`[compile] ${compiled.length} modules OK (${compiler.name})`);
  if (verifyOnly) return null;

  let bundle = BUNDLE_PRELUDE;
  for (const mod of compiled) {
    bundle += `  __modules[${JSON.stringify(mod.id)}] = function(module, exports, require) {\n${mod.code}\n  };\n`;
  }
  bundle += `  const rt = __require("jsx-runtime");\n  window.h = rt.h;\n  window.Fragment = rt.Fragment;\n  __require(${JSON.stringify(ENTRY_MODULE)});\n})();\n`;

  fs.mkdirSync(distDir, { recursive: true });
  fs.writeFileSync(path.join(distDir, 'bundle.js'), bundle, 'utf8');

  // Stylesheets are copied verbatim; index.html gets content-hash query strings so that a browser
  // never keeps running a stale bundle after an upgrade.
  const hashes = { 'bundle.js': shortHash(bundle) };
  for (const css of listStylesheets()) {
    const content = fs.readFileSync(path.join(srcDir, css));
    fs.writeFileSync(path.join(distDir, css), content);
    hashes[css] = shortHash(content);
  }
  // Static assets (favicons, logo) in web/public are copied as they are.
  const publicDir = path.join(webRoot, 'public');
  if (fs.existsSync(publicDir)) {
    for (const name of fs.readdirSync(publicDir).sort()) {
      const src = path.join(publicDir, name);
      if (fs.statSync(src).isFile()) fs.copyFileSync(src, path.join(distDir, name));
    }
  }
  let html = fs.readFileSync(path.join(webRoot, 'index.html'), 'utf8');
  for (const [name, hash] of Object.entries(hashes)) {
    html = html.split(`"/${name}"`).join(`"/${name}?v=${hash}"`);
  }
  fs.writeFileSync(path.join(distDir, 'index.html'), html, 'utf8');

  console.log(`[bundle] ${path.relative(process.cwd(), path.join(distDir, 'bundle.js'))} (${bundle.length} bytes)`);
  return bundle.length;
}

function shortHash(content) {
  return crypto.createHash('sha256').update(content).digest('hex').slice(0, 10);
}

// ---------------------------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------------------------

const compiler = findCompiler();
if (!compiler) {
  const haveDist = fs.existsSync(path.join(distDir, 'bundle.js'));
  const hint =
    'No TypeScript/Babel compiler found. Run `npm install` inside web/ (installs the `typescript` devDependency), then build again.';
  if (ifAvailable && haveDist) {
    console.log(`[skip] ${hint}`);
    console.log('[skip] Keeping the committed web/dist bundle, which is ready to serve as-is.');
    process.exit(0);
  }
  console.error(`[error] ${hint}`);
  if (haveDist) console.error('[error] The committed web/dist bundle is still usable; only rebuilding needs a compiler.');
  process.exit(1);
}

try {
  buildOnce(compiler);
} catch (err) {
  console.error(`[error] ${err && err.message ? err.message : err}`);
  if (!watchMode) process.exit(1);
}

if (verifyOnly) {
  console.log('Frontend TypeScript/TSX compile verification passed.');
} else if (watchMode) {
  console.log('[watch] Watching web/src and web/index.html — press Ctrl+C to stop.');
  let timer = null;
  const trigger = () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      try {
        buildOnce(compiler);
      } catch (err) {
        console.error(`[error] ${err && err.message ? err.message : err}`);
      }
    }, 120);
  };
  const watchDir = (dir) => {
    fs.watch(dir, trigger);
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) watchDir(path.join(dir, entry.name));
    }
  };
  watchDir(srcDir);
  fs.watch(path.join(webRoot, 'index.html'), trigger);
}
