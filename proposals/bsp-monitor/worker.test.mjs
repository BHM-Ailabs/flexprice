import { test, expect } from "bun:test";
import { run, collectRailway } from "./worker.mjs";

const ids = ["5ca4bc0b-c6c2-487d-99cf-1480b663847e", "b992913e-5981-42d6-abf0-4f2c32eeca75", "81feaabc-7ef1-4d72-8b63-fc131dd9c7b9"];
function fixture() {
  let saved = null;
  const env = { STATE: { async get() { return saved; }, async put(_k, value) { saved = value; } }, RAILWAY_API_TOKEN: "synthetic", RESEND_API_KEY: "synthetic", BSP_READY_URL: "https://bsp.example/health/ready", ALERT_FROM: "ops@example.com", ALERT_TO: "oncall@example.com" };
  const state = { percent: 20, ready: true, telemetry: true, stale: false, mail: true, sent: [], queries: [] };
  const fetcher = async (url, init) => {
    if (url.includes("backboard.railway")) {
      state.queries.push(JSON.parse(init.body).query);
      if (!state.telemetry) return new Response("error", { status: 500 });
      const nodes = ids.map(volumeId => ({ node: { volumeId, currentSizeMB: state.percent, sizeMB: 100 } }));
      const data = { project: { environments: { edges: [{ node: { id: "9f1ba1b2-cf16-4e13-9966-b9932741f00c", volumeInstances: { edges: nodes } } }] } } };
      ids.forEach((_, i) => { data[`v${i}`] = [{ values: [{ ts: Date.now() / 1000 - (state.stale ? 3600 : 0), value: state.percent / 1000 }] }]; });
      return Response.json({ data });
    }
    if (url === env.BSP_READY_URL) return Response.json({ status: state.ready ? "ok" : "unavailable" }, { status: state.ready ? 200 : 503 });
    if (url.includes("api.resend.com")) {
      if (!state.mail) return new Response("failure", { status: 500 });
      state.sent.push(JSON.parse(init.body)); return Response.json({ id: "synthetic" });
    }
    throw new Error("Unexpected external call in test");
  };
  return { env, state, fetcher, saved: () => saved };
}

test("warning, critical, hysteresis and recovery", async () => {
  const f = fixture(); let now = Date.now();
  await run(f.env, f.fetcher, now); expect(f.state.sent).toHaveLength(0);
  f.state.percent = 76; await run(f.env, f.fetcher, now += 300000); expect(f.state.sent).toHaveLength(3); expect(f.state.sent[0].subject).toContain("WARNING");
  f.state.percent = 91; await run(f.env, f.fetcher, now += 300000); expect(f.state.sent).toHaveLength(6); expect(f.state.sent[3].subject).toContain("CRITICAL");
  f.state.percent = 89; await run(f.env, f.fetcher, now += 300000); expect(f.state.sent).toHaveLength(6);
  f.state.percent = 60; await run(f.env, f.fetcher, now += 300000); expect(f.state.sent).toHaveLength(9); expect(f.state.sent[6].subject).toContain("RECOVERED");
  expect(f.state.queries.every(q => q.includes("query(") && !q.includes("mutation"))).toBe(true);
});

test("DB/dependency down needs two failures; recovery sends email", async () => {
  const f = fixture(); let now = Date.now(); f.state.ready = false;
  await run(f.env, f.fetcher, now); expect(f.state.sent).toHaveLength(0);
  await run(f.env, f.fetcher, now += 300000); expect(f.state.sent).toHaveLength(1); expect(f.state.sent[0].text).toContain("readiness failed");
  f.state.ready = true; await run(f.env, f.fetcher, now += 300000); expect(f.state.sent.at(-1).subject).toContain("RECOVERED");
});

test("telemetry errors/staleness alert instead of assuming zero disk usage", async () => {
  for (const fail of ["telemetry", "stale"]) {
    const f = fixture(); f.state[fail] = fail === "stale";
    await run(f.env, f.fetcher); expect(f.state.sent).toHaveLength(1); expect(f.state.sent[0].subject).toContain("telemetry");
  }
});

test("failed email does not acknowledge an alert", async () => {
  const f = fixture(); f.state.percent = 95; f.state.mail = false;
  await expect(run(f.env, f.fetcher)).rejects.toThrow(); expect(f.saved()).toBe(null);
  f.state.mail = true; await run(f.env, f.fetcher); expect(f.state.sent).toHaveLength(3);
});

test("six-hour reminders", async () => {
  const f = fixture(); f.state.percent = 95; const now = Date.now();
  await run(f.env, f.fetcher, now); await run(f.env, f.fetcher, now + 300000); expect(f.state.sent).toHaveLength(3);
  await run(f.env, f.fetcher, now + 6 * 3600000); expect(f.state.sent).toHaveLength(6);
});
