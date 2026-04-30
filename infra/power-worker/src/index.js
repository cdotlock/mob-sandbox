// mob-power Cloudflare Worker
//
// Brokers Vultr instance power control (start/stop/status) for mob operators
// authenticated by SSH ed25519 signature. The Vultr API key never leaves the
// Worker; operators only need their local SSH private key.
//
// Request: POST /
//   Content-Type: application/json
//   Body: {
//     "action":    "start" | "stop" | "reboot" | "status",
//     "operator":  "<name>",
//     "timestamp": <unix seconds>,
//     "signature": "<base64 ed25519 signature>"
//   }
//
// Signature input (canonical, plain text):
//   "<action>|<operator>|<timestamp>"
//
// Worker bindings (set via wrangler.toml + secrets):
//   VULTR_API_KEY        secret  Vultr bearer token
//   VM_ID                var     Vultr instance UUID
//   AUTHORIZED_PUBKEYS   var     JSON array of {name, pubkey_b64}
//                                pubkey_b64 = base64(32 raw ed25519 pubkey bytes)
//   CLOCK_SKEW_SECONDS   var     optional, default 300

const VULTR_API = "https://api.vultr.com/v2";

const ACTIONS = {
  start:  { method: "POST", path: (id) => `/instances/${id}/start`,  description: "power on" },
  stop:   { method: "POST", path: (id) => `/instances/${id}/halt`,   description: "power off" },
  reboot: { method: "POST", path: (id) => `/instances/${id}/reboot`, description: "reboot" },
  status: { method: "GET",  path: (id) => `/instances/${id}`,        description: "status" },
};

export default {
  async fetch(request, env) {
    if (request.method === "GET" && new URL(request.url).pathname === "/health") {
      return json({ ok: true, vm_id: env.VM_ID });
    }
    if (request.method !== "POST") {
      return json({ error: "method not allowed" }, 405);
    }

    let body;
    try {
      body = await request.json();
    } catch {
      return json({ error: "invalid JSON body" }, 400);
    }

    const { action, operator, timestamp, signature } = body || {};
    if (!action || !operator || !timestamp || !signature) {
      return json({ error: "missing required fields: action, operator, timestamp, signature" }, 400);
    }

    const def = ACTIONS[action];
    if (!def) {
      return json({ error: `unknown action: ${action}; want one of ${Object.keys(ACTIONS).join(", ")}` }, 400);
    }

    const skew = Number(env.CLOCK_SKEW_SECONDS || 300);
    const now = Math.floor(Date.now() / 1000);
    if (Math.abs(now - Number(timestamp)) > skew) {
      return json({ error: `timestamp out of window (now=${now}, got=${timestamp}, max_skew=${skew}s)` }, 401);
    }

    let authorized;
    try {
      authorized = JSON.parse(env.AUTHORIZED_PUBKEYS || "[]");
    } catch {
      return json({ error: "Worker misconfigured: AUTHORIZED_PUBKEYS not valid JSON" }, 500);
    }
    const op = authorized.find((o) => o.name === operator);
    if (!op) {
      return json({ error: `unknown operator: ${operator}` }, 401);
    }

    const message = `${action}|${operator}|${timestamp}`;
    const valid = await verifyEd25519(op.pubkey_b64, signature, message);
    if (!valid) {
      return json({ error: "bad signature" }, 401);
    }

    if (!env.VULTR_API_KEY) {
      return json({ error: "Worker misconfigured: VULTR_API_KEY secret not set" }, 500);
    }
    if (!env.VM_ID) {
      return json({ error: "Worker misconfigured: VM_ID var not set" }, 500);
    }

    const upstream = await fetch(VULTR_API + def.path(env.VM_ID), {
      method: def.method,
      headers: { Authorization: `Bearer ${env.VULTR_API_KEY}` },
    });
    const upstreamText = await upstream.text();

    console.log(`operator=${operator} action=${action} vultr_status=${upstream.status}`);

    if (action === "status") {
      try {
        const data = JSON.parse(upstreamText);
        const inst = data.instance || {};
        return json({
          status: inst.status,
          power_status: inst.power_status,
          server_status: inst.server_status,
          ip: inst.main_ip,
          region: inst.region,
        }, upstream.status);
      } catch {
        return new Response(upstreamText, { status: upstream.status, headers: { "Content-Type": "application/json" } });
      }
    }

    return json({
      ok: upstream.status >= 200 && upstream.status < 300,
      action: def.description,
      vultr_status: upstream.status,
    }, upstream.status);
  },
};

function json(obj, status = 200) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

async function verifyEd25519(pubkeyB64, signatureB64, message) {
  try {
    const pubkey = b64decode(pubkeyB64);
    const signature = b64decode(signatureB64);
    if (pubkey.length !== 32) return false;
    if (signature.length !== 64) return false;
    const data = new TextEncoder().encode(message);
    const cryptoKey = await crypto.subtle.importKey(
      "raw",
      pubkey,
      { name: "Ed25519" },
      false,
      ["verify"]
    );
    return await crypto.subtle.verify("Ed25519", cryptoKey, signature, data);
  } catch (err) {
    console.log(`verifyEd25519 error: ${err.message}`);
    return false;
  }
}

function b64decode(s) {
  const bin = atob(s);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}
