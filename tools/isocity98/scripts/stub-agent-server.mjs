// 验证用 stub agent-server：实现 deploy/dsh-plugins/agent-server.mjs 里面板依赖的
// 四个端点（/agents、/kb-stats、/agent/chat、/health），并顺带托管面板静态文件。
//
// 为什么需要它：用 `python3 -m http.server` 跑面板时 /agents、/kb-stats 会 404，
// Chrome 会把这两个网络 404 记进 console（面板本身无 JS 异常）。stub 提供真实的
// 端点契约，让验证能覆盖「有 agent 时」的完整行为，并得到干净的 console。
//
//   node scripts/stub-agent-server.mjs 8125
import { createServer } from 'node:http';
import { readFileSync, existsSync } from 'node:fs';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const port = Number(process.argv[2] || 8125);
const root = fileURLToPath(new URL('../../../deploy/dsh-plugins', import.meta.url));

const AGENTS = [
  // 示例数据：故意用中性值，不引用任何真实项目的编号体系
  { sessionId: 'session-aaaaaa111111', phase: 'implementing', taskStatus: 'round2', task: '示例需求 · 面板渲染\n实现等距绘制', project: 'demo-project', taskId: 'TASK-001', status: 'working', elapsed: 125, model: 'demo-model' },
  { sessionId: 'session-bbbbbb222222', phase: 'review', task: '示例需求 · 面板渲染（验收）', project: 'demo-project', taskId: 'TASK-001', status: 'working', elapsed: 80, delegationDepth: 1, parentSessionId: 'session-aaaaaa111111' },
  { sessionId: 'session-cccccc333333', phase: 'planning', task: '示例需求 · 天气系统', project: 'demo-project', taskId: 'TASK-002', status: 'idle', elapsed: 10 },
  { sessionId: 'session-dddddd444444', phase: 'blocked', task: '示例需求 · 阻塞等待', project: 'demo-project', taskId: 'TASK-003', status: 'working', elapsed: 300 },
];
const KB = {
  restored: false,
  totals: { hits: 42, misses: 8, searches: 50, avgMs: 37, hist: { boundaries: [0, 5, 10, 25, 50, 100, 250, 500, 1000], counts: [4, 12, 9, 14, 6, 3, 1, 1] } },
};
const MIME = { '.html': 'text/html; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.png': 'image/png' };

const json = (res, obj, headers = {}) => {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8', ...headers });
  res.end(JSON.stringify(obj));
};
const body = (req) => new Promise((resolve) => { let b = ''; req.on('data', (c) => { b += c; }); req.on('end', () => resolve(b)); });

createServer(async (req, res) => {
  const url = (req.url || '/').split('?')[0];
  if (req.method === 'GET' && (url === '/health' || url === '/healthz')) return json(res, { ok: true });
  if (req.method === 'GET' && url === '/agents') return json(res, AGENTS, { 'x-agents-finished': '7' });
  if (req.method === 'GET' && url === '/kb-stats') return json(res, KB);
  if (req.method === 'POST' && url === '/agent/chat') {
    const payload = JSON.parse((await body(req)) || '{}');
    const echo = String(payload.message || '');
    const hasKb = Boolean(payload.kbQuery);
    return json(res, { sessionId: payload.sessionId || 'session-chat-000001', text: `（stub 回复）收到「${echo}」${hasKb ? ' · 首问已注入 kbQuery=' + payload.kbQuery : ' · 多轮复用 sessionId'}` });
  }
  // 静态文件
  const rel = normalize(url === '/' ? '/agent-monitor.html' : url).replace(/^([/\\])+/, '');
  const file = join(root, rel);
  if (!file.startsWith(root) || !existsSync(file)) { res.writeHead(404, { 'content-type': 'application/json' }); return res.end('{"error":"not found"}'); }
  res.writeHead(200, { 'content-type': MIME[extname(file)] || 'application/octet-stream' });
  res.end(readFileSync(file));
}).listen(port, '127.0.0.1', () => console.log(`stub agent-server: http://127.0.0.1:${port}/agent-monitor.html`));
