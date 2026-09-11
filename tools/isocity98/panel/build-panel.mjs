// 把 Go 离线管线产出的三个资产内联进面板模板，生成单文件 agent-monitor.html。
//
//   node tools/isocity98/panel/build-panel.mjs [--panel <dir>] [--out <file>]
//
// 面板源码（本目录）与资产生成器（上级目录）都随仓库维护：
//   make -C tools/isocity98 agenttown   # 产出 build/panel/
//   node tools/isocity98/panel/build-panel.mjs
//
// 为什么要有这一步：图集 base64 后约 1.6MB、精灵表 163KB、小镇 JSON 29KB，
// 手写进 HTML 既不可维护也没法在编辑器里改。模板只保存代码，资产在构建时注入。
// 运行时仍是「单文件、零外部请求」。

import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const argv = process.argv.slice(2);
const argOf = (name, def) => {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : def;
};

const panelDir = argOf('panel', fileURLToPath(new URL('../build/panel', import.meta.url)));
const tplPath = argOf('tpl', fileURLToPath(new URL('./agent-monitor.tpl.html', import.meta.url)));
const outPath = argOf('out', fileURLToPath(new URL('../../../deploy/dsh-plugins/agent-monitor.html', import.meta.url)));

const atlasB64 = readFileSync(`${panelDir}/atlas_0.png`).toString('base64');
const spritesJson = readFileSync(`${panelDir}/sprites.json`, 'utf8').trim();
const townJson = readFileSync(`${panelDir}/town.json`, 'utf8').trim();

let html = readFileSync(tplPath, 'utf8');

// 用替换函数而不是字符串：JSON 里可能含 `$&` 之类的替换模式
const inject = (token, value) => {
  if (!html.includes(token)) throw new Error(`模板缺少占位符 ${token}`);
  html = html.replace(token, () => value);
};
inject('@@ATLAS_B64@@', atlasB64);
inject('@@SPRITES_JSON@@', spritesJson);
inject('@@TOWN_JSON@@', townJson);

// 内联 JSON 里若出现 `</script` 会提前结束脚本块（当前数据没有，防御性检查）
for (const [name, value] of [['atlas', atlasB64], ['sprites.json', spritesJson], ['town.json', townJson]]) {
  if (/<\/script/i.test(value)) throw new Error(`${name} 含 </script，需要转义`);
}

writeFileSync(outPath, html);
const kb = (statSync(outPath).size / 1024).toFixed(0);
const lines = html.split('\n').length;
console.log(`已生成 ${outPath}`);
console.log(`  atlas_0.png  ${(atlasB64.length / 1024).toFixed(0)} KB(base64)  sprites.json ${(spritesJson.length / 1024).toFixed(0)} KB  town.json ${(townJson.length / 1024).toFixed(0)} KB`);
console.log(`  单文件 ${kb} KB / ${lines} 行`);
