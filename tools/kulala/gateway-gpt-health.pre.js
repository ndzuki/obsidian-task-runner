const { spawnSync } = require("child_process");

// Prefer an explicitly exported key; otherwise use the local OMP KeePassXC
// adapter. Never include the key in logs or error messages.
const directKey = process.env.CODEX_API_KEY || process.env.GATEWAY_API_KEY;
const keyScript = process.env.OMP_KEY_SCRIPT || `${process.env.HOME}/.omp/get-api-key.sh`;
const keyEntry = process.env.OMP_KEY_ENTRY || "CODEX_API_KEY";

let apiKey = directKey;
if (!apiKey) {
  const result = spawnSync("bash", [keyScript], {
    env: { ...process.env, API_KEY: keyEntry },
    encoding: "utf8",
  });

  if (result.error || result.status !== 0 || !result.stdout || !result.stdout.trim()) {
    throw new Error(
      "Gateway API key unavailable. Set CODEX_API_KEY/GATEWAY_API_KEY or configure OMP_KEY_SCRIPT.",
    );
  }

  apiKey = result.stdout.trim();
}

request.variables.set("GATEWAY_API_KEY", apiKey);
request.variables.set("PROBE_STARTED_AT", String(Date.now()));
request.variables.set(
  "REQUEST_ID",
  `kulala-${Date.now()}-${Math.random().toString(16).slice(2)}`,
);
