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

  // withQuery appends params to a path as a query string, leaving out empty
  // values. Hash links and API calls both use it.
  function withQuery(path, params) {
    if (!params) return path;
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
    }
    const qs = q.toString();
    return qs ? path + "?" + qs : path;
  }

  function hashLink(path, params) {
    return withQuery("#" + path, params);
  }

  // go shows a page. Setting the hash it already has fires no hashchange,
  // so then it renders again (a repeated filter or search refreshes).
  function go(hash) {
    if (location.hash === hash) route();
    else location.hash = hash;
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

  // table builds a table. A header that starts with "#" is a number
  // column. body is a tbody element (for a list that grows) or its rows.
  function table(headers, body) {
    const tbody = body instanceof HTMLTableSectionElement ? body : h("tbody", null, body);
    return h("div", { class: "wrap" },
      h("table", null,
        h("thead", null, h("tr", null, headers.map((x) => {
          const num = x.startsWith("#");
          return h("th", { class: num ? "num" : null }, num ? x.slice(1) : x);
        }))),
        tbody));
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
    const resp = await fetch(withQuery(path, params), { headers: { Accept: "application/json" } });
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
  let statusSeq = 0;
  let statusTimer = null;

  // scheduleStatus refreshes the status bar soon, once for a burst of new
  // blocks.
  function scheduleStatus() {
    if (statusTimer) return;
    statusTimer = setTimeout(() => {
      statusTimer = null;
      refreshStatus();
    }, 500);
  }

  async function refreshStatus() {
    const seq = ++statusSeq;
    try {
      const st = await api("/v1/status");
      // A newer request is on its way or done: do not draw an older answer.
      if (seq !== statusSeq) return;
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
      if (seq !== statusSeq) return;
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
    [/^\/flows$/, "flows", pageFlows],
    [/^\/flows\/([^/]+)$/, "flows", pageFlowRuns],
    [/^\/flows\/([^/]+)\/runs\/([^/]+)$/, "flows", pageFlowRun],
    [/^\/batches$/, "batches", pageBatches],
    [/^\/batch$/, "batches", pageBatchQueue],
    [/^\/connector$/, "connector", pageConnector],
    [/^\/connector\/(.+)$/, "connector", pageConnectorRequest],
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
    const tbl = table(["#Block", "Time", "#Valid transactions", "", "Data hash", "#Size"], tbody);
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
    // With no block listed, start at the first block the explorer holds
    // (from=0), so a block indexed before the stream connects still shows.
    const from = data.blocks.length ? data.blocks[0].number + 1 : 0;
    const url = "/v1/stream/blocks?from=" + from;
    const es = new EventSource(url);
    stream = es;
    es.addEventListener("open", () => {
      live.textContent = "Following new blocks.";
      live.className = "muted";
    });
    es.addEventListener("error", () => {
      // The browser retries a dropped stream, but not a refused one.
      live.textContent = es.readyState === EventSource.CLOSED
        ? "Stream closed. Reload the page to follow new blocks."
        : "Stream disconnected; reconnecting…";
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
      scheduleStatus();
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
    render(ctx.isCurrent,
      h("h1", null, "Block " + n),
      h("div", { class: "pager" }, prev, next),
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
      go(hashLink("/transactions", { method: methodIn.value.trim(), valid: validSel.value }));
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
      table(["#Block", "#Index", "ID", "Type", "Validation", "Creator MSP", "Chaincode", "Method", "Time"], tbody),
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
          // A key read with no version was never written: it has no history.
          h("td", null, r.version ? keyLink(rw.namespace, r.key) : keyText(r.key)),
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
      go(hashLink("/keys", { kind: q.kind, space: q.space, namespace: nsIn.value.trim(), prefix: prefixIn.value }));
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
      table(["Namespace", "Key", "Space", "#Writes", "#Size", "Last write", "Time"], tbody),
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


  // ---- DefFlow ------------------------------------------------------------

  // A flow header stores Unix nanoseconds.
  function fmtUnixNano(ns) {
    if (!ns) return h("span", { class: "muted" }, "—");
    const d = new Date(Math.floor(ns / 1e6));
    return isNaN(d.getTime()) ? String(ns) : fmtTime(d.toISOString());
  }

  function flowStateBadge(state) {
    const cls = state === "done" ? "ok" : state === "failed" ? "bad" : state === "waiting" ? "warn" : "";
    return h("span", { class: "badge " + cls }, state || "unknown");
  }

  function countsText(m) {
    const parts = Object.keys(m || {}).sort().map((k) => k + " " + m[k]);
    return parts.length ? parts.join(", ") : "";
  }

  function flowRunLink(flow, id, namespace, short) {
    const text = short && id.length > 16 ? id.slice(0, 12) + "…" : id;
    return h("a", {
      href: hashLink("/flows/" + encodeURIComponent(flow) + "/runs/" + encodeURIComponent(id), { namespace: namespace }),
      class: "mono", title: id,
    }, text);
  }

  async function pageFlows(ctx) {
    const data = await api("/v1/flows");
    render(ctx.isCurrent,
      h("h1", null, "Flows"),
      h("p", { class: "muted" }, "DefFlow runs in public state. A flow with a private store keeps only hashes on the ledger and does not show here."),
      table(["Flow", "Namespace", "#Runs", "By state", "By kind", "#Parked", "Manifests"], data.flows.map((f) =>
        h("tr", null,
          h("td", null, h("a", { href: hashLink("/flows/" + encodeURIComponent(f.flow), { namespace: f.namespace }), class: "mono" }, f.flow)),
          h("td", { class: "mono" }, f.namespace),
          h("td", { class: "num" }, String(f.runs)),
          h("td", null, countsText(f.by_state),
            f.undecodable ? h("span", { class: "muted" }, (countsText(f.by_state) ? ", " : "") + f.undecodable + " other format") : null),
          h("td", null, countsText(f.by_kind)),
          h("td", { class: "num" }, String(f.parked)),
          h("td", { class: "mono" }, (f.manifests || []).join(", "))))),
      data.flows.length ? null : h("p", { class: "muted" }, "No flow runs on this channel yet."));
  }

  function flowRunRow(r) {
    const hd = r.header;
    return h("tr", null,
      h("td", null, flowRunLink(r.flow, r.id, r.namespace, true)),
      h("td", null, hd ? flowStateBadge(hd.state) : h("span", { class: "badge" }, "format " + rawVersion(r.raw))),
      h("td", { class: "mono" }, hd ? hd.label || "" : ""),
      h("td", null, hd ? hd.kind || "" : ""),
      h("td", { class: "mono" }, hd ? hd.phylum || "" : ""),
      h("td", { class: "mono" }, hd ? hd.caller || "" : ""),
      h("td", null, hd ? fmtUnixNano(hd.updated_at) : ""),
      h("td", null, txLink(r.last_write.tx_id, true)));
  }

  function rawVersion(raw) {
    return raw && typeof raw === "object" && raw.v !== undefined ? String(raw.v) : "?";
  }

  async function pageFlowRuns(ctx) {
    const flow = decodeURIComponent(ctx.match[1]);
    const names = ["state", "kind", "label", "phylum", "caller", "q"];
    const params = { namespace: ctx.query.get("namespace") || "" };
    for (const n of names) params[n] = ctx.query.get(n) || "";
    const data = await api("/v1/flows/" + encodeURIComponent(flow) + "/runs", Object.assign({ limit: 200 }, params));

    const stateSel = h("select", { name: "state" },
      ["", "waiting", "done", "failed"].map((v) => h("option", { value: v }, v || "any")));
    stateSel.value = params.state;
    const kindSel = h("select", { name: "kind" },
      ["", "input", "connector", "flow"].map((v) => h("option", { value: v }, v || "any")));
    kindSel.value = params.kind;
    const inputs = {};
    for (const [n, label, ph] of [["label", "Label", "any"], ["phylum", "Phylum", "any"], ["caller", "Caller", "any"], ["q", "Run ID", "prefix or part"]]) {
      inputs[n] = h("input", { type: "text", value: params[n], placeholder: ph, name: n });
    }
    const form = h("form", { class: "filters" },
      h("label", null, "State", stateSel),
      h("label", null, "Waiting on", kindSel),
      h("label", null, "Label", inputs.label),
      h("label", null, "Phylum", inputs.phylum),
      h("label", null, "Caller", inputs.caller),
      h("label", null, "Run ID", inputs.q),
      h("button", { type: "submit" }, "Filter"));
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      go(hashLink("/flows/" + encodeURIComponent(flow), {
        namespace: params.namespace, state: stateSel.value, kind: kindSel.value,
        label: inputs.label.value.trim(), phylum: inputs.phylum.value.trim(),
        caller: inputs.caller.value.trim(), q: inputs.q.value.trim(),
      }));
    });
    render(ctx.isCurrent,
      h("h1", null, "Flow ", h("span", { class: "mono" }, flow)),
      h("p", null, h("a", { href: hashLink("/flows") }, "All flows")),
      form,
      table(["Run", "State", "Label", "Waiting on", "Phylum", "Caller", "Updated", "Last write"], data.runs.map(flowRunRow)),
      data.runs.length ? null : h("p", { class: "muted" }, "No runs match."),
      data.runs.length >= 200 ? h("p", { class: "muted" }, "Showing the 200 most recently written runs. Filter to narrow the list.") : null);
  }

  async function pageFlowRun(ctx) {
    const flow = decodeURIComponent(ctx.match[1]);
    const id = decodeURIComponent(ctx.match[2]);
    const namespace = ctx.query.get("namespace") || "";
    const p = await api("/v1/flows/" + encodeURIComponent(flow) + "/runs/" + encodeURIComponent(id), { namespace: namespace });
    const hd = p.header;
    const flowHref = hashLink("/flows/" + encodeURIComponent(flow), { namespace: p.namespace });
    const out = [
      h("h1", null, "Run ", h("span", { class: "mono break" }, p.id)),
      h("p", null, "Flow ", h("a", { href: flowHref, class: "mono" }, p.flow), " on ", h("span", { class: "mono" }, p.namespace)),
    ];
    if (hd) {
      out.push(fields([
        ["State", flowStateBadge(hd.state)],
        ["Label", hd.label ? h("span", { class: "mono" }, hd.label) : null],
        ["Waiting on", hd.kind],
        ["Input from", hd.from && hd.from.length ? hd.from.join(", ") : null],
        ["Due", hd.due_at ? fmtUnixNano(hd.due_at) : null],
        ["Generation", String(hd.gen)],
        ["History rows", String(hd.hist)],
        ["Inputs", hd.inputs ? String(hd.inputs) : null],
        ["Attempt", hd.attempt ? String(hd.attempt) + (hd.retry_cap ? " of " + hd.retry_cap : "") : null],
        ["Flow version", h("span", { class: "mono" }, hd.version)],
        ["Manifest", hd.manifest ? h("span", { class: "mono" }, hd.manifest) : null],
        ["Phylum", hd.phylum ? h("span", { class: "mono" }, hd.phylum) : null],
        ["Pinned", hd.pinned ? "yes" : null],
        ["Caller", hd.caller ? h("span", { class: "mono" }, hd.caller) : null],
        ["Parent", hd.parent ? [flowRunLink(hd.parent.flow, hd.parent.run, p.namespace, false),
          h("span", { class: "muted" }, " (" + hd.parent.flow + (hd.parent.label ? ", " + hd.parent.label : "") + ")")] : null],
        ["Child", hd.child ? flowRunLink(hd.child_flow || p.flow, hd.child, p.namespace, false) : null],
        ["Depth", hd.depth ? String(hd.depth) : null],
        ["Started", fmtUnixNano(hd.started_at)],
        ["Updated", fmtUnixNano(hd.updated_at)],
        ["Last write", txLink(p.last_write.tx_id, false)],
        ["Result", p.has_result ? h("span", { class: "muted" }, "stored, not shown") : null],
        ["Saved locals", p.has_vars ? h("span", { class: "muted" }, "stored, not shown") : null],
      ]));
      if (hd.error) {
        const e = hd.error;
        out.push(h("h2", null, "Error"), fields([
          ["Reason", h("span", { class: "badge bad" }, e.reason)],
          ["Kind", e.kind],
          ["Label", e.label ? h("span", { class: "mono" }, e.label) : null],
          ["Attempts", e.attempts ? String(e.attempts) : null],
          ["Message", e.message],
        ]), h("p", { class: "muted" }, "The error's data is not shown: it can hold personal data."));
      }
    } else {
      out.push(h("p", { class: "muted" }, "This header has a format this explorer does not know (version " + rawVersion(p.raw) + "). " +
        "Only its plain fields are shown; the others can hold personal data."),
        h("pre", { class: "value" }, JSON.stringify(p.raw, null, 2)));
    }
    out.push(h("h2", null, "History"),
      table(["#N", "#Gen", "Event", "From", "To", "Sender", "At", "Transaction"], (p.history || []).map((e) =>
        h("tr", null,
          h("td", { class: "num" }, String(e.n)),
          h("td", { class: "num" }, String(e.gen)),
          h("td", null, e.event),
          h("td", { class: "mono" }, e.from || ""),
          h("td", { class: "mono" }, e.to || ""),
          h("td", { class: "mono" }, e.sender || ""),
          h("td", null, fmtTime(e.at)),
          h("td", null, txLink(e.tx_id, true))))),
      (p.history || []).length ? null : h("p", { class: "muted" }, "No history rows in public state."));
    if (p.children && p.children.length) {
      out.push(h("h2", null, "Children"),
        h("ul", null, p.children.map((c) => h("li", null,
          c.flow ? flowRunLink(c.flow, c.run, p.namespace, false) : h("span", { class: "mono" }, c.run),
          c.flow ? h("span", { class: "muted" }, " (" + c.flow + (c.label ? ", at " + c.label : "") + ")") : null))));
    }
    if (p.parked && p.parked.length) {
      out.push(h("h2", null, "Parked"),
        table(["Flow version", "Label", "Outcome", "Target", "Error", "Transaction"], p.parked.map((r) =>
          h("tr", null,
            h("td", { class: "mono" }, r.version),
            h("td", { class: "mono" }, r.row ? r.row.label : ""),
            h("td", null, r.row && r.row.outcome ? r.row.outcome.status : r.row ? "parked" : "unknown format"),
            h("td", { class: "mono" }, r.row && r.row.outcome ? r.row.outcome.target || "" : ""),
            h("td", null, r.row && r.row.outcome ? r.row.outcome.error || "" : ""),
            h("td", null, txLink(r.tx_id, true))))));
    }
    render(ctx.isCurrent, out);
  }


  // ---- Batches ------------------------------------------------------------

  function pausedText(p) {
    if (p === undefined || p === null) return h("span", { class: "muted" }, "unknown");
    return p ? h("span", { class: "badge warn" }, "paused") : "no";
  }

  function batchQueueLink(q) {
    return h("a", { href: hashLink("/batch", { namespace: q.namespace, name: q.name }), class: "mono" }, q.name);
  }

  async function pageBatches(ctx) {
    const data = await api("/v1/batches");
    render(ctx.isCurrent,
      h("h1", null, "Batches"),
      h("p", { class: "muted" }, "Substrate batch queues (luther:batch:). An item is due once its time has passed at the latest block."),
      table(["Queue", "Namespace", "#Prepared", "#Due", "Oldest due", "#Pending batches", "#Pending items", "Paused", "Created"], data.queues.map((q) =>
        h("tr", null,
          h("td", null, batchQueueLink(q)),
          h("td", { class: "mono" }, q.namespace),
          h("td", { class: "num" }, String(q.prepared)),
          h("td", { class: "num" }, String(q.due)),
          h("td", null, fmtTime(q.oldest_due)),
          h("td", { class: "num" }, String(q.pending_batches)),
          h("td", { class: "num" }, String(q.pending_items)),
          h("td", null, pausedText(q.paused)),
          h("td", null, q.created ? "yes" : h("span", { class: "badge warn", title: "No queue record: init did not create this queue" }, "no"))))),
      data.queues.length ? null : h("p", { class: "muted" }, "No batch queue on this channel yet."),
      data.undecodable_keys ? h("p", null, h("span", { class: "badge warn" }, String(data.undecodable_keys)),
        " live keys under luther:batch: have a form this explorer does not know. ",
        h("a", { href: hashLink("/keys", { space: "luther:batch:" }) }, "List them")) : null);
  }

  async function pageBatchQueue(ctx) {
    const namespace = ctx.query.get("namespace") || "";
    const name = ctx.query.get("name") || "";
    const p = await api("/v1/batches/queue", { namespace: namespace, name: name });
    const outcome = (f) => f.outcome === "collected" ? ["collected into ", h("span", { class: "mono" }, f.batch)]
      : f.outcome === "skipped" ? h("span", { class: "badge warn", title: "Its request record was missing or not a JSON object" }, "skipped")
        : f.outcome;
    render(ctx.isCurrent,
      h("h1", null, "Batch queue ", h("span", { class: "mono break" }, p.name)),
      h("p", null, h("a", { href: hashLink("/batches") }, "All queues"), " · ", h("span", { class: "mono" }, p.namespace)),
      fields([
        ["Prepared", String(p.prepared)],
        ["Due", String(p.due) + (p.as_of ? " at the latest block (" + new Date(p.as_of).toLocaleString() + ")" : "")],
        ["Pending batches", String(p.pending_batches) + " (" + p.pending_items + " items)"],
        ["Paused", pausedText(p.paused)],
        ["Created", p.created ? "yes" : "no: init did not create this queue"],
      ]),
      h("h2", null, "Pending batches"),
      h("p", { class: "muted" }, "A processor took these and has not answered. Delivery is at most once, so a batch left here needs an operator."),
      table(["Batch", "Items", "Taken by", "Taken"], p.pending.map((b) =>
        h("tr", null,
          h("td", null, h("a", { href: hashLink("/key", { namespace: p.namespace, key: b.key }), class: "mono" }, b.id)),
          h("td", { class: "mono break" }, b.undecodable ? h("span", { class: "muted" }, "not a list of ids") : b.items.join(", ")),
          h("td", null, txLink(b.taken.tx_id, true)),
          h("td", null, fmtTime(b.taken.time))))),
      p.pending.length ? null : h("p", { class: "muted" }, "None."),
      h("h2", null, "Prepared items"),
      table(["Item", "Due", "Request record", "Scheduled by"], p.items.map((it) =>
        h("tr", null,
          h("td", null, h("a", { href: hashLink("/key", { namespace: p.namespace, key: it.key }), class: "mono" }, it.id)),
          h("td", null, fmtTime(it.due), it.is_due ? [" ", h("span", { class: "badge ok" }, "due")] : null),
          h("td", null, it.has_request ? "yes" : h("span", { class: "badge bad" }, "missing")),
          h("td", null, txLink(it.scheduled.tx_id, true))))),
      p.items.length ? null : h("p", { class: "muted" }, "None."),
      h("h2", null, "Finished items"),
      h("p", { class: "muted" }, "Items that left the queue, latest first (at most 200)."),
      table(["Item", "Due", "Outcome", "Removed by", "Method"], p.finished.map((f) =>
        h("tr", null,
          h("td", null, h("a", { href: hashLink("/key", { namespace: p.namespace, key: f.key }), class: "mono" }, f.id)),
          h("td", null, fmtTime(f.due)),
          h("td", null, outcome(f)),
          h("td", null, txLink(f.removed.tx_id, true)),
          h("td", { class: "mono" }, f.removed.method || "")))),
      p.finished.length ? null : h("p", { class: "muted" }, "None."));
  }


  // ---- Connector Hub ------------------------------------------------------

  function connectorState(r) {
    if (!r.has_callback) return h("span", { class: "badge" }, "no callback");
    if (r.reply) return h("span", { class: "badge ok" }, "replied");
    if (r.dropped) return h("span", { class: "badge bad", title: "The callback state was purged without a reply, for example by a flow timeout" }, "dropped");
    return h("span", { class: "badge warn" }, "open");
  }

  function fmtMS(ms) {
    if (ms === undefined || ms === null) return "";
    return ms < 1000 ? ms + " ms" : (ms / 1000).toFixed(1) + " s";
  }

  function bodyText(r) {
    if (!r.body) return h("span", { class: "muted", title: "Stored at a key other than the request id" }, "other key");
    return r.body === "private" ? "private (" + r.collection + ")" : "public";
  }

  function connectorRequestLink(rid) {
    return h("a", { href: hashLink("/connector/" + encodeURIComponent(rid)), class: "mono", title: rid },
      rid.length > 16 ? rid.slice(0, 13) + "…" : rid);
  }

  async function pageConnector(ctx) {
    const state = ctx.query.get("state") || "";
    const q = ctx.query.get("q") || "";
    const data = await api("/v1/connector/requests", { state: state, q: q, limit: 200 });
    const sum = data.summary;
    const stateSel = h("select", { name: "state" },
      [["", "all"], ["open", "open"], ["replied", "replied"], ["dropped", "dropped"], ["no-callback", "no callback"]]
        .map(([v, l]) => h("option", { value: v }, l)));
    stateSel.value = state;
    const qIn = h("input", { type: "text", value: q, placeholder: "part of an id", name: "q" });
    const form = h("form", { class: "filters" },
      h("label", null, "State", stateSel),
      h("label", null, "Request ID", qIn),
      h("button", { type: "submit" }, "Filter"));
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      go(hashLink("/connector", { state: stateSel.value, q: qIn.value.trim() }));
    });
    render(ctx.isCurrent,
      h("h1", null, "Connector requests"),
      h("p", { class: "muted" }, "Connector events that transactions raised. A request with a callback is open until a $ch_callback transaction purges its callback state (replied), or another transaction does, such as a flow timeout (dropped). A handler that fails commits nothing, so its request stays open. Request bodies are not shown."),
      fields([
        ["Requests", String(sum.requests)],
        ["Open", String(sum.open) + (sum.oldest_open ? " (oldest raised " + new Date(sum.oldest_open).toLocaleString() + ")" : "")],
        ["Replied", String(sum.replied)],
        ["Dropped", String(sum.dropped)],
        ["No callback", String(sum.no_callback)],
      ]),
      form,
      table(["Request", "State", "Raised by", "Method", "Raised", "Body", "Closed by", "#Latency", "Flow run"], data.requests.map((r) =>
        h("tr", null,
          h("td", null, connectorRequestLink(r.rid)),
          h("td", null, connectorState(r)),
          h("td", null, txLink(r.raised.tx_id, true)),
          h("td", { class: "mono" }, r.raised.method || ""),
          h("td", null, fmtTime(r.raised.time)),
          h("td", null, bodyText(r)),
          h("td", null, r.reply ? txLink(r.reply.tx_id, true) : r.dropped ? txLink(r.dropped.tx_id, true) : ""),
          h("td", { class: "num" }, fmtMS(r.latency_ms)),
          h("td", null, r.flow ? flowRunLink(r.flow.flow, r.flow.run, "", true) : "")))),
      data.requests.length ? null : h("p", { class: "muted" }, "No requests match."),
      data.requests.length >= 200 ? h("p", { class: "muted" }, "Showing the 200 most recent. Filter to narrow the list.") : null);
  }

  async function pageConnectorRequest(ctx) {
    const rid = decodeURIComponent(ctx.match[1]);
    const r = await api("/v1/connector/requests/" + encodeURIComponent(rid));
    render(ctx.isCurrent,
      h("h1", null, "Connector request ", h("span", { class: "mono break" }, r.rid)),
      h("p", null, h("a", { href: hashLink("/connector") }, "All requests")),
      fields([
        ["State", connectorState(r)],
        ["Namespace", h("span", { class: "mono" }, r.namespace)],
        ["Raised by", [txLink(r.raised.tx_id, false), " (event " + r.event + ")"]],
        ["Method", r.raised.method ? h("span", { class: "mono" }, r.raised.method) : null],
        ["Raised", fmtTime(r.raised.time)],
        ["Body", [bodyText(r), h("span", { class: "muted" }, " · not shown: a request can hold personal data")]],
        ["Callback", r.has_callback ? "registered" : "none: nothing waits for a reply"],
        ["Reply", r.reply ? txLink(r.reply.tx_id, false) : null],
        ["Replied", r.reply ? fmtTime(r.reply.time) : null],
        ["Latency", r.latency_ms !== undefined ? fmtMS(r.latency_ms) : null],
        ["Dropped by", r.dropped ? [txLink(r.dropped.tx_id, false), r.dropped.method ? h("span", { class: "muted" }, " (" + r.dropped.method + ")") : null] : null],
        ["Flow run", r.flow ? flowRunLink(r.flow.flow, r.flow.run, "", false) : null],
      ]),
      r.has_callback && !r.reply && !r.dropped ? h("p", { class: "muted" }, "No valid transaction has purged this request's callback state yet.") : null);
  }

  // ---- Start --------------------------------------------------------------

  document.getElementById("search").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const input = document.getElementById("search-input");
    const v = input.value.trim();
    if (!v) return;
    go(/^\d+$/.test(v) ? hashLink("/blocks/" + v) : hashLink("/tx/" + encodeURIComponent(v)));
    input.value = "";
  });

  window.addEventListener("hashchange", route);
  refreshStatus();
  setInterval(refreshStatus, 5000);
  route();
})();
