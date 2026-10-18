const PROJECT = "57f773d2-63a2-448d-a344-d5ef0b156b42";
const ENVIRONMENT = "9f1ba1b2-cf16-4e13-9966-b9932741f00c";
const VOLUMES = [
  ["5ca4bc0b-c6c2-487d-99cf-1480b663847e", "Postgres"],
  ["b992913e-5981-42d6-abf0-4f2c32eeca75", "Kafka"],
  ["81feaabc-7ef1-4d72-8b63-fc131dd9c7b9", "ClickHouse"],
];
const REMINDER_MS = 6 * 3600_000;

async function request(url, init, fetcher) {
  const response = await fetcher(url, { ...init, redirect: "error", signal: AbortSignal.timeout(10_000) });
  if (!response.ok) throw new Error("upstream request failed");
  return response;
}

export async function collectRailway(env, fetcher = fetch) {
  const since = new Date(Date.now() - 20 * 60_000).toISOString();
  const metrics = VOLUMES.map(([id], i) => `v${i}: metrics(projectId: $project, environmentId: $environment, volumeId: "${id}", measurements: [DISK_USAGE_GB], startDate: $since, sampleRateSeconds: 60) { values { ts value } }`).join("\n");
  const query = `query($project: String!, $environment: String!, $since: DateTime!) {
    project(id: $project) { environments { edges { node { id volumeInstances { edges { node { volumeId currentSizeMB sizeMB } } } } } } }
    ${metrics}
  }`;
  const response = await request("https://backboard.railway.com/graphql/v2", {
    method: "POST", headers: { Authorization: `Bearer ${env.RAILWAY_API_TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify({ query, variables: { project: PROJECT, environment: ENVIRONMENT, since } }),
  }, fetcher);
  const payload = await response.json();
  if (payload.errors?.length) throw new Error("Railway GraphQL rejected query");
  const environment = payload.data?.project?.environments?.edges?.find(e => e.node.id === ENVIRONMENT)?.node;
  const volumes = environment?.volumeInstances?.edges?.map(e => e.node);
  if (!volumes) throw new Error("Railway production volume data missing");
  return VOLUMES.map(([id, label], i) => {
    const volume = volumes.find(v => v.volumeId === id);
    const samples = (payload.data[`v${i}`] ?? []).flatMap(m => m.values ?? []).filter(v => Number.isFinite(v.ts) && Number.isFinite(v.value) && v.value >= 0);
    samples.sort((a, b) => a.ts - b.ts);
    const latest = samples.at(-1);
    if (!volume || !Number.isFinite(volume.currentSizeMB) || volume.currentSizeMB < 0 || !(volume.sizeMB > 0) || !latest || Date.now() / 1000 - latest.ts > 15 * 60) {
      throw new Error("Railway volume telemetry missing or stale");
    }
    // volumeInstances can lag metrics. Railway DISK_USAGE_GB is decimal GB.
    const used = Math.max(volume.currentSizeMB, latest.value * 1000);
    return { key: `volume:${id}`, label, percent: used / volume.sizeMB * 100, used, capacity: volume.sizeMB };
  });
}

export async function run(env, fetcher = fetch, now = Date.now()) {
  const previous = JSON.parse(await env.STATE.get("bsp-monitor") || "{}");
  const conditions = {};
  const [volumeResult, readinessResult] = await Promise.allSettled([
    collectRailway(env, fetcher),
    (async () => {
      const url = new URL(env.BSP_READY_URL);
      if (url.protocol !== "https:" || url.pathname !== "/health/ready") throw new Error("invalid readiness URL");
      const response = await request(url.href, { headers: { "Cache-Control": "no-cache" } }, fetcher);
      const body = await response.json();
      if (body.status !== "ok") throw new Error("readiness failed");
    })(),
  ]);
  if (volumeResult.status === "fulfilled") {
    for (const v of volumeResult.value) {
      const old = previous[v.key]?.level || 0;
      let level = v.percent >= 90 ? 2 : v.percent >= 75 ? 1 : 0;
      // Hysteresis avoids repeated threshold flapping.
      if (old === 2 && v.percent >= 85) level = 2;
      else if (old >= 1 && v.percent >= 70 && level === 0) level = 1;
      conditions[v.key] = { level, text: `${v.label}: ${v.used.toFixed(0)}/${v.capacity} MB (${v.percent.toFixed(1)}%)` };
    }
    conditions.telemetry = { level: 0, text: "Railway volume telemetry recovered" };
  } else {
    conditions.telemetry = { level: 2, text: "Railway volume telemetry failed or is stale; capacity is unknown" };
  }
  const failures = readinessResult.status === "rejected" ? (previous.readiness?.failures || 0) + 1 : 0;
  conditions.readiness = { level: failures >= 2 ? 2 : previous.readiness?.level && failures ? 2 : 0, failures,
    text: failures ? "BSP readiness failed: database or another hard dependency may be down" : "BSP readiness recovered" };

  for (const [key, condition] of Object.entries(conditions)) {
    const old = previous[key] || { level: 0, sentAt: 0, sequence: 0 };
    const changed = old.level !== condition.level;
    const due = condition.level > 0 && now - old.sentAt >= REMINDER_MS;
    if (changed || due) {
      const sequence = old.sequence + 1;
      const severity = condition.level === 2 ? "CRITICAL" : condition.level === 1 ? "WARNING" : "RECOVERED";
      await request("https://api.resend.com/emails", {
        method: "POST", headers: { Authorization: `Bearer ${env.RESEND_API_KEY}`, "Content-Type": "application/json", "Idempotency-Key": `bsp-${key}-${sequence}-${Math.floor(now / REMINDER_MS)}` },
        body: JSON.stringify({ from: env.ALERT_FROM, to: env.ALERT_TO.split(",").map(s => s.trim()).filter(Boolean),
          subject: `[BSP ${severity}] ${key}`, text: `${condition.text}\nUTC: ${new Date(now).toISOString()}\nProject: ${PROJECT}\nRead-only monitor. Investigate before changing capacity or retention. No automatic restart or financial retries.` }),
      }, fetcher);
      previous[key] = { ...condition, sentAt: now, sequence };
    } else {
      previous[key] = { ...old, ...condition };
    }
  }
  previous.lastRun = now;
  // Failed delivery never advances state; retries retain the alert.
  await env.STATE.put("bsp-monitor", JSON.stringify(previous));
}

export default {
  async scheduled(_controller, env, ctx) {
    ctx.waitUntil(run(env).catch(() => { console.error("BSP monitor failed; inspect credentials, KV and email delivery"); throw new Error("BSP monitor failed"); }));
  },
  async fetch(_request, env) {
    const state = JSON.parse(await env.STATE.get("bsp-monitor") || "{}");
    const fresh = Date.now() - (state.lastRun || 0) < 15 * 60_000;
    return Response.json({ status: fresh ? "ok" : "stale" }, { status: fresh ? 200 : 503, headers: { "Cache-Control": "no-store" } });
  },
};
