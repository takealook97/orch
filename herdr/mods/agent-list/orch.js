export function workerIndex(hud) {
  const workers = new Map();
  const heads = new Map();
  if (!Array.isArray(hud?.runs)) return { workers, heads };
  for (const run of hud.runs) {
    if (!run || !Array.isArray(run.workers)) continue;
    for (const worker of run.workers) {
      if (!worker || typeof worker.terminal !== "string" || !worker.terminal) continue;
      workers.set(worker.terminal, worker);
      if (typeof run.head_terminal !== "string" || !run.head_terminal || worker.status === "closed" || worker.status === "lost") continue;
      const counts = heads.get(run.head_terminal) ?? { total: 0, working: 0 };
      counts.total++;
      if (worker.status === "spawned" || worker.status === "prompted") counts.working++;
      heads.set(run.head_terminal, counts);
    }
  }
  return { workers, heads };
}

export function isHidden(agent, index) {
  return index.workers.has(agent?.address) || index.workers.has(agent?.terminal);
}

export function headSuffix(address, index) {
  const counts = index.heads.get(address);
  return counts?.total ? ` · worker ${counts.total}${counts.working ? ` (${counts.working} 작업 중)` : ""}` : "";
}
