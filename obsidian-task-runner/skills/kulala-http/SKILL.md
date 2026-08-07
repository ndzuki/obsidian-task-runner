---
name: kulala-http
description: >
  Write, review, debug, or verify Kulala.nvim `.http`/`.rest` collections,
  `http-client.env.json`, Kulala pre/post-request scripts, API assertions,
  secret-manager integrations, or `@kulala-*` operators. Trigger when a task
  asks for an HTTP debug script, REST collection, API smoke probe, or Kulala
  request file.
alwaysApply: false
hide: false
disableModelInvocation: false
---

# Kulala HTTP Authoring

Use this skill for **Kulala-specific request files**, not for generic curl-only answers. Keep the interface small: produce a runnable `.http` collection, a safe environment/secret seam, and observable assertions.

## Workflow

### 1. Load the local reference first

Run the local knowledge search before writing syntax:

```bash
otg kb search "Kulala HTTP client variables environments scripting testing"
```

Read the matching `References/` document. If it is missing or stale, consult the official usage index: `https://kulala.app/usage`. Prefer the current `kulala-core` syntax over older snippets.

### 2. Inspect repository conventions

Before adding files, inspect:

- existing `.http`/`.rest` collections;
- `http-client.env.json`, `http-client.private.env.json`, `.env`, and `.gitignore`;
- service base URLs, auth conventions, and safe/non-mutating health endpoints;
- existing pre/post scripts and test commands.

Reuse the nearest layout. For a non-service repository, `tools/kulala/` is acceptable; for a Go service using the project scaffold, prefer `api/kulala/`.

### 3. Author the collection

- Separate requests with `###` and give every reusable request a stable name.
- Use document variables (`@NAME=value`) for non-secret constants.
- Use `{{ $env.NAME }}` for process environment variables; plain `{{NAME}}` is not an OS environment lookup.
- Keep request line, headers, blank line, and body in that order.
- Use explicit `Content-Type: application/json` for JSON bodies.
- Use current operators such as `@kulala-expect-status-code`, `@timeout`, `@connection-timeout`, `@no-cookie-jar`, and `@kulala-curl--*`.
- Do not copy legacy `@curl-*` operators without checking the migration rules.

### 4. Protect credentials

Never write a real key, password, cookie, or token into a tracked `.http`, public env file, Markdown document, or log.

Choose one seam:

1. `{{ $env.API_TOKEN }}` when the caller exports the secret;
2. `http-client.private.env.json` when the secret is local and the file is ignored;
3. a pre-request script that calls an approved secret manager and sets a request-scoped variable.

For secret-bearing requests, add `@no-log` when request logging could expose headers. Do not log the resolved Authorization header. Keep secret lookup failures explicit so the request is not sent with an empty credential.

### 5. Add scripts only when they earn their cost

Use `< ./pre.js` for authentication, generated variables, or request setup. Use `> {% ... %}` or `> ./post.js` for response logging, extraction, replay, and assertions.

Useful APIs include:

```javascript
request.variables.set("NAME", "value");
request.variables.get("NAME");
client.global.set("NAME", "value");
client.log("key", value);
client.test("status", function() {
  client.assert(response.status === 200, "unexpected status");
});
```

Pre-request errors must stop the request. Post-request errors should preserve the HTTP response while reporting the assertion failure. Prefer request-scoped variables for secrets and short-lived probe metadata.

### 6. Make diagnostics falsifiable

For health or stability probes, use a safe deterministic prompt or read-only endpoint and record:

- HTTP status;
- elapsed time or TTFT when the runner exposes it;
- response/request ID;
- returned model or service identity;
- usage/error category;
- whether a fallback was used.

Split probes by layer: DNS/TLS/auth, provider endpoint, actual model request, then tool/session integration. Do not infer an upstream outage from a local `repo busy`, `phase gate full`, MCP startup delay, or an OMP session error without the underlying HTTP/provider evidence.

### 7. Verify before delivery

Complete all applicable checks:

- every `{{...}}` variable is defined by the file, selected environment, process environment, or an intentional script;
- no credential literal appears in tracked files;
- request names and `###` separators parse structurally;
- external JavaScript passes `node --check` when Node is available;
- if `kulala-fmt` or a project parser is installed, run its check/format command;
- send at least one safe request through Kulala when the plugin and target are available;
- otherwise run an equivalent direct HTTP/OMP probe and state that Kulala runtime execution was not available.

Do not claim a Kulala smoke test from a curl or OMP-only test.

## Knowledge and tags

When adding reusable Kulala knowledge, keep one source of truth in the local `References/` document and include explicit frontmatter tags. Recommended vocabulary:

```yaml
topics: [kulala, http-client, rest, neovim, api-testing, scripting, secrets]
tags: [kulala, http-client, rest-client, api-testing, neovim]
aliases: [Kulala.nvim, .http 文件, REST Client, API 调试]
```

For project tasks, tags improve retrieval and classification; the durable task-to-document link remains `knowledge_refs` written by planning/knowledge-base flow. Add both when a task depends on a specific reference.

## Output contract

Report:

- files created or updated;
- request names and safe execution order;
- secret source without revealing its value;
- verification actually performed;
- any unavailable Kulala runtime or unverified provider behavior.
