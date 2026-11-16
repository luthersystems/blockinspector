// Block explorer UI (luthersystems/blockinspector#30). It reads only the API
// v1 that `blockinspector serve` serves (docs/api.md), from the same origin,
// and sends no transaction.
//
// Values from the ledger are untrusted: this file puts them in the page only
// as text (textContent, attribute values), never as HTML.
"use strict";

(function () {
  const main = document.getElementById("main");
  const statusEl = document.getElementById("status");

  // ---- DOM helpers --------------------------------------------------------

  // h builds an element. attrs sets properties (class, href, title, ...) and
  // "on*" listeners; children are nodes or strings (as text), and null,
  // undefined and false are skipped. Arrays are flattened.
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) {
      for (const [k, v] of Object.entries(attrs)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === "class") el.className = v;
        else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
        else if (k === "dataset") Object.assign(el.dataset, v);
        else el.setAttribute(k, v === true ? "" : String(v));
      }
    }
    append(el, children);
    return el;
  }

  function append(el, children) {
    for (const c of children) {
      if (c === undefined || c === null || c === false) continue;
      if (Array.isArray(c)) append(el, c);
      else if (c instanceof Node) el.appendChild(c);
      else el.appendChild(document.createTextNode(String(c)));
    }
  }

  function clear(el) {
    while (el.firstChild) el.removeChild(el.firstChild);
  }

  function hashLink(path, params) {
    let s = "#" + path;
    if (params) {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(params)) {
        if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
      }
      const qs = q.toString();
      if (qs) s += "?" + qs;
    }
    return s;
  }

  function blockLink(n) {
    return h("a", { href: hashLink("/blocks/" + n) }, String(n));
  }

  function txLink(id, short) {
    const text = short && id.length > 16 ? id.slice(0, 12) + "…" : id;
    return h("a", { href: hashLink("/tx/" + encodeURIComponent(id)), class: "mono", title: id }, text);
  }

  // keyText shows a ledger key with the Fabric composite key separator
  // (U+0000) and other control characters visible.
  function keyText(key) {
    const span = h("span", { class: "mono break" });
    let run = "";
    const flush = () => {
      if (run) span.appendChild(document.createTextNode(run));
      run = "";
    };
    for (const ch of key) {
      const cp = ch.codePointAt(0);
      if (cp < 0x20 || cp === 0x7f) {
        flush();
        const label = cp === 0 ? "␀" : "\\x" + cp.toString(16).padStart(2, "0");
        span.appendChild(h("span", { class: "nul", title: "U+" + cp.toString(16).toUpperCase().padStart(4, "0") }, label));
      } else {
        run += ch;
      }
    }
    flush();
    return span;
  }

  function keyLink(namespace, key) {
    return h("a", { href: hashLink("/key", { namespace: namespace, key: key }) }, keyText(key));
  }

  function fmtTime(t) {
    if (!t) return h("span", { class: "muted" }, "—");
    const d = new Date(t);
    if (isNaN(d.getTime())) return t;
    return h("span", { title: t }, d.toLocaleString());
  }

  function fmtBytes(n) {
    if (n === undefined || n === null) return "";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KiB";
    return (n / (1024 * 1024)).toFixed(1) + " MiB";
  }

  function validBadge(code, valid) {
    return h("span", { class: "badge " + (valid ? "ok" : "bad") }, code || (valid ? "VALID" : "INVALID"));
  }

  function methodText(tx) {
    if (tx.method === "batch" && tx.batch_methods && tx.batch_methods.length) {
      return "batch: " + tx.batch_methods.join(", ");
    }
    return tx.method || "";
  }

  function table(headers, rows) {
    return h("div", { class: "wrap" },
      h("table", null,
        h("thead", null, h("tr", null, headers.map((x) => {
          const num = x.startsWith("#");
          return h("th", { class: num ? "num" : null }, num ? x.slice(1) : x);
        }))),
        h("tbody", null, rows)));
  }

  function fields(pairs) {
    const dl = h("dl", { class: "fields" });
    for (const [k, v] of pairs) {
      if (v === undefined || v === null || v === "") continue;
      dl.appendChild(h("dt", null, k));
      dl.appendChild(h("dd", null, v));
    }
    return dl;
  }

  // ---- Values -------------------------------------------------------------

  function decodeBase64(s) {
    const bin = atob(s);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  const hexLimit = 4096;

  function hexDump(bytes) {
    const lines = [];
    const n = Math.min(bytes.length, hexLimit);
    for (let off = 0; off < n; off += 16) {
      const row = [];
      for (let i = off; i < Math.min(off + 16, n); i++) row.push(bytes[i].toString(16).padStart(2, "0"));
      lines.push(off.toString(16).padStart(8, "0") + "  " + row.join(" "));
    }
    if (bytes.length > n) lines.push("… " + (bytes.length - n) + " more bytes");
    return lines.join("\n");
  }

  // valueText returns the text to show for an API value and its kind:
  // "json" (formatted), "text" or "hex".
  function valueText(v) {
    if (v.encoding === "base64") {
      try {
        return { kind: "hex", text: hexDump(decodeBase64(v.data)) };
      } catch (e) {
        return { kind: "hex", text: v.data };
      }
    }
    const t = v.data.trim();
    if (t.startsWith("{") || t.startsWith("[")) {
      try {
        return { kind: "json", text: JSON.stringify(JSON.parse(t), null, 2) };
      } catch (e) {
        // not JSON: show it as text
      }
    }
    return { kind: "text", text: v.data };
  }

  function valueView(v, label) {
    if (!v) return h("span", { class: "muted" }, "—");
    const vt = valueText(v);
    return h("div", null,
      h("div", { class: "value-meta" }, [label ? label + " · " : "", vt.kind, " · ", fmtBytes(v.size)]),
      h("pre", { class: "value" }, vt.text));
  }

  // lineDiff returns the lines of a and b as [op, line] with op " ", "-" or
  // "+", from a longest common subsequence. Large inputs return null.
  function lineDiff(a, b) {
    const x = a.split("\n");
    const y = b.split("\n");
    if (x.length * y.length > 4000000) return null;
    const m = x.length;
    const n = y.length;
    const dp = new Array(m + 1);
    for (let i = 0; i <= m; i++) dp[i] = new Uint32Array(n + 1);
    for (let i = m - 1; i >= 0; i--) {
      for (let j = n - 1; j >= 0; j--) {
        dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
    const out = [];
    let i = 0;
    let j = 0;
    while (i < m && j < n) {
      if (x[i] === y[j]) {
        out.push([" ", x[i]]);
        i++;
        j++;
      } else if (dp[i + 1][j] >= dp[i][j + 1]) {
        out.push(["-", x[i++]]);
      } else {
        out.push(["+", y[j++]]);
      }
    }
    while (i < m) out.push(["-", x[i++]]);
    while (j < n) out.push(["+", y[j++]]);
    return out;
  }

  function diffView(before, after) {
    const a = valueText(before);
    const b = valueText(after);
    if (a.kind !== "json" || b.kind !== "json") return null;
    const d = lineDiff(a.text, b.text);
    if (!d) return null;
    return h("div", { class: "diff" }, d.map(([op, line]) =>
      h("div", { class: op === "+" ? "add" : op === "-" ? "del" : null }, op + " " + line)));
  }

  // ---- API ----------------------------------------------------------------

  async function api(path, params) {
    let url = path;
    if (params) {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(params)) {
        if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
      }
      const qs = q.toString();
      if (qs) url += "?" + qs;
    }
    const resp = await fetch(url, { headers: { Accept: "application/json" } });
    let body = null;
    try {
      body = await resp.json();
    } catch (e) {
      // body stays null
    }
    if (!resp.ok) {
      const err = new Error((body && body.error) || resp.status + " " + resp.statusText);
      err.status = resp.status;
      throw err;
    }
    return body;
  }

  // ---- Status bar ---------------------------------------------------------

  let lastStatus = null;

  async function refreshStatus() {
    try {
      const st = await api("/v1/status");
      lastStatus = st;
      const src = st.source || {};
      const ok = src.connected || src.done;
      let state = src.done ? "all blocks read" : src.connected ? "connected" : "not connected";
      if (src.live === false && !src.done) state = src.connected ? "reading block files" : state;
      clear(statusEl);
      append(statusEl, [
        h("span", { class: "dot " + (ok ? "ok" : "bad") }),
        "Channel ", h("strong", null, st.channel || "—"),
        " · height ", h("strong", null, String(st.height)),
        st.height > 0 ? [" · first block ", String(st.first_block)] : null,
        " · ", src.source ? src.source + ": " : "", state,
        src.last_error ? [" · ", h("span", { class: "err", title: src.last_error_at || "" }, "last error: " + src.last_error)] : null,
      ]);
    } catch (e) {
      clear(statusEl);
      append(statusEl, [h("span", { class: "dot bad" }), h("span", { class: "err" }, "API unreachable: " + e.message)]);
    }
  }

  // ---- Router -------------------------------------------------------------

  let renderSeq = 0;
  let stream = null;

  function closeStream() {
    if (stream) {
      stream.close();
      stream = null;
    }
  }

  function parseHash() {
    let raw = location.hash.slice(1) || "/";
    const qi = raw.indexOf("?");
    const path = qi >= 0 ? raw.slice(0, qi) : raw;
    const query = new URLSearchParams(qi >= 0 ? raw.slice(qi + 1) : "");
    return { path: path || "/", query: query };
  }

  const routes = [
    [/^\/$/, "blocks", pageBlocks],
    [/^\/blocks\/(\d+)$/, "blocks", pageBlock],
    [/^\/transactions$/, "transactions", pageTransactions],
    [/^\/tx\/(.+)$/, "transactions", pageTransaction],
    [/^\/keys$/, "keys", pageKeys],
    [/^\/key$/, "keys", pageKeyHistory],
    [/^\/framework$/, "framework", pageFramework],
    [/^\/phylum$/, "phylum", pagePhylum],
  ];

  async function route() {
    closeStream();
    const seq = ++renderSeq;
    const { path, query } = parseHash();
    let handler = null;
    let match = null;
    let nav = null;
    for (const [re, n, fn] of routes) {
      match = path.match(re);
      if (match) {
        handler = fn;
        nav = n;
        break;
      }
    }
    for (const a of document.querySelectorAll("nav a")) {
      a.classList.toggle("active", a.dataset.nav === nav);
    }
    // isCurrent tells a page whether it is still the one on screen after an
    // await, so a slow answer does not draw over a newer page.
    const isCurrent = () => seq === renderSeq;
    if (!handler) {
      render(isCurrent, h("p", { class: "error" }, "No such page."));
      return;
    }
    try {
      await handler({ match: match, query: query, isCurrent: isCurrent });
    } catch (e) {
      render(isCurrent, h("div", null, h("h1", null, "Error"), h("p", { class: "error" }, e.message)));
    }
  }

  function render(isCurrent, ...nodes) {
    if (!isCurrent()) return false;
    clear(main);
    append(main, nodes);
    window.scrollTo(0, 0);
    return true;
  }

  // ---- Blocks -------------------------------------------------------------

  function blockRow(b, fresh) {
    return h("tr", { class: fresh ? "new" : null, dataset: { number: String(b.number) } },
      h("td", { class: "num" }, blockLink(b.number)),
      h("td", null, fmtTime(b.time)),
      h("td", { class: "num" }, [String(b.valid_tx_count), " of ", String(b.tx_count)]),
      h("td", null, b.config ? h("span", { class: "badge warn" }, "config") : null),
      h("td", { class: "mono break", title: b.data_hash }, b.data_hash ? b.data_hash.slice(0, 16) + "…" : ""),
      h("td", { class: "num" }, fmtBytes(b.size)));
  }

  async function pageBlocks(ctx) {
    const data = await api("/v1/blocks", { limit: 50 });
    const tbody = h("tbody", null, data.blocks.map((b) => blockRow(b, false)));
    const tbl = h("div", { class: "wrap" }, h("table", null,
      h("thead", null, h("tr", null,
        h("th", { class: "num" }, "Block"), h("th", null, "Time"), h("th", { class: "num" }, "Valid transactions"),
        h("th", null, ""), h("th", null, "Data hash"), h("th", { class: "num" }, "Size"))),
      tbody));
    const live = h("span", { class: "muted" }, "Following new blocks…");
    let nextBefore = data.next_before;
    const more = h("button", { class: "more" }, "Older blocks");
    more.hidden = !nextBefore;
    more.addEventListener("click", async () => {
      more.disabled = true;
      try {
        const page = await api("/v1/blocks", { before: nextBefore, limit: 50 });
        for (const b of page.blocks) tbody.appendChild(blockRow(b, false));
        nextBefore = page.next_before;
        more.hidden = !nextBefore;
      } catch (e) {
        more.after(h("p", { class: "error" }, e.message));
      } finally {
        more.disabled = false;
      }
    });
    if (!render(ctx.isCurrent, h("h1", null, "Blocks"), h("p", null, live), tbl,
      data.blocks.length ? null : h("p", { class: "muted" }, "No blocks yet."), more)) return;

    // Follow new blocks. Start after the newest block shown, so none is
    // missed between the list and the stream. EventSource resends the last
    // block number (Last-Event-ID) when it reconnects.
    const seen = new Set(data.blocks.map((b) => b.number));
    const top = data.blocks.length ? data.blocks[0].number + 1 : null;
    const url = top === null ? "/v1/stream/blocks" : "/v1/stream/blocks?from=" + top;
    const es = new EventSource(url);
    stream = es;
    es.addEventListener("open", () => {
      live.textContent = "Following new blocks.";
      live.className = "muted";
    });
    es.addEventListener("error", () => {
      live.textContent = "Stream disconnected; reconnecting…";
      live.className = "error";
    });
    es.addEventListener("block", (ev) => {
      let b;
      try {
        b = JSON.parse(ev.data);
      } catch (e) {
        return;
      }
      if (seen.has(b.number)) return;
      seen.add(b.number);
      // Keep the list in order: put the block above the first older one.
      const row = blockRow(b, true);
      let before = null;
      for (const tr of tbody.children) {
        if (Number(tr.dataset.number) < b.number) {
          before = tr;
          break;
        }
      }
      tbody.insertBefore(row, before);
      refreshStatus();
    });
  }

  function txRow(tx, opts) {
    return h("tr", null,
      opts && opts.block ? h("td", { class: "num" }, blockLink(tx.block_number)) : null,
      h("td", { class: "num" }, String(tx.index)),
      h("td", null, txLink(tx.id, true)),
      h("td", null, tx.type),
      h("td", null, validBadge(tx.validation_code, tx.valid)),
      h("td", null, tx.creator_msp || ""),
      h("td", null, tx.chaincode_id || ""),
      h("td", { class: "mono break" }, methodText(tx)),
      opts && opts.time ? h("td", null, fmtTime(tx.time)) : null);
  }

  async function pageBlock(ctx) {
    const n = Number(ctx.match[1]);
    const b = await api("/v1/blocks/" + n);
    const prev = n > 0 ? h("a", { href: hashLink("/blocks/" + (n - 1)) }, "← Block " + (n - 1)) : null;
    const next = h("a", { href: hashLink("/blocks/" + (n + 1)) }, "Block " + (n + 1) + " →");
    const height = lastStatus ? lastStatus.height : null;
    render(ctx.isCurrent,
      h("h1", null, "Block " + n),
      h("div", { class: "pager" }, prev, height === null || n + 1 < height ? next : null),
      fields([
        ["Time", fmtTime(b.time)],
        ["Transactions", b.valid_tx_count + " valid of " + b.tx_count],
        ["Config block", b.config ? "yes" : "no"],
        ["Size", fmtBytes(b.size)],
        ["Data hash", h("span", { class: "mono" }, b.data_hash)],
        ["Previous hash", h("span", { class: "mono" }, b.previous_hash)],
      ]),
      h("h2", null, "Transactions"),
      table(["#Index", "ID", "Type", "Validation", "Creator MSP", "Chaincode", "Method"],
        (b.transactions || []).map((tx) => txRow(tx))));
  }

  // ---- Transactions -------------------------------------------------------

  async function pageTransactions(ctx) {
    const method = ctx.query.get("method") || "";
    const valid = ctx.query.get("valid") || "";
    const data = await api("/v1/transactions", { method: method, valid: valid, limit: 50 });

    const methodIn = h("input", { type: "text", value: method, placeholder: "any", name: "method" });
    const validSel = h("select", { name: "valid" },
      h("option", { value: "" }, "all"),
      h("option", { value: "true" }, "valid"),
      h("option", { value: "false" }, "invalid"));
    validSel.value = valid;
    const form = h("form", { class: "filters" },
      h("label", null, "Method", methodIn),
      h("label", null, "Validation", validSel),
      h("button", { type: "submit" }, "Filter"));
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      location.hash = hashLink("/transactions", { method: methodIn.value.trim(), valid: validSel.value });
    });

    const tbody = h("tbody", null, data.transactions.map((tx) => txRow(tx, { block: true, time: true })));
    let next = data.next;
    const more = h("button", { class: "more" }, "Older transactions");
    more.hidden = !next;
    more.addEventListener("click", async () => {
      more.disabled = true;
      try {
        const page = await api("/v1/transactions", {
          method: method, valid: valid, limit: 50, before: next.before, before_index: next.before_index,
        });
        for (const tx of page.transactions) tbody.appendChild(txRow(tx, { block: true, time: true }));
        next = page.next;
        more.hidden = !next;
      } catch (e) {
        more.after(h("p", { class: "error" }, e.message));
      } finally {
        more.disabled = false;
      }
    });
    render(ctx.isCurrent,
      h("h1", null, "Transactions"),
      form,
      h("div", { class: "wrap" }, h("table", null,
        h("thead", null, h("tr", null, ["Block", "Index", "ID", "Type", "Validation", "Creator MSP", "Chaincode", "Method", "Time"]
          .map((x) => h("th", { class: x === "Block" || x === "Index" ? "num" : null }, x)))),
        tbody)),
      data.transactions.length ? null : h("p", { class: "muted" }, "No transactions match."),
      more);
  }

  function identity(id) {
    if (!id) return null;
    return [id.msp_id, id.subject ? h("span", { class: "muted" }, " (" + id.subject + ")") : null];
  }

  function rwsetView(tx, rw) {
    const parts = [h("h3", null, ["Namespace ", h("span", { class: "mono" }, rw.namespace)])];
    if (rw.reads && rw.reads.length) {
      parts.push(table(["Read key", "Version (block:tx)"], rw.reads.map((r) =>
        h("tr", null,
          h("td", null, keyLink(rw.namespace, r.key)),
          h("td", { class: "mono" }, r.version ? r.version.block + ":" + r.version.tx : "none")))));
    }
    if (rw.range_queries) parts.push(h("p", { class: "muted" }, rw.range_queries + " range queries"));
    if (rw.writes && rw.writes.length) {
      parts.push(table(["Written key", "Value"], rw.writes.map((w) =>
        h("tr", null,
          h("td", null, tx.valid ? keyLink(rw.namespace, w.key) : keyText(w.key)),
          h("td", null, w.is_delete ? h("span", { class: "badge bad" }, "delete") : valueView(w.value))))));
    }
    if (rw.private_writes && rw.private_writes.length) {
      parts.push(h("p", { class: "muted" }, "A block holds only the hashes of private data."));
      parts.push(table(["Collection", "Key hash", "Value hash"], rw.private_writes.map((p) =>
        h("tr", null,
          h("td", null, p.collection),
          h("td", { class: "mono break" }, p.key_hash),
          h("td", { class: "mono break" }, p.is_purge ? "purge" : p.is_delete ? "delete" : p.value_hash || "")))));
    }
    if (parts.length === 1) parts.push(h("p", { class: "muted" }, "No reads or writes."));
    return parts;
  }

  async function pageTransaction(ctx) {
    const id = decodeURIComponent(ctx.match[1]);
    const tx = await api("/v1/transactions/" + encodeURIComponent(id));
    let request = null;
    if (tx.request !== undefined && tx.request !== null) {
      request = h("pre", { class: "value" }, JSON.stringify(tx.request, null, 2));
    }
    const resp = tx.response;
    const ev = tx.event;
    render(ctx.isCurrent,
      h("h1", { class: "break" }, ["Transaction ", h("span", { class: "mono" }, tx.id)]),
      fields([
        ["Block", [blockLink(tx.block_number), " · index " + tx.index]],
        ["Time", fmtTime(tx.time)],
        ["Type", tx.type],
        ["Validation", validBadge(tx.validation_code, tx.valid)],
        ["Creator", identity(tx.creator) || tx.creator_msp],
        ["Endorsers", (tx.endorsers || []).length ? (tx.endorsers || []).map((e, i) => [i ? ", " : "", identity(e)]) : null],
        ["Chaincode", tx.chaincode_id],
        ["Method", methodText(tx) ? h("a", { href: hashLink("/transactions", { method: tx.method }) }, methodText(tx)) : null],
      ]),
      request ? [h("h2", null, "Request"), request] : null,
      tx.args && tx.args.length ? [h("h2", null, "Arguments"), tx.args.map((a, i) => valueView(a, "arg " + i))] : null,
      resp ? [h("h2", null, "Response"), fields([["Status", String(resp.status)], ["Message", resp.message]]),
        resp.payload ? valueView(resp.payload, "payload") : null] : null,
      ev ? [h("h2", null, "Event"), fields([["Name", ev.name], ["Chaincode", ev.chaincode_id]]),
        ev.payload ? valueView(ev.payload, "payload") : null] : null,
      h("h2", null, "Read and write sets"),
      tx.valid ? null : h("p", { class: "muted" }, "This transaction is not valid, so its writes did not change state."),
      (tx.rwsets || []).length ? tx.rwsets.map((rw) => rwsetView(tx, rw)) : h("p", { class: "muted" }, "None."));
  }

  // ---- Keys ---------------------------------------------------------------

  function keyRow(k) {
    return h("tr", null,
      h("td", { class: "mono" }, k.namespace),
      h("td", null, keyLink(k.namespace, k.key)),
      h("td", null, k.space ? h("a", { href: hashLink("/keys", { space: k.space }), class: "mono" }, k.space) : h("span", { class: "muted" }, "phylum")),
      h("td", { class: "num" }, String(k.writes)),
      h("td", { class: "num" }, k.is_delete ? h("span", { class: "badge bad" }, "deleted") : fmtBytes(k.size)),
      h("td", null, [txLink(k.last_write.tx_id, true), " in ", blockLink(k.last_write.block_number)]),
      h("td", null, fmtTime(k.last_write.time)));
  }

  async function pageKeys(ctx) {
    const q = {
      kind: ctx.query.get("kind") || "",
      space: ctx.query.get("space") || "",
      namespace: ctx.query.get("namespace") || "",
      prefix: ctx.query.get("prefix") || "",
    };
    const data = await api("/v1/keys", Object.assign({ limit: 100 }, q));

    const tab = (label, kind) => h("a", {
      href: hashLink("/keys", { kind: kind, namespace: q.namespace, prefix: q.prefix }),
      class: !q.space && q.kind === kind ? "active" : null,
    }, label);
    const nsIn = h("input", { type: "text", value: q.namespace, placeholder: "any", name: "namespace" });
    const prefixIn = h("input", { type: "text", value: q.prefix, placeholder: "any", name: "prefix" });
    const form = h("form", { class: "filters" },
      h("label", null, "Namespace", nsIn),
      h("label", null, "Key prefix", prefixIn),
      h("button", { type: "submit" }, "Filter"));
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      location.hash = hashLink("/keys", { kind: q.kind, space: q.space, namespace: nsIn.value.trim(), prefix: prefixIn.value });
    });

    const tbody = h("tbody", null, data.keys.map(keyRow));
    let next = data.next;
    const more = h("button", { class: "more" }, "More keys");
    more.hidden = !next;
    more.addEventListener("click", async () => {
      more.disabled = true;
      try {
        const page = await api("/v1/keys", Object.assign({
          limit: 100, after_namespace: next.after_namespace, after_key: next.after_key,
        }, q));
        for (const k of page.keys) tbody.appendChild(keyRow(k));
        next = page.next;
        more.hidden = !next;
      } catch (e) {
        more.after(h("p", { class: "error" }, e.message));
      } finally {
        more.disabled = false;
      }
    });
    render(ctx.isCurrent,
      h("h1", null, q.space ? ["Keys in ", h("span", { class: "mono" }, q.space)] : "Keys"),
      h("div", { class: "tabs" }, tab("All", ""), tab("Phylum keys", "phylum"), tab("Framework keys", "framework")),
      form,
      h("p", { class: "muted" }, "Keys that valid transactions wrote, with their last write. ",
        h("a", { href: hashLink("/framework") }, "Key spaces"), " groups the framework keys."),
      h("div", { class: "wrap" }, h("table", null,
        h("thead", null, h("tr", null, ["Namespace", "Key", "Space", "#Writes", "#Size", "Last write", "Time"]
          .map((x) => h("th", { class: x.startsWith("#") ? "num" : null }, x.replace("#", ""))))),
        tbody)),
      data.keys.length ? null : h("p", { class: "muted" }, "No keys match."),
      more);
  }

  async function pageKeyHistory(ctx) {
    const ns = ctx.query.get("namespace") || "";
    const key = ctx.query.get("key");
    if (!ns || key === null) {
      render(ctx.isCurrent, h("p", { class: "error" }, "A key history needs a namespace and a key."));
      return;
    }
    const data = await api("/v1/keys/history", { namespace: ns, key: key });
    const writes = data.writes.map((w, i) => {
      let body;
      if (w.is_delete) {
        body = h("div", { class: "cols" }, valueView(w.before, "before"), h("div", null, h("span", { class: "badge bad" }, "deleted")));
      } else {
        const diff = w.before && w.after ? diffView(w.before, w.after) : null;
        body = [
          h("div", { class: "cols" }, valueView(w.before, "before"), valueView(w.after, "after")),
          diff ? [h("h3", null, "Change"), diff] : null,
        ];
      }
      return h("div", { class: "write" },
        h("div", { class: "write-head" },
          h("strong", null, "#" + (i + 1)),
          txLink(w.tx_id, true),
          h("span", null, ["block ", blockLink(w.block_number), " · index " + w.tx_index]),
          w.method ? h("span", { class: "mono" }, w.method) : null,
          fmtTime(w.time)),
        body);
    });
    render(ctx.isCurrent,
      h("h1", null, "Key history"),
      fields([
        ["Namespace", h("span", { class: "mono" }, data.namespace)],
        ["Key", keyText(data.key)],
        ["Space", data.space ? h("a", { href: hashLink("/keys", { space: data.space }), class: "mono" }, data.space) : "phylum key"],
        ["Writes", String(data.writes.length)],
      ]),
      h("h2", null, "Writes, oldest first"),
      writes);
  }

  async function pageFramework(ctx) {
    const data = await api("/v1/framework-keys");
    const row = (s, label, href) => h("tr", null,
      h("td", null, h("a", { href: href, class: "mono" }, label)),
      h("td", null, s.owner || ""),
      h("td", { class: "num" }, String(s.keys)),
      h("td", { class: "num" }, String(s.live_keys)),
      h("td", { class: "num" }, String(s.writes)));
    render(ctx.isCurrent,
      h("h1", null, "Key spaces"),
      h("p", { class: "muted" }, "Substrate reserves the keys under luther: (and the utl:migration spaces) for the framework. Every other key is a phylum key."),
      table(["Space", "Owner", "#Keys", "#Live keys", "#Writes"], [
        row(data.phylum, "phylum keys", hashLink("/keys", { kind: "phylum" })),
        data.spaces.map((s) => row(s, s.space + (s.exact ? " (key)" : ""), hashLink("/keys", { space: s.space }))),
      ]));
  }

  function statusBadge(status, deleted) {
    if (deleted) return h("span", { class: "badge bad" }, "deleted");
    const cls = status === "IN_SERVICE" ? "ok" : status === "DISABLED" ? "warn" : null;
    return h("span", { class: "badge " + (cls || "") }, status || "unknown");
  }

  async function pagePhylum(ctx) {
    const data = await api("/v1/phylum-versions");
    const versions = data.versions.map((v) => h("div", { class: "write" },
      h("div", { class: "write-head" },
        h("strong", { class: "mono" }, v.version),
        statusBadge(v.status, v.deleted),
        h("span", { class: "muted mono" }, v.namespace)),
      fields([
        ["Fingerprint", v.fingerprint ? h("span", { class: "mono" }, v.fingerprint) : null],
        ["Init timestamp", v.init_timestamp],
        ["Source size", v.source_size ? fmtBytes(v.source_size) : null],
      ]),
      table(["Action", "Status", "Method", "Block", "Transaction", "Time"], (v.events || []).map((e) =>
        h("tr", null,
          h("td", null, e.action),
          h("td", null, e.status || ""),
          h("td", { class: "mono" }, e.method || ""),
          h("td", null, blockLink(e.block_number)),
          h("td", null, txLink(e.tx_id, true)),
          h("td", null, fmtTime(e.time)))))));
    render(ctx.isCurrent,
      h("h1", null, "Phylum versions"),
      versions.length ? versions : h("p", { class: "muted" }, "No phylum version on this channel yet."));
  }

  // ---- Start --------------------------------------------------------------

  document.getElementById("search").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const input = document.getElementById("search-input");
    const v = input.value.trim();
    if (!v) return;
    location.hash = /^\d+$/.test(v) ? hashLink("/blocks/" + v) : hashLink("/tx/" + encodeURIComponent(v));
    input.value = "";
  });

  window.addEventListener("hashchange", route);
  refreshStatus();
  setInterval(refreshStatus, 5000);
  route();
})();
