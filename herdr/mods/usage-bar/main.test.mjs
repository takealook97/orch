import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import * as format from './format.js';

// Real HUD fixture shape, with Gemini added. No network or app state is used.
const fixture = {
  sys: { cpu_pct: 23.4, mem_used_bytes: 23730000000, mem_total_bytes: 38654705664 },
  usage: { accounts: [
    { provider: 'codex', home: '/work', id: 'work', label: 'work@example.com', plan: 'prolite', five_hour: null,
      weekly: { used_pct: 9, resets_at: '2026-11-04T15:37:06+09:00' },
      coupons: { available: 2, next_expires_at: '2026-11-30T00:00:00+09:00' }, terminals: ['t2w'], stale: false, error: '' },
    { provider: 'claude', home: '', id: 'default', label: 'me', plan: 'max',
      five_hour: { used_pct: 34, resets_at: '2026-11-03T15:10:00+09:00' },
      weekly: { used_pct: 81, resets_at: '2026-11-10T10:59:59+09:00' }, terminals: ['t2t'], stale: true, error: 'HTTP 429' },
    { provider: 'gemini', id: 'agy', label: '',
      five_hour: { used_pct: 12, resets_at: '2026-11-03T15:10:00+09:00' },
      weekly: { used_pct: 34, resets_at: '2026-11-10T10:59:59+09:00' } },
  ] },
};

function element(kind) {
  const target = { kind, nodes: [], style: {} };
  const proxy = new Proxy(target, { get(obj, key) {
    if (key in obj) return obj[key];
    return (...args) => {
      if (key === 'child') obj.nodes.push(args[0]);
      else if (key === 'children') obj.nodes.push(...args[0]);
      else if (key === 'when') { if (args[0]) args[1](proxy); }
      else obj.style[key] = args.length === 1 ? args[0] : args;
      return proxy;
    };
  } });
  return proxy;
}
const strip = code => code.replace(/^import .*;\n/gm, '').replace(/export default /g, '').replace(/export /g, '');
const div = () => element('div'), h_flex = () => element('row'), v_flex = () => element('column');
const ui = new Function('div', 'h_flex', 'v_flex', 'pctColorRole',
  `${strip(readFileSync(new URL('./ui.js', import.meta.url), 'utf8'))}; return {cell, groupLabel, systemRow};`
)(div, h_flex, v_flex, format.pctColorRole);
const source = strip(readFileSync(new URL('./main.js', import.meta.url), 'utf8'));
const walk = node => typeof node === 'object' ? [node, ...node.nodes.flatMap(walk)] : [node];
const text = node => walk(node).filter(value => typeof value === 'string');
const byID = (node, id) => walk(node).find(value => value?.style?.id === id);

function harness() {
  const state = {
    spaces: [{ id: 's1', active: true, tabs: [{ id: 'tb1' }] }],
    panes: [{ tab: 'tb1', terminal: 't2t' }, { tab: 'tb1', terminal: 't2w' },
      { tab: 'tb1', terminal: 'remote/t9' }, { tab: 'tb1', terminal: null }, { tab: 'other', terminal: 't99' }],
    agents: [{ space: 's1', address: 't2t', status: 'working' }],
  };
  const calls = [], tasks = [], timers = [], intervals = [];
  let respond = args => ({ code: 0, stdout: JSON.stringify(args[1] === 'sys' ? fixture.sys : fixture.usage) });
  const cx = {
    timer: {
      after(ms, fn) { const job = { ms, fn, canceled: false, cancel() { this.canceled = true; } }; timers.push(job); return job; },
      every(ms, fn) { intervals.push({ ms, fn }); },
    },
    spawn(fn) { const task = fn(cx); tasks.push(task); return task; },
    notify() {}, stop_propagation() {},
  };
  const deps = { View: class {}, h_flex, v_flex, exec: async (_cmd, args) => { calls.push(args); return respond(args); },
    spaces: () => state.spaces, panes: () => state.panes, agents: () => state.agents,
    theme: () => new Proxy({}, { get: (_obj, prop) => prop }), ...format, ...ui };
  const UsageBar = new Function(...Object.keys(deps), `${source}; return UsageBar;`)(...Object.values(deps));
  const view = new UsageBar();
  view.init({}, cx);
  return { view, cx, state, calls, timers, intervals,
    respond(fn) { respond = fn; },
    async flush() { while (tasks.length) await Promise.all(tasks.splice(0)); },
    tick(ms) {
      const jobs = timers.filter(job => job.ms === ms && !job.canceled);
      for (const job of jobs) { job.canceled = true; job.fn(cx); }
    },
  };
}

test('poll intervals and selected account arguments', async () => {
  const h = harness(); await h.flush();
  assert.deepEqual(h.intervals.map(job => job.ms), [3000, 60000]);
  assert.deepEqual(h.calls.filter(args => args[1] === 'usage'), [['hud', 'usage', '--selected', '--max-age', '45s']]);
});

test('provider cards retain placeholders, local labels, coupon text and stale opacity', async () => {
  const h = harness(); await h.flush(); h.tick(1000); await h.flush();
  const tree = h.view.render(h.cx), labels = text(tree);
  assert.deepEqual(byID(tree, 'groups').nodes.map(node => node.style.id), ['account-codex', 'account-claude', 'account-gemini']);
  assert.equal(labels.filter(label => label === 'CPU').length, 1);
  assert.deepEqual(tree.nodes.map(node => node.style.id), ['groups', undefined, 'system']);
  assert.equal(tree.nodes[1].style.flex_1.length, 0);
  assert.ok(labels.includes('2 resets'));
  assert.ok(labels.includes('work'));
  assert.ok(!labels.some(label => /max|prolite|default|⚠|🎟|@/.test(label)));
  const codex = byID(tree, 'account-codex');
  const missing = codex.nodes[0].nodes[1];
  assert.deepEqual(text(missing), ['5h', '—', '']);
  assert.equal(byID(tree, 'account-claude').nodes[0].style.opacity, 0.6);
  assert.equal(byID(tree, 'account-gemini').nodes[0].nodes[0].nodes.length, 1);
  assert.ok(walk(tree).some(node => node?.style?.w === 36 && node?.style?.h === 2));
  h.view.width = 979;
  assert.ok(!walk(h.view.render(h.cx)).some(node => node?.style?.w === 36 && node?.style?.h === 2));
  h.view.width = 819;
  assert.ok(!text(h.view.render(h.cx)).includes('work'));
  h.view.width = 699;
  const narrow = byID(h.view.render(h.cx), 'account-gemini');
  assert.equal(text(narrow.nodes[0].nodes[2]).at(-1), '');
  assert.equal(narrow.nodes[0].nodes[2].style.w, 58);
});

test('space switches do not trigger usage refresh', async () => {
  const h = harness(); await h.flush();
  const count = h.calls.length;
  h.state.spaces[0].id = 's2';
  h.view.render(h.cx); h.tick(0); h.tick(1000); await h.flush();
  assert.equal(h.calls.length, count);
});

test('working completion transitions debounce reads for two seconds', async () => {
  for (const status of ['done', 'idle', 'blocked']) {
    const h = harness(); await h.flush(); h.tick(1000); await h.flush();
    h.state.agents[0].status = status;
    const count = h.calls.length;
    h.view.render(h.cx); h.tick(0);
    assert.equal(h.calls.length, count);
    assert.ok(h.timers.some(job => job.ms === 2000 && !job.canceled));
    h.tick(2000); await h.flush();
    assert.equal(h.calls.length, count + 1);
    assert.ok(!h.calls.at(-1).includes('--refresh'));
  }
});

test('completion bursts cancel previous debounce and ignore agents in other spaces', async () => {
  const h = harness(); await h.flush();
  h.state.agents[0].status = 'done'; h.view.render(h.cx); h.tick(0);
  const first = h.timers.at(-1);
  h.state.agents[0].status = 'working'; h.view.render(h.cx); h.tick(0);
  h.state.agents[0].status = 'blocked'; h.view.render(h.cx); h.tick(0);
  assert.equal(first.canceled, true);
  assert.equal(h.timers.filter(job => job.ms === 2000 && !job.canceled).length, 1);
  const count = h.timers.length;
  h.state.agents.push({ space: 'other', address: 't99', status: 'done' });
  h.view.render(h.cx);
  assert.equal(h.timers.length, count);
});

test('failures keep last readings and partial errors preserve only the same account', async () => {
  const h = harness(); await h.flush(); h.tick(1000); await h.flush();
  const previous = h.view.usage;
  h.respond(() => ({ code: 1, stdout: '' }));
  h.view.refresh('usage', h.cx); await h.flush();
  assert.equal(h.view.usage, previous);
  assert.equal(h.view.stale.usage, true);
  h.respond(() => ({ code: 0, stdout: JSON.stringify({ accounts: [{ ...fixture.usage.accounts[0], weekly: null, error: 'offline' }] }) }));
  h.view.refresh('usage', h.cx); await h.flush();
  assert.equal(h.view.usage.accounts[0].weekly.used_pct, 9);
  assert.equal(h.view.usage.accounts[0].stale, true);
  h.respond(() => ({ code: 0, stdout: JSON.stringify({ accounts: [{ provider: 'codex', id: 'different', home: '/work', error: 'offline' }] }) }));
  h.view.refresh('usage', h.cx); await h.flush();
  assert.equal(h.view.usage.accounts[0].weekly, undefined);
});
