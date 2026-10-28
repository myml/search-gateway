(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const view = $("view");

  /* ---------------- constants ---------------- */
  const ICON = (name, cls = "icon") => `<svg class="${cls}" aria-hidden="true"><use href="#i-${name}"/></svg>`;

  const PAGES = {
    overview: { title: "概览", sub: "网关运行状态、渠道健康与最近调用。", icon: "overview" },
    "search-channels": { title: "搜索渠道", sub: "上游搜索提供商。测试会真实调用上游并写入调用日志。", icon: "search" },
    "search-routes": { title: "搜索路由", sub: "把搜索渠道或组按顺序回退、并发聚合或负载均衡。", icon: "route" },
    "fetch-channels": { title: "抓取渠道", sub: "网页抓取方式。测试会真实请求 URL 并写入调用日志。", icon: "fetch" },
    "fetch-routes": { title: "抓取路由", sub: "把抓取渠道或组按顺序回退、并发聚合或负载均衡。", icon: "route" },
    keys: { title: "API Key", sub: "给 DSH / curl 使用的凭证；每个 Key 同时绑定搜索路由与抓取路由。", icon: "key" },
    logs: { title: "调用日志", sub: "网关调用、渠道测试与鉴权失败都记录在 SQLite 里。", icon: "logs" },
  };

  const NAV = [
    { label: "", items: ["overview"] },
    { label: "搜索", items: ["search-channels", "search-routes"] },
    { label: "抓取", items: ["fetch-channels", "fetch-routes"] },
    { label: "接入", items: ["keys", "logs"] },
  ];

  const MODE_LABEL = { fallback: "回退", merge: "聚合", balance: "均衡" };
  const MODE_HINT = {
    fallback: "按顺序逐个尝试，失败或空结果时自动回退到下一个目标。",
    merge: "并发调用全部目标并聚合去重，适合需要覆盖面的搜索。",
    balance: "按策略从目标中挑一个调用，适合平摊配额。",
  };
  const PROTOCOL_LABEL = {
    search: "搜索", fetch: "抓取", mcp: "MCP", messages: "Anthropic",
    "admin-test": "渠道测试", "key-test": "Key 测试", auth: "鉴权",
  };

  const TEST_DEFAULT = { search: "deepseek", fetch: "https://ipinfo.io/" };

  const CHANNEL_DEFAULTS = {
    tavily: "https://api.tavily.com/",
    brave: "https://api.search.brave.com/",
    exa: "https://api.exa.ai/",
    serper: "https://google.serper.dev/",
    bocha: "https://api.bochaai.com/",
    deepseek: "https://api.deepseek.com/",
    searxng: "",
    direct: "",
    jina: "https://r.jina.ai/",
  };
  const CHANNEL_HINTS = {
    tavily: "默认 https://api.tavily.com/，填 API Key 即可。",
    brave: "默认 https://api.search.brave.com/，填 API Key 即可。",
    exa: "默认 https://api.exa.ai/，填 API Key 即可；被地区拦截时可配置代理。",
    serper: "默认 https://google.serper.dev/，填 API Key 即可。",
    bocha: "默认 https://api.bochaai.com/，填 API Key 即可。",
    deepseek: "默认 https://api.deepseek.com/，填 DeepSeek API Key 即可，搜索与抓取共用同一个 Key。",
    searxng: "必填，例如 http://127.0.0.1:8888；SearXNG 需在 settings.yml 打开 formats: [html, json]。",
    direct: "抓取直连，不需要 Base URL；会使用浏览器 UA 请求目标页面。",
    jina: "默认 https://r.jina.ai/，可选 API Key。",
  };
  const CHANNEL_ROLE_TYPES = {
    search: ["tavily", "brave", "exa", "serper", "bocha", "deepseek", "searxng"],
    fetch: ["direct", "jina", "deepseek"],
  };

  const PROVIDER_SITES = {
    tavily: "https://tavily.com/",
    brave: "https://brave.com/search/api/",
    exa: "https://exa.ai/",
    serper: "https://serper.dev/",
    bocha: "https://bochaai.com/",
    deepseek: "https://platform.deepseek.com/",
    searxng: "https://docs.searxng.org/",
    jina: "https://jina.ai/",
  };

  /* ---------------- state ---------------- */
  const state = {
    admin: localStorage.getItem("sg-admin") || "",
    authed: false,
    loginError: "",
    page: "overview",
    overview: null,
    channels: [],
    routes: [],
    keys: [],
    recentLogs: null,
    recentLoading: false,
    logs: {
      entries: [], total: 0, offset: 0, limit: 50,
      filter: { protocol: "", status: "", route_id: "", key_id: "", q: "" },
      auto: false, timer: null, loading: false,
    },
  };

  /* ---------------- formatting helpers ---------------- */
  const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const val = (fd, k) => String(fd.get(k) || "").trim();

  const fmtTime = (t) => {
    if (!t) return "";
    const d = new Date(t);
    return Number.isNaN(d.getTime()) ? String(t) : d.toLocaleString("zh-CN", { hour12: false });
  };

  const relTime = (t) => {
    if (!t) return "";
    const d = new Date(t);
    if (Number.isNaN(d.getTime())) return String(t);
    const diff = Date.now() - d.getTime();
    if (diff < 0) return fmtTime(t);
    if (diff < 60e3) return "刚刚";
    if (diff < 3600e3) return `${Math.floor(diff / 60e3)} 分钟前`;
    if (diff < 864e5) return `${Math.floor(diff / 3600e3)} 小时前`;
    if (diff < 7 * 864e5) return `${Math.floor(diff / 864e5)} 天前`;
    const opts = d.getFullYear() === new Date().getFullYear()
      ? { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }
      : { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false };
    return d.toLocaleString("zh-CN", opts);
  };

  const timeCell = (t) => (t
    ? `<time datetime="${esc(t)}" title="${esc(fmtTime(t))}">${esc(relTime(t))}</time>`
    : '<span class="muted">—</span>');

  const fmtDuration = (ms) => {
    const n = Number(ms) || 0;
    if (n <= 0) return "<1 ms";
    if (n < 1000) return `${n} ms`;
    return `${(n / 1000).toFixed(2)} s`;
  };

  const truncate = (s, n) => {
    const str = String(s ?? "");
    return str.length > n ? `${str.slice(0, n - 1)}…` : str;
  };

  const statusDot = (enabled, on = "启用", off = "停用") =>
    `<span class="status ${enabled ? "ok" : "warn"}"><span class="dot"></span>${enabled ? on : off}</span>`;

  const modeTag = (mode) =>
    `<span class="tag accent" title="${esc(MODE_LABEL[mode] ? `${mode}（${MODE_LABEL[mode]}）` : mode || "")}">${esc(MODE_LABEL[mode] || mode || "—")}</span>`;

  const protocolTag = (p) => {
    const cls = p === "auth" ? "err" : p === "key-test" ? "info" : p === "admin-test" ? "warn" : "";
    return `<span class="tag ${cls}">${esc(PROTOCOL_LABEL[p] || p || "—")}</span>`;
  };

  const httpTag = (status) =>
    `<span class="tag ${status >= 200 && status < 300 ? "ok" : "err"}">${esc(status)}</span>`;

  const emptyState = (icon, text, action) =>
    `<div class="empty">${ICON(icon)}<p>${esc(text)}</p>${action || ""}</div>`;

  const providerTag = (type) => {
    const site = PROVIDER_SITES[type];
    if (!site) return `<span class="tag mono">${esc(type)}</span>`;
    return `<a class="tag mono tag-link" href="${esc(site)}" target="_blank" rel="noopener noreferrer" title="打开 ${esc(type)} 官网">${esc(type)}${ICON("external", "icon ext")}</a>`;
  };

  const channelName = (id) => (state.channels.find((c) => c.id === id) || {}).name || id;
  const routeName = (id) => (state.routes.find((r) => r.id === id) || {}).name || id;

  const channelTestSummary = (channel) => {
    const tests = channel.tests || {};
    const roles = (channel.capabilities || []).filter((role) => tests[role]);
    if (!roles.length) return '<span class="status"><span class="dot"></span>未测试</span>';
    return `<div class="chips">${roles.map((role) => {
      const test = tests[role];
      const detail = test.ok
        ? (test.kind === "search" ? `${test.result_count || 0} 条结果` : `${test.title || "抓取成功"} · ${test.content_chars || 0} 字符`)
        : (test.error || "失败");
      return `<span class="tag ${test.ok ? "ok" : "err"}" title="${esc(detail)}">${role === "search" ? "搜" : "抓"} ${test.ok ? "正常" : "失败"}</span>`;
    }).join("")}</div>`;
  };

  const channelUsage = (channel, role) => {
    const stats = channel.stats || {};
    const requests = role === "search" ? (stats.search_requests || 0) : (stats.fetch_requests || 0);
    const errors = role === "search" ? (stats.search_errors || 0) : (stats.fetch_errors || 0);
    return `<div class="chips"><span>${requests} 次${errors ? " ·" : ""}</span>${errors ? `<span class="tag err">${errors} 错误</span>` : ""}</div>`;
  };

  /* -------- 渠道月度额度（channel.quota 可能缺失，按旧服务端降级） -------- */
  const quotaInfo = (channel) => {
    const quota = channel && channel.quota;
    return quota && typeof quota === "object" ? quota : null;
  };

  const quotaStats = (quota) => {
    const limit = Math.max(0, Number(quota.monthly_limit) || 0);
    const used = Math.max(0, Number(quota.used) || 0);
    const unlimited = quota.unlimited === true || limit === 0;
    const exhausted = !unlimited && (quota.exhausted === true || used >= limit);
    const remaining = Number.isFinite(Number(quota.remaining))
      ? Math.max(0, Number(quota.remaining))
      : Math.max(0, limit - used);
    const pct = unlimited ? 0 : Math.min(100, (used / limit) * 100);
    const level = unlimited ? "unlimited" : exhausted ? "err" : pct >= 80 ? "warn" : "";
    return { limit, used, unlimited, exhausted, remaining, pct, level };
  };

  const quotaCell = (channel) => {
    const quota = quotaInfo(channel);
    if (!quota) return '<span class="muted">—</span>';
    const q = quotaStats(quota);
    if (q.unlimited) {
      return `<div class="quota"><div class="quota-line"><span class="tag">不限</span><span class="quota-meta">本月 ${esc(q.used)} 次</span></div></div>`;
    }
    return `<div class="quota ${q.level}">
      <div class="quota-line"><span class="quota-count">${esc(q.used)} / ${esc(q.limit)} 次</span>${q.exhausted ? '<span class="tag err">额度用尽</span>' : ""}</div>
      <div class="quota-bar"><span style="width:${q.pct.toFixed(1)}%"></span></div>
      <div class="quota-meta">${q.exhausted ? "已自动跳过，次月恢复" : `剩余 ${esc(q.remaining)} 次`}</div>
    </div>`;
  };

  const quotaOverviewLine = (channel) => {
    const quota = quotaInfo(channel);
    if (!quota) return "";
    const q = quotaStats(quota);
    if (q.unlimited) {
      return `<div class="quota-mini" title="本月额度不限，已用 ${esc(q.used)} 次">不限 · 已用${esc(q.used)}</div>`;
    }
    const label = q.exhausted ? "额度用尽" : `${Math.round(q.pct)}% · 剩${q.remaining}`;
    const title = q.exhausted
      ? `本月额度用尽（${q.used} / ${q.limit} 次），已自动跳过`
      : `本月额度 ${q.used} / ${q.limit} 次，剩余 ${q.remaining} 次`;
    return `<div class="quota-mini ${q.level}" title="${esc(title)}">${esc(label)}</div>`;
  };

  const quotaUsageBlock = (quota) => {
    const q = quotaStats(quota);
    const resetText = quota.resets_at ? esc(fmtTime(quota.resets_at)) : "次月自动重置";
    return `<div class="quota ${q.level}">
      <div class="quota-line"><span class="quota-count">${esc(q.used)} / ${esc(q.limit)} 次</span>${q.exhausted ? '<span class="tag err">额度用尽</span>' : `<span class="quota-meta">剩余 ${esc(q.remaining)} 次</span>`}</div>
      <div class="quota-bar"><span style="width:${q.pct.toFixed(1)}%"></span></div>
      <div class="quota-meta">下次重置：${resetText}${quota.period ? ` · 周期 ${esc(quota.period)}` : ""}</div>
    </div>`;
  };

  const routeTargetChips = (route, role) => {
    const isSearch = role === "search";
    const order = route[isSearch ? "search_order" : "fetch_order"] || [];
    const fallback = [
      ...(route[isSearch ? "search_channels" : "fetch_channels"] || []),
      ...(route[isSearch ? "search_groups" : "fetch_groups"] || []),
    ];
    const refs = order.length ? order : fallback;
    if (!refs.length) return '<span class="muted">—</span>';
    return `<div class="chips">${refs.map((id, index) => {
      const channel = state.channels.find((c) => c.id === id);
      if (channel) return `<span class="chip" title="渠道"><span class="idx">${index + 1}</span>${esc(channel.name)}</span>`;
      const group = state.routes.find((r) => r.id === id);
      return `<span class="chip group" title="路由组"><span class="idx">${index + 1}</span>组 ${esc(group ? group.name : id)}</span>`;
    }).join("")}</div>`;
  };

  const pageHead = (pageId, actions) => {
    const page = PAGES[pageId];
    return `<header class="page-head">
      <div><h1>${esc(page.title)}</h1><p>${esc(page.sub)}</p></div>
      <div class="page-actions">${actions || ""}</div>
    </header>`;
  };

  const kv = (key, value, mono = false, wide = false) =>
    `<div class="kv${wide ? " wide" : ""}"><div class="k">${esc(key)}</div><div class="v${mono ? " mono" : ""}">${esc(value || "—")}</div></div>`;

  /* ---------------- toast ---------------- */
  let toastTimer = null;
  function toast(message, kind) {
    const el = $("toast");
    el.className = `toast ${kind === "err" ? "err" : "ok"}`;
    el.innerHTML = `${ICON(kind === "err" ? "alert" : "check")}<span></span>`;
    el.querySelector("span").textContent = message;
    requestAnimationFrame(() => el.classList.add("show"));
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove("show"), 2800);
  }

  /* ---------------- auth / api ---------------- */
  function setAuthed(value) {
    state.authed = value;
    document.body.classList.toggle("authed", value);
    $("logout").hidden = !value;
  }

  function setAdminState(ok, text) {
    const apply = (el) => {
      if (!el) return;
      el.className = `status ${ok ? "ok" : "err"}`;
      const label = el.querySelector(".label");
      if (label) label.textContent = text;
    };
    apply($("adminState"));
    apply($("mobileState"));
  }

  async function api(path, { method = "GET", body, auth = "admin" } = {}) {
    const headers = {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (auth === "admin" && state.admin) headers.Authorization = `Bearer ${state.admin}`;
    const started = performance.now();
    let resp;
    try {
      resp = await fetch(path, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
      });
    } catch {
      return {
        resp: { ok: false, status: 0 },
        data: { error: { message: "网络错误：无法连接到网关服务" } },
        ms: Math.round(performance.now() - started),
      };
    }
    const ms = Math.round(performance.now() - started);
    let data = null;
    try { data = await resp.json(); } catch { /* non-JSON response */ }
    if (auth === "admin" && resp.status === 401 && state.authed) {
      state.loginError = "Admin Token 无效或已失效，请重新登录（config.json 的 admin_tokens，不是 API Key）";
      setAuthed(false);
      setAdminState(false, "未登录");
      render();
    }
    return { resp, data, ms };
  }

  /* ---------------- overlay / modal ---------------- */
  const overlayStack = [];

  function trapFocus(container) {
    const selector = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
    const onKey = (event) => {
      if (event.key !== "Tab") return;
      const items = [...container.querySelectorAll(selector)].filter((el) => el.offsetParent !== null || el === document.activeElement);
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    container.addEventListener("keydown", onKey);
    return () => container.removeEventListener("keydown", onKey);
  }

  function closeTopOverlay(restore = true) {
    const entry = overlayStack.pop();
    if (!entry) return;
    if (entry.cleanup) entry.cleanup();
    entry.el.remove();
    if (!overlayStack.length) document.body.classList.remove("overlay-open");
    if (entry.onClose) entry.onClose();
    if (restore && entry.restoreFocus && typeof entry.restoreFocus.focus === "function" && document.contains(entry.restoreFocus)) {
      try { entry.restoreFocus.focus({ preventScroll: true }); } catch { /* ignore */ }
    }
  }

  function closeOverlay(restore = true) {
    while (overlayStack.length) closeTopOverlay(restore);
    document.body.classList.remove("overlay-open");
  }

  function openOverlay(html, { stack = false, onClose = null } = {}) {
    if (!stack) closeOverlay();
    const root = $("modalRoot");
    root.insertAdjacentHTML("beforeend", html);
    const overlay = root.lastElementChild;
    if (!overlay) return null;
    const entry = {
      el: overlay,
      onClose,
      cleanup: null,
      restoreFocus: document.activeElement instanceof HTMLElement ? document.activeElement : null,
    };
    overlayStack.push(entry);
    document.body.classList.add("overlay-open");
    const releaseTrap = trapFocus(overlay);
    const isTop = () => overlayStack[overlayStack.length - 1] === entry;
    const onKey = (event) => {
      if (event.key !== "Escape" || !isTop()) return;
      event.preventDefault();
      event.stopPropagation();
      closeTopOverlay();
    };
    const onBackdrop = (event) => {
      if (event.target === overlay) closeTopOverlay();
    };
    document.addEventListener("keydown", onKey, true);
    overlay.addEventListener("mousedown", onBackdrop);
    entry.cleanup = () => {
      document.removeEventListener("keydown", onKey, true);
      overlay.removeEventListener("mousedown", onBackdrop);
      releaseTrap();
    };
    setTimeout(() => {
      if (!overlay.isConnected) return;
      const target = overlay.querySelector("[autofocus]")
        || overlay.querySelector(".drawer")
        || overlay.querySelector(".modal-body input:not([type=hidden]):not([disabled]), .modal-body select, .modal-body textarea, .modal-body button")
        || overlay.querySelector("button");
      if (target) target.focus();
    }, 30);
    return overlay;
  }

  function openModal({ title, subtitle, body, submitLabel = "保存", onSubmit, wide = false, closeOnSuccess = true, danger = false }) {
    const overlay = openOverlay(`<div class="overlay">
      <div class="modal ${wide ? "wide" : ""}" role="dialog" aria-modal="true" aria-labelledby="modalTitle">
        <div class="modal-head">
          <div><h2 id="modalTitle">${esc(title)}</h2>${subtitle ? `<p class="card-sub">${esc(subtitle)}</p>` : ""}</div>
          <button type="button" class="icon-btn" id="modalClose" aria-label="关闭">${ICON("close")}</button>
        </div>
        <form id="modalForm">
          <div class="modal-body">${body}</div>
          <div class="modal-foot">
            <div class="form-error" id="modalError"></div>
            <div class="actions">
              <button type="button" class="btn btn-secondary" id="modalCancel">取消</button>
              <button type="submit" class="btn ${danger ? "btn-danger-solid" : "btn-primary"}" id="modalSubmit">${esc(submitLabel)}</button>
            </div>
          </div>
        </form>
      </div>
    </div>`);
    if (!overlay) return;

    $("modalClose").onclick = () => closeOverlay();
    $("modalCancel").onclick = () => closeOverlay();
    const form = $("modalForm");
    form.querySelectorAll("input, textarea").forEach((el) => { el.spellcheck = false; });
    form.onsubmit = async (event) => {
      event.preventDefault();
      const submit = $("modalSubmit");
      const error = $("modalError");
      if (submit) {
        submit.classList.add("is-busy");
        submit.disabled = true;
      }
      if (error) error.textContent = "";
      try {
        await onSubmit(new FormData(form), form);
        if (closeOnSuccess) closeOverlay();
      } catch (err) {
        if (error) error.textContent = err && err.message ? err.message : String(err);
      } finally {
        if (submit) {
          submit.classList.remove("is-busy");
          submit.disabled = false;
        }
      }
    };
    setTimeout(() => {
      const first = form.querySelector("input[name]:not([type=hidden]), select[name], textarea[name]");
      if (first) first.focus();
    }, 40);
  }

  function confirmDialog({ title, message, confirmLabel = "确认", cancelLabel = "取消", danger = false, stack = false }) {
    return new Promise((resolve) => {
      let finished = false;
      const settle = (value) => {
        if (finished) return false;
        finished = true;
        resolve(value);
        return true;
      };
      const overlay = openOverlay(`<div class="overlay">
        <div class="modal sm" role="dialog" aria-modal="true" aria-labelledby="confirmTitle">
          <div class="modal-head">
            <div><h2 id="confirmTitle">${esc(title)}</h2><p class="card-sub">${esc(message)}</p></div>
            <button type="button" class="icon-btn" id="confirmClose" aria-label="关闭">${ICON("close")}</button>
          </div>
          <div class="modal-foot plain">
            <div class="spacer"></div>
            <div class="actions">
              <button type="button" class="btn btn-secondary" id="confirmCancel">${esc(cancelLabel)}</button>
              <button type="button" class="btn ${danger ? "btn-danger-solid" : "btn-primary"}" id="confirmOK">${esc(confirmLabel)}</button>
            </div>
          </div>
        </div>
      </div>`, { stack, onClose: () => settle(false) });
      if (!overlay) {
        settle(false);
        return;
      }
      const finish = (value) => {
        if (!settle(value)) return;
        closeTopOverlay();
      };
      $("confirmCancel").onclick = () => finish(false);
      $("confirmClose").onclick = () => finish(false);
      $("confirmOK").onclick = () => finish(true);
      setTimeout(() => {
        const ok = $("confirmOK");
        if (ok) ok.focus();
      }, 30);
    });
  }

  /* ---------------- copy ---------------- */
  async function copyText(text, message = "已复制到剪贴板") {
    try {
      await navigator.clipboard.writeText(text);
      toast(message);
    } catch {
      toast("复制失败，请手动选择文本", "err");
    }
  }

  /* ---------------- data loading ---------------- */
  async function loadOverview() {
    const { resp, data } = await api("/admin/api/overview");
    if (!resp.ok) {
      setAdminState(false, resp.status === 401 ? "未登录" : "加载失败");
      state.loginError = resp.status === 401
        ? "Admin Token 无效，请检查 config.json 的 admin_tokens（不是搜索用的 API Key）"
        : resp.status === 0
          ? "无法连接网关服务，请确认进程还在运行"
          : `无法加载管理数据（HTTP ${resp.status}）`;
      return false;
    }
    state.overview = data;
    state.channels = data.channels || [];
    state.routes = data.routes || [];
    state.keys = data.keys || [];
    state.loginError = "";
    setAdminState(true, "已连接");
    return true;
  }

  async function loadRecentLogs() {
    if (state.recentLoading) return;
    state.recentLoading = true;
    try {
      const { resp, data } = await api("/admin/api/logs?limit=6&offset=0");
      state.recentLogs = resp.ok ? (data.entries || []) : [];
    } finally {
      state.recentLoading = false;
    }
    if (state.page === "overview" && state.authed) render();
  }

  /* ---------------- nav / login / render ---------------- */
  function renderNav() {
    $("nav").innerHTML = NAV.map((group) => `
      <div class="nav-group">
        ${group.label ? `<div class="nav-label">${esc(group.label)}</div>` : ""}
        ${group.items.map((id) => `
          <a class="nav-item ${state.page === id ? "active" : ""}" href="#/${id}" data-page="${id}" ${state.page === id ? 'aria-current="page"' : ""}>
            ${ICON(PAGES[id].icon)}<span>${esc(PAGES[id].title)}</span>
          </a>`).join("")}
      </div>`).join("");
  }

  function renderLogin() {
    setAuthed(false);
    closeOverlay(false);
    stopLogAuto();
    setAdminState(false, state.loginError ? "连接失败" : "未登录");
    view.innerHTML = `
      <div class="login-page">
        <div class="login-card">
          <div class="login-brand">
            <span class="logo">${ICON("search")}</span>
            <span class="brand-name">search-gateway</span>
            <span class="version">v0.2.0</span>
          </div>
          <h1>登录控制台</h1>
          <p class="hint">输入 <strong>Admin Token</strong> 进入管理台。它是 <span class="mono">config.json</span> 里的 <span class="mono">admin_tokens</span>，不是搜索用的 API Key。</p>
          <form class="login-form" id="loginForm">
            <label class="field">
              <span class="field-label">Admin Token</span>
              <input id="adminToken" type="password" placeholder="粘贴 admin_tokens" autocomplete="off" spellcheck="false" autofocus>
            </label>
            <div class="form-error" id="loginError">${esc(state.loginError || "")}</div>
            <button class="btn btn-primary w-full" type="submit" id="loginBtn">登录</button>
          </form>
          <div class="login-note">${ICON("info")}<span>API Key 以 <span class="mono">sg_</span> 开头，只用于调用搜索 / 抓取接口，不能登录控制台。</span></div>
          <div class="login-foot">
            <details class="plain">
              <summary>在服务器上怎么拿到 Admin Token？</summary>
              <pre>jq -r '.admin_tokens[0]' config.json</pre>
            </details>
          </div>
        </div>
      </div>`;
    const input = $("adminToken");
    input.value = state.admin;
    input.focus();
    $("loginForm").onsubmit = async (event) => {
      event.preventDefault();
      const token = input.value.trim();
      if (!token) {
        $("loginError").textContent = "请填写 Admin Token";
        input.focus();
        return;
      }
      const button = $("loginBtn");
      button.disabled = true;
      button.classList.add("is-busy");
      button.textContent = "验证中…";
      state.admin = token;
      const ok = await loadOverview();
      if (ok) {
        localStorage.setItem("sg-admin", token);
        state.loginError = "";
        setAuthed(true);
        render();
        return;
      }
      button.disabled = false;
      button.classList.remove("is-busy");
      button.textContent = "登录";
      renderLogin();
    };
  }

  function render() {
    document.body.classList.toggle("authed", state.authed);
    if (!state.authed) {
      renderLogin();
      return;
    }
    $("logout").hidden = false;
    renderNav();
    const page = PAGES[state.page] ? state.page : "overview";
    view.innerHTML = pages[page]();
    if (bind[page]) bind[page]();
    if (page === "overview" && state.recentLogs === null) loadRecentLogs();
  }

  /* ---------------- overview ---------------- */
  function overviewChannelsCard(channels) {
    const rows = channels.map((channel) => {
      const stats = channel.stats || {};
      const requests = (stats.search_requests || 0) + (stats.fetch_requests || 0);
      const errors = (stats.search_errors || 0) + (stats.fetch_errors || 0);
      const roles = `<div class="chips">${(channel.capabilities || []).map((role) =>
        `<span class="tag">${role === "search" ? "搜索" : "抓取"}</span>`).join("")}</div>`;
      return `<tr>
        <td data-label="渠道"><div class="cell-title">${esc(channel.name)}</div><div class="cell-sub">${providerTag(channel.type)}${channel.enabled ? "" : " · 已停用"}</div></td>
        <td data-label="角色">${roles || '<span class="muted">—</span>'}</td>
        <td data-label="最近测试">${channelTestSummary(channel)}</td>
        <td data-label="请求 / 错误"><div class="chips"><span>${requests}${errors ? " ·" : ""}</span>${errors ? `<span class="tag err">${errors} 错误</span>` : ""}</div>${quotaOverviewLine(channel)}</td>
        <td data-label="最近调用">${timeCell(stats.last_used_at)}</td>
      </tr>`;
    }).join("");
    return `<section class="card">
      <div class="card-head">
        <div><h2>渠道状态</h2><p class="card-sub">最近测试与累计调用</p></div>
        <div class="spacer"></div><a class="link" href="#/search-channels">管理</a>
      </div>
      <div class="table-wrap"><table class="table">
        <thead><tr><th>渠道</th><th>角色</th><th>最近测试</th><th>请求 / 错误</th><th>最近调用</th></tr></thead>
        <tbody>${rows || `<tr><td colspan="5" data-label="">${emptyState("search", "还没有渠道")}</td></tr>`}</tbody>
      </table></div>
    </section>`;
  }

  function overviewRecentCard() {
    const logs = state.recentLogs;
    const items = (logs || []).map((entry) => {
      const target = entry.query || entry.url || entry.error || "—";
      return `<div class="recent-item" data-recent="${esc(entry.id)}" role="button" tabindex="0">
        ${httpTag(entry.status)}
        <div class="recent-main">
          <div class="recent-query">${esc(target)}</div>
          <div class="recent-meta">${protocolTag(entry.protocol)}<span>${esc(entry.route_name || "—")}</span><span>${fmtDuration(entry.duration_ms)}</span><span>${timeCell(entry.time)}</span></div>
        </div>
      </div>`;
    }).join("");
    const body = logs === null
      ? `<div class="card-body">${emptyState("logs", "正在加载…")}</div>`
      : items
        ? `<div class="recent-list">${items}</div>`
        : emptyState("logs", "还没有调用记录");
    return `<section class="card">
      <div class="card-head">
        <div><h2>最近调用</h2><p class="card-sub">最新 6 条记录，点击查看详情</p></div>
        <div class="spacer"></div><a class="link" href="#/logs">查看全部</a>
      </div>
      ${body}
    </section>`;
  }

  function overviewRoutesCard(routes) {
    const rows = routes.map((route) => {
      const targets = (route.search_order || route.fetch_order || []).length;
      return `<tr>
        <td data-label="名称"><div class="cell-title">${esc(route.name)}</div></td>
        <td data-label="角色"><span class="tag">${route.role === "search" ? "搜索" : "抓取"}</span></td>
        <td data-label="模式">${modeTag(route.mode)}</td>
        <td data-label="目标">${targets} 个</td>
        <td data-label="状态">${statusDot(route.enabled)}</td>
      </tr>`;
    }).join("");
    return `<section class="card">
      <div class="card-head">
        <div><h2>路由</h2><p class="card-sub">搜索 / 抓取路由的执行方式</p></div>
        <div class="spacer"></div><a class="link" href="#/search-routes">管理</a>
      </div>
      <div class="table-wrap"><table class="table">
        <thead><tr><th>名称</th><th>角色</th><th>模式</th><th>目标</th><th>状态</th></tr></thead>
        <tbody>${rows || `<tr><td colspan="5" data-label="">${emptyState("route", "还没有路由")}</td></tr>`}</tbody>
      </table></div>
    </section>`;
  }

  function overviewKeysCard(keys) {
    const rows = keys.map((key) => {
      const routes = [key.search_route_name, key.fetch_route_name].filter(Boolean).join(" · ") || "—";
      return `<tr>
        <td data-label="名称"><div class="cell-title">${esc(key.name)}</div></td>
        <td data-label="前缀" class="mono">${esc(key.prefix)}</td>
        <td data-label="路由">${esc(routes)}</td>
        <td data-label="请求" class="col-num">${key.stats ? key.stats.requests : 0}</td>
        <td data-label="状态">${statusDot(key.enabled)}</td>
      </tr>`;
    }).join("");
    return `<section class="card">
      <div class="card-head">
        <div><h2>API Key</h2><p class="card-sub">客户端凭证与用量</p></div>
        <div class="spacer"></div><a class="link" href="#/keys">管理</a>
      </div>
      <div class="table-wrap"><table class="table">
        <thead><tr><th>名称</th><th>前缀</th><th>路由</th><th class="col-num">请求</th><th>状态</th></tr></thead>
        <tbody>${rows || `<tr><td colspan="5" data-label="">${emptyState("key", "还没有 API Key")}</td></tr>`}</tbody>
      </table></div>
    </section>`;
  }

  function overviewPage() {
    const actions = `<button class="btn btn-secondary" id="refreshOverview">${ICON("refresh")}刷新</button>`;
    const overview = state.overview;
    if (!overview) {
      return pageHead("overview", actions) + `<div class="card"><div class="card-body">${emptyState("overview", "正在加载概览…")}</div></div>`;
    }
    const channels = overview.channels || [];
    const routes = overview.routes || [];
    const keys = overview.keys || [];
    const searchChannels = channels.filter((c) => (c.capabilities || []).includes("search")).length;
    const fetchChannels = channels.filter((c) => (c.capabilities || []).includes("fetch")).length;
    const searchRoutes = routes.filter((r) => r.role === "search").length;
    const fetchRoutes = routes.filter((r) => r.role === "fetch").length;
    const enabledKeys = keys.filter((k) => k.enabled).length;
    return pageHead("overview", actions) + `
      <div class="stat-grid">
        <div class="stat">${ICON("search", "icon stat-icon")}<div class="stat-k">渠道</div><div class="stat-v">${channels.length}</div><div class="stat-s">搜索 ${searchChannels} · 抓取 ${fetchChannels}</div></div>
        <div class="stat">${ICON("route", "icon stat-icon")}<div class="stat-k">路由</div><div class="stat-v">${routes.length}</div><div class="stat-s">搜索 ${searchRoutes} · 抓取 ${fetchRoutes}</div></div>
        <div class="stat">${ICON("key", "icon stat-icon")}<div class="stat-k">API Key</div><div class="stat-v">${keys.length}</div><div class="stat-s">启用 ${enabledKeys} · 停用 ${keys.length - enabledKeys}</div></div>
        <div class="stat">${ICON("settings", "icon stat-icon")}<div class="stat-k">配置版本</div><div class="stat-v">${overview.revision}</div><div class="stat-s">store 快照 revision</div></div>
      </div>
      <div class="grid grid-2 mt-16">
        ${overviewChannelsCard(channels)}
        ${overviewRecentCard()}
        ${overviewRoutesCard(routes)}
        ${overviewKeysCard(keys)}
      </div>`;
  }

  /* ---------------- channels ---------------- */
  function channelsPage(role) {
    const isSearch = role === "search";
    const pageId = isSearch ? "search-channels" : "fetch-channels";
    const title = isSearch ? "搜索渠道" : "抓取渠道";
    const channels = state.channels.filter((channel) => (channel.capabilities || []).includes(role));
    const rows = channels.map((channel) => `<tr>
      <td data-label="名称">
        <div class="cell-title">${esc(channel.name)}</div>
        ${channel.config && channel.config.api_key_set ? '<div class="cell-sub channel-sub">已配置 API Key</div>' : ""}
      </td>
      <td data-label="提供商">${providerTag(channel.type)}</td>
      <td data-label="状态">${statusDot(channel.enabled)}</td>
      <td data-label="最近测试">${channelTestSummary(channel)}</td>
      <td data-label="用量">${channelUsage(channel, role)}</td>
      <td data-label="本月额度">${quotaCell(channel)}</td>
      <td data-label="更新时间">${timeCell(channel.updated_at)}</td>
      <td data-label="" class="cell-actions"><div class="row-actions">
        <button class="btn btn-ghost btn-sm" data-test="${esc(channel.id)}">${ICON("play")}测试</button>
        <button class="icon-btn sm" data-edit="${esc(channel.id)}" title="编辑" aria-label="编辑">${ICON("edit")}</button>
        <button class="icon-btn sm" data-toggle="${esc(channel.id)}" title="${channel.enabled ? "停用" : "启用"}" aria-label="${channel.enabled ? "停用" : "启用"}">${ICON("power")}</button>
        <button class="icon-btn sm danger" data-del="${esc(channel.id)}" title="删除" aria-label="删除">${ICON("trash")}</button>
      </div></td>
    </tr>`).join("");
    return pageHead(pageId, `
        <span class="tag">${channels.length} 个</span>
        <button class="btn btn-secondary" data-test-all>${ICON("play")}测试全部</button>
        <button class="btn btn-primary" data-new-channel>${ICON("plus")}新建${isSearch ? "搜索" : "抓取"}渠道</button>`) + `
      <section class="card">
        <div class="table-wrap"><table class="table">
          <thead><tr><th>名称</th><th>提供商</th><th>状态</th><th>最近测试</th><th>用量</th><th>本月额度</th><th>更新时间</th><th></th></tr></thead>
          <tbody>${rows || `<tr><td colspan="8" data-label="">
            ${emptyState(isSearch ? "search" : "fetch", `还没有${title}`, `<button class="btn btn-primary btn-sm" data-new-channel>${ICON("plus")}新建${title}</button>`)}
          </td></tr>`}</tbody>
        </table></div>
      </section>`;
  }

  /* ---------------- routes ---------------- */
  function routesPage(role) {
    const isSearch = role === "search";
    const pageId = isSearch ? "search-routes" : "fetch-routes";
    const title = isSearch ? "搜索路由" : "抓取路由";
    const routes = state.routes.filter((route) => route.role === role);
    const rows = routes.map((route) => `<tr>
      <td data-label="名称">
        <div class="cell-title">${esc(route.name)}</div>
        ${route.description ? `<div class="cell-sub">${esc(route.description)}</div>` : ""}
      </td>
      <td data-label="模式">
        ${modeTag(route.mode)}
        ${route.mode === "balance" ? `<div class="cell-sub">${esc(route.balance || "round_robin")}</div>` : ""}
        ${route.max_retries ? `<div class="cell-sub">最多重试 ${route.max_retries} 次</div>` : ""}
      </td>
      <td data-label="目标">${routeTargetChips(route, role)}</td>
      <td data-label="空结果回退"><span class="tag ${route.fallback_on_empty ? "ok" : ""}">${route.fallback_on_empty ? "继续回退" : "直接返回"}</span></td>
      <td data-label="状态">${statusDot(route.enabled)}</td>
      <td data-label="" class="cell-actions"><div class="row-actions">
        <button class="icon-btn sm" data-edit="${esc(route.id)}" title="编辑" aria-label="编辑">${ICON("edit")}</button>
        <button class="icon-btn sm" data-toggle="${esc(route.id)}" title="${route.enabled ? "停用" : "启用"}" aria-label="${route.enabled ? "停用" : "启用"}">${ICON("power")}</button>
        <button class="icon-btn sm danger" data-del="${esc(route.id)}" title="删除" aria-label="删除">${ICON("trash")}</button>
      </div></td>
    </tr>`).join("");
    return pageHead(pageId, `
        <span class="tag">${routes.length} 个</span>
        <button class="btn btn-primary" data-new-route>${ICON("plus")}新建${title}</button>`) + `
      <section class="card">
        <div class="table-wrap"><table class="table">
          <thead><tr><th>名称</th><th>模式</th><th>目标</th><th>空结果回退</th><th>状态</th><th></th></tr></thead>
          <tbody>${rows || `<tr><td colspan="6" data-label="">
            ${emptyState("route", `还没有${title}`, `<button class="btn btn-primary btn-sm" data-new-route>${ICON("plus")}新建${title}</button>`)}
          </td></tr>`}</tbody>
        </table></div>
      </section>`;
  }

  /* ---------------- keys ---------------- */
  function keysPage() {
    const origin = location.origin;
    const curlSearch = `curl -s "${origin}/search?q=deepseek&count=5" \\\n  -H "Authorization: Bearer <YOUR_API_KEY>"`;
    const curlFetch = `curl -s "${origin}/fetch?url=https%3A%2F%2Fexample.com" \\\n  -H "Authorization: Bearer <YOUR_API_KEY>"`;
    const mcp = `MCP：Streamable HTTP

URL: ${origin}/mcp
Header: Authorization: Bearer <YOUR_API_KEY>

工具：web_search / web_fetch`;
    const dsh = `DSH：设置 → 网页搜索

默认模式（Messages API）
  Base URL: ${origin}/v1
  API Key:  本页签发的 Key（不是 Admin Token）

新版 Web 搜索 API 模式
  搜索 / 抓取 Base URL: ${origin}/v1/tools
  API Key:  本页签发的 Key`;
    const rows = state.keys.map((key) => `<tr>
      <td data-label="名称"><div class="cell-title">${esc(key.name)}</div></td>
      <td data-label="前缀" class="mono">${esc(key.prefix)}</td>
      <td data-label="搜索路由">${esc(key.search_route_name || "—")}</td>
      <td data-label="抓取路由">${esc(key.fetch_route_name || "—")}</td>
      <td data-label="请求" class="col-num"><div class="chips"><span>${key.stats ? key.stats.requests : 0}${key.stats && key.stats.errors ? " ·" : ""}</span>${key.stats && key.stats.errors ? `<span class="tag err" title="${esc(key.stats.last_error || "")}">${key.stats.errors} 错误</span>` : ""}</div></td>
      <td data-label="最近调用">${timeCell(key.stats && key.stats.last_used_at)}</td>
      <td data-label="状态">${statusDot(key.enabled)}</td>
      <td data-label="" class="cell-actions"><div class="row-actions">
        <button class="btn btn-ghost btn-sm" data-test="${esc(key.id)}">${ICON("play")}测试</button>
        <button class="icon-btn sm" data-toggle="${esc(key.id)}" title="${key.enabled ? "停用" : "启用"}" aria-label="${key.enabled ? "停用" : "启用"}">${ICON("power")}</button>
        <button class="icon-btn sm danger" data-del="${esc(key.id)}" title="删除" aria-label="删除">${ICON("trash")}</button>
      </div></td>
    </tr>`).join("");
    return pageHead("keys", `
        <span class="tag">${state.keys.length} 个</span>
        <button class="btn btn-primary" id="newKey">${ICON("plus")}新建 Key</button>`) + `
      <section class="card">
        <div class="table-wrap"><table class="table">
          <thead><tr><th>名称</th><th>前缀</th><th>搜索路由</th><th>抓取路由</th><th class="col-num">请求</th><th>最近调用</th><th>状态</th><th></th></tr></thead>
          <tbody>${rows || `<tr><td colspan="8" data-label="">
            ${emptyState("key", "还没有 API Key", `<button class="btn btn-primary btn-sm" id="newKeyEmpty">${ICON("plus")}新建 Key</button>`)}
          </td></tr>`}</tbody>
        </table></div>
      </section>
      <details class="card tutorial-card mt-16">
        <summary>
          ${ICON("chevron", "icon chev")}
          <div><h2>接入教程</h2><p class="card-sub">curl / DSH / MCP 配置、响应头与常见错误</p></div>
          <span class="spacer"></span>
          <span class="tag">4 个示例</span>
        </summary>
        <div class="tutorial-body">
          <div class="grid">
            <div class="code-block">
              <div class="code-head"><h3>curl：搜索</h3><button class="icon-btn sm" data-copy-target="tutSearch" title="复制" aria-label="复制">${ICON("copy")}</button></div>
              <pre id="tutSearch">${esc(curlSearch)}</pre>
            </div>
            <div class="code-block">
              <div class="code-head"><h3>curl：抓取网页</h3><button class="icon-btn sm" data-copy-target="tutFetch" title="复制" aria-label="复制">${ICON("copy")}</button></div>
              <pre id="tutFetch">${esc(curlFetch)}</pre>
            </div>
            <div class="code-block">
              <div class="code-head"><h3>MCP 设置</h3><button class="icon-btn sm" data-copy-target="tutMCP" title="复制" aria-label="复制">${ICON("copy")}</button></div>
              <pre id="tutMCP">${esc(mcp)}</pre>
            </div>
            <div class="code-block">
              <div class="code-head"><h3>DSH 设置</h3><button class="icon-btn sm" data-copy-target="tutDSH" title="复制" aria-label="复制">${ICON("copy")}</button></div>
              <pre id="tutDSH">${esc(dsh)}</pre>
            </div>
          </div>
          <p class="hint">认证头支持 <span class="mono">Authorization: Bearer &lt;key&gt;</span> 或 <span class="mono">x-api-key</span>，MCP 也使用同一个头。响应头里的 <span class="mono">X-Search-Route</span> / <span class="mono">X-Search-Channels</span> 表示本次命中的路由与渠道，完整记录在「调用日志」。</p>
          <details class="plain mt-8">
            <summary>常见错误</summary>
            <ul class="error-list">
              <li><span class="mono">401 invalid api key</span>：Key 填错、已删除或已停用（确认用的是 API Key，不是 Admin Token）。</li>
              <li><span class="mono">401 route ... is disabled</span>：这个 Key 绑定的路由被停用了。</li>
              <li><span class="mono">503 route has no usable channel</span>：路由里没有可用的搜索 / 抓取渠道。</li>
              <li><span class="mono">502 all search channels failed</span>：上游渠道报错，去「搜索渠道」看最近测试或「调用日志」。</li>
            </ul>
          </details>
        </div>
      </details>`;
  }

  /* ---------------- logs ---------------- */
  function logsPage() {
    const filter = state.logs.filter;
    const selected = (value, want) => (value === want ? "selected" : "");
    const protocolOptions = `
      <option value="">全部协议</option>
      <optgroup label="网关调用">
        <option value="search" ${selected(filter.protocol, "search")}>搜索</option>
        <option value="fetch" ${selected(filter.protocol, "fetch")}>抓取</option>
        <option value="mcp" ${selected(filter.protocol, "mcp")}>MCP</option>
        <option value="messages" ${selected(filter.protocol, "messages")}>Anthropic</option>
      </optgroup>
      <optgroup label="测试 / 鉴权">
        <option value="admin-test" ${selected(filter.protocol, "admin-test")}>渠道测试</option>
        <option value="key-test" ${selected(filter.protocol, "key-test")}>Key 测试</option>
        <option value="auth" ${selected(filter.protocol, "auth")}>鉴权失败</option>
      </optgroup>`;
    const routeOptions = `<option value="">全部路由</option>` + state.routes.map((route) =>
      `<option value="${esc(route.id)}" ${selected(filter.route_id, route.id)}>${esc(route.name)}</option>`).join("");
    const keyOptions = `<option value="">全部 Key</option>` + state.keys.map((key) =>
      `<option value="${esc(key.id)}" ${selected(filter.key_id, key.id)}>${esc(key.name)}</option>`).join("");
    return pageHead("logs", `<button class="btn btn-danger" id="logClear">${ICON("trash")}清空日志</button>`) + `
      <section class="card">
        <div class="filter-bar">
          <div class="grow">${ICON("search")}<input id="logQ" placeholder="搜索 query / URL / 错误信息" value="${esc(filter.q)}" aria-label="关键字"></div>
          <select id="logProtocol" aria-label="协议">${protocolOptions}</select>
          <select id="logStatus" aria-label="状态">
            <option value="">全部状态</option>
            <option value="ok" ${selected(filter.status, "ok")}>成功（2xx）</option>
            <option value="error" ${selected(filter.status, "error")}>失败（非 2xx）</option>
            <option value="401" ${selected(filter.status, "401")}>401 未授权</option>
            <option value="502" ${selected(filter.status, "502")}>502 上游失败</option>
            <option value="503" ${selected(filter.status, "503")}>503 无可用渠道</option>
          </select>
          <select id="logRoute" aria-label="路由">${routeOptions}</select>
          <select id="logKey" aria-label="API Key">${keyOptions}</select>
          <button class="btn btn-secondary" id="logRefresh">查询</button>
          <label class="toggle">
            <input type="checkbox" id="logAuto" ${state.logs.auto ? "checked" : ""}>
            <span class="toggle-track"><span class="toggle-thumb"></span></span>自动刷新
          </label>
        </div>
        <div class="table-wrap"><table class="table">
          <thead><tr><th>时间</th><th>协议</th><th>Key</th><th>路由</th><th>渠道</th><th>状态</th><th class="col-num">耗时</th><th>Query / URL</th></tr></thead>
          <tbody id="logsBody"></tbody>
        </table></div>
        <div class="pager">
          <span class="hint" id="logsPage"></span>
          <span class="spacer"></span>
          <button class="btn btn-secondary btn-sm" id="logPrev">上一页</button>
          <button class="btn btn-secondary btn-sm" id="logNext">下一页</button>
        </div>
      </section>`;
  }

  const pages = {
    overview: overviewPage,
    "search-channels": () => channelsPage("search"),
    "fetch-channels": () => channelsPage("fetch"),
    "search-routes": () => routesPage("search"),
    "fetch-routes": () => routesPage("fetch"),
    keys: keysPage,
    logs: logsPage,
  };

  /* ---------------- bindings ---------------- */
  const bind = {
    overview() {
      $("refreshOverview").onclick = async () => {
        state.recentLogs = null;
        const ok = await loadOverview();
        if (!ok && state.authed) toast(state.loginError || "刷新失败", "err");
        render();
      };
      view.querySelectorAll("[data-recent]").forEach((row) => {
        const open = () => {
          const entry = (state.recentLogs || []).find((item) => item.id === row.dataset.recent);
          if (entry) openLogDetail(entry);
        };
        row.onclick = open;
        row.onkeydown = (event) => {
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            open();
          }
        };
      });
    },

    "search-channels": () => bindChannelsPage("search"),
    "fetch-channels": () => bindChannelsPage("fetch"),
    "search-routes": () => bindRoutesPage("search"),
    "fetch-routes": () => bindRoutesPage("fetch"),

    async keys() {
      const openNew = () => openKeyModal();
      const newKey = $("newKey");
      if (newKey) newKey.onclick = openNew;
      const newKeyEmpty = $("newKeyEmpty");
      if (newKeyEmpty) newKeyEmpty.onclick = openNew;

      view.querySelectorAll("[data-copy-target]").forEach((button) => {
        button.onclick = () => {
          const target = $(button.dataset.copyTarget);
          if (target) copyText(target.textContent);
        };
      });
      view.querySelectorAll("[data-test]").forEach((button) => {
        button.onclick = () => {
          const key = state.keys.find((item) => item.id === button.dataset.test);
          if (key) openKeyTestModal(key);
        };
      });
      view.querySelectorAll("[data-toggle]").forEach((button) => {
        button.onclick = async () => {
          const key = state.keys.find((item) => item.id === button.dataset.toggle);
          if (!key) return;
          button.disabled = true;
          const { resp, data } = await api(`/admin/api/keys/${key.id}`, { method: "PATCH", body: { enabled: !key.enabled } });
          button.disabled = false;
          if (!resp.ok) return toast(data?.error?.message || "操作失败", "err");
          toast(key.enabled ? "Key 已停用" : "Key 已启用");
          await loadOverview();
          render();
        };
      });
      view.querySelectorAll("[data-del]").forEach((button) => {
        button.onclick = async () => {
          const key = state.keys.find((item) => item.id === button.dataset.del);
          if (!key) return;
          const confirmed = await confirmDialog({
            title: `删除 Key「${key.name}」？`,
            message: "删除后该 Key 立即失效，且无法恢复。已产生的调用日志会保留。",
            confirmLabel: "删除",
            danger: true,
          });
          if (!confirmed) return;
          const { resp, data } = await api(`/admin/api/keys/${key.id}`, { method: "DELETE" });
          if (!resp.ok) return toast(data?.error?.message || "删除失败", "err");
          toast("已删除");
          await loadOverview();
          render();
        };
      });
    },

    logs() {
      const readFilters = () => {
        state.logs.filter = {
          protocol: $("logProtocol").value,
          status: $("logStatus").value,
          route_id: $("logRoute").value,
          key_id: $("logKey").value,
          q: $("logQ").value.trim(),
        };
        state.logs.offset = 0;
        loadLogs();
      };
      $("logProtocol").onchange = readFilters;
      $("logStatus").onchange = readFilters;
      $("logRoute").onchange = readFilters;
      $("logKey").onchange = readFilters;
      $("logQ").onkeydown = (event) => { if (event.key === "Enter") readFilters(); };
      $("logRefresh").onclick = () => { state.logs.offset = 0; loadLogs(); };
      $("logAuto").onchange = () => {
        state.logs.auto = $("logAuto").checked;
        if (state.logs.auto) startLogAuto();
        else stopLogAuto();
      };
      $("logPrev").onclick = () => {
        state.logs.offset = Math.max(0, state.logs.offset - state.logs.limit);
        loadLogs();
      };
      $("logNext").onclick = () => {
        if (state.logs.offset + state.logs.limit >= state.logs.total) return;
        state.logs.offset += state.logs.limit;
        loadLogs();
      };
      $("logClear").onclick = async () => {
        const confirmed = await confirmDialog({
          title: "清空全部调用日志？",
          message: "会删除 SQLite 中的所有调用记录（渠道用量统计不受影响），此操作不可撤销。",
          confirmLabel: "清空",
          danger: true,
        });
        if (!confirmed) return;
        const { resp, data } = await api("/admin/api/logs/clear", { method: "POST" });
        if (!resp.ok) return toast(data?.error?.message || "清空失败", "err");
        toast("日志已清空");
        state.logs.offset = 0;
        loadLogs();
      };
      if (state.logs.auto) startLogAuto();
      loadLogs();
    },
  };

  /* ---------------- channel page bindings ---------------- */
  function bindChannelsPage(role) {
    view.querySelectorAll("[data-new-channel]").forEach((button) => {
      button.onclick = () => openChannelModal(null, role);
    });
    view.querySelectorAll("[data-test-all]").forEach((button) => {
      button.onclick = () => openTestAllModal(role);
    });
    view.querySelectorAll("[data-test]").forEach((button) => {
      button.onclick = () => {
        const channel = state.channels.find((item) => item.id === button.dataset.test);
        if (channel) openChannelTestModal(channel, role);
      };
    });
    view.querySelectorAll("[data-edit]").forEach((button) => {
      button.onclick = () => {
        const channel = state.channels.find((item) => item.id === button.dataset.edit);
        if (channel) openChannelModal(channel, role);
      };
    });
    view.querySelectorAll("[data-toggle]").forEach((button) => {
      button.onclick = async () => {
        const channel = state.channels.find((item) => item.id === button.dataset.toggle);
        if (!channel) return;
        button.disabled = true;
        const payload = channelPayload(channel);
        payload.enabled = !channel.enabled;
        const { resp, data } = await api(`/admin/api/channels/${channel.id}`, { method: "PATCH", body: payload });
        button.disabled = false;
        if (!resp.ok) return toast(data?.error?.message || "操作失败", "err");
        toast(channel.enabled ? "渠道已停用" : "渠道已启用");
        await loadOverview();
        render();
      };
    });
    view.querySelectorAll("[data-del]").forEach((button) => {
      button.onclick = async () => {
        const channel = state.channels.find((item) => item.id === button.dataset.del);
        if (!channel) return;
        const confirmed = await confirmDialog({
          title: `删除渠道「${channel.name}」？`,
          message: "路由中对该渠道的引用会一并移除，此操作不可撤销。",
          confirmLabel: "删除",
          danger: true,
        });
        if (!confirmed) return;
        const { resp, data } = await api(`/admin/api/channels/${channel.id}`, { method: "DELETE" });
        if (!resp.ok) return toast(data?.error?.message || "删除失败", "err");
        toast("渠道已删除");
        await loadOverview();
        render();
      };
    });
  }

  function channelPayload(channel, overrides = {}) {
    const cfg = channel.config || {};
    return {
      name: channel.name,
      type: channel.type,
      enabled: channel.enabled,
      monthly_quota: channel.quota?.monthly_limit || 0,
      config: {
        base_url: cfg.base_url,
        proxy: cfg.proxy,
        timeout_seconds: cfg.timeout_seconds,
        headers: cfg.headers,
        user_agent: cfg.user_agent,
      },
      ...overrides,
    };
  }

  /* ---------------- route page bindings ---------------- */
  function bindRoutesPage(role) {
    view.querySelectorAll("[data-new-route]").forEach((button) => {
      button.onclick = () => openRouteModal(null, role);
    });
    view.querySelectorAll("[data-edit]").forEach((button) => {
      button.onclick = () => {
        const route = state.routes.find((item) => item.id === button.dataset.edit);
        if (route) openRouteModal(route, role);
      };
    });
    view.querySelectorAll("[data-toggle]").forEach((button) => {
      button.onclick = async () => {
        const route = state.routes.find((item) => item.id === button.dataset.toggle);
        if (!route) return;
        button.disabled = true;
        const payload = routePayload(route);
        payload.enabled = !route.enabled;
        const { resp, data } = await api(`/admin/api/routes/${route.id}`, { method: "PATCH", body: payload });
        button.disabled = false;
        if (!resp.ok) return toast(data?.error?.message || "操作失败", "err");
        toast(route.enabled ? "路由已停用" : "路由已启用");
        await loadOverview();
        render();
      };
    });
    view.querySelectorAll("[data-del]").forEach((button) => {
      button.onclick = async () => {
        const route = state.routes.find((item) => item.id === button.dataset.del);
        if (!route) return;
        const confirmed = await confirmDialog({
          title: `删除路由「${route.name}」？`,
          message: "绑定到这条路由的 API Key 将失去对应的搜索或抓取能力。引用它的组也会同步移除。",
          confirmLabel: "删除",
          danger: true,
        });
        if (!confirmed) return;
        const { resp, data } = await api(`/admin/api/routes/${route.id}`, { method: "DELETE" });
        if (!resp.ok) return toast(data?.error?.message || "删除失败", "err");
        toast("路由已删除");
        await loadOverview();
        render();
      };
    });
  }

  function routePayload(route, overrides = {}) {
    return {
      name: route.name,
      description: route.description,
      role: route.role,
      mode: route.mode,
      balance: route.balance || "",
      max_retries: route.max_retries || 0,
      fallback_on_empty: !!route.fallback_on_empty,
      search_channels: route.search_channels || [],
      fetch_channels: route.fetch_channels || [],
      search_groups: route.search_groups || [],
      fetch_groups: route.fetch_groups || [],
      search_order: route.search_order || [],
      fetch_order: route.fetch_order || [],
      enabled: route.enabled,
      ...overrides,
    };
  }

  /* ---------------- channel modal ---------------- */
  function openChannelModal(channel, role = "search") {
    const cfg = channel ? channel.config || {} : {};
    const types = [...CHANNEL_ROLE_TYPES[role]];
    const initialType = channel ? channel.type : (role === "fetch" ? "direct" : "tavily");
    const initialDefault = CHANNEL_DEFAULTS[initialType] || "";
    const initialBase = cfg.base_url || (channel ? "" : initialDefault);
    const headerJSON = cfg.headers ? JSON.stringify(cfg.headers, null, 2) : "";
    const quota = channel ? quotaInfo(channel) : null;
    const quotaStat = quota ? quotaStats(quota) : null;
    const quotaLimit = quotaStat && quotaStat.limit > 0 ? quotaStat.limit : 0;
    const body = `
      <div class="modal-section">
        <h3>基本信息</h3>
        <div class="form-grid">
          <label class="field">
            <span class="field-label">名称 <span class="req">*</span></span>
            <input name="name" required value="${esc(channel ? channel.name : "")}" placeholder="${role === "fetch" ? "例如：direct 抓取" : "例如：tavily 主力"}">
          </label>
          <div class="field">
            <span class="field-label">提供商 <a class="link" id="chSite" target="_blank" rel="noopener noreferrer" title="打开官网">官网 ↗</a></span>
            <select name="type" id="chType">${types.map((type) => `<option value="${type}" ${initialType === type ? "selected" : ""}>${type}</option>`).join("")}</select>
          </div>
          <label class="field full">
            <span class="field-label">Base URL</span>
            <input name="base_url" id="chBaseUrl" value="${esc(initialBase)}" placeholder="${esc(initialDefault)}">
            <span class="field-help" id="chTypeHint">${esc(CHANNEL_HINTS[initialType] || "")}</span>
          </label>
        </div>
      </div>
      <div class="modal-section">
        <h3>连接与鉴权</h3>
        <div class="form-grid">
          <label class="field">
            <span class="field-label">API Key</span>
            <input type="password" name="api_key" autocomplete="off" placeholder="${channel ? "留空保持不变" : "填写密钥"}">
            <span class="field-help">密钥只保存到服务端 store，不会回显。</span>
          </label>
          <label class="field">
            <span class="field-label">超时（秒）</span>
            <input type="number" min="0" name="timeout_seconds" value="${cfg.timeout_seconds || ""}" placeholder="使用全局默认">
          </label>
          <label class="field full">
            <span class="field-label">代理</span>
            <input name="proxy" placeholder="http://127.0.0.1:7890 或 socks5://…" value="${esc(cfg.proxy || "")}">
            <span class="field-help">上游被地区限制或 Cloudflare 拦截时填写，保存后重新测试。</span>
          </label>
          <label class="field full">
            <span class="field-label">Headers（JSON 对象，可留空）</span>
            <textarea name="headers" rows="3" spellcheck="false" placeholder='{"X-Custom": "value"}'>${esc(headerJSON)}</textarea>
          </label>
          <label class="field full">
            <span class="field-label">User-Agent</span>
            <input name="user_agent" value="${esc(cfg.user_agent || "")}" placeholder="留空使用默认 UA">
            <span class="field-help">direct 抓取默认使用浏览器 UA；搜索渠道可留空。</span>
          </label>
        </div>
      </div>
      <div class="modal-section">
        <h3>月度额度 <span class="hint">可选</span></h3>
        <div class="form-grid">
          <label class="field">
            <span class="field-label">月度额度（次）</span>
            <input type="number" min="0" step="1" inputmode="numeric" name="monthly_quota" value="${quotaLimit ? esc(quotaLimit) : ""}" placeholder="0 = 不限量">
            <span class="field-help">留空或填 0 表示不限量。达到上限后该渠道会自动从路由中跳过，下个自然月自动恢复。</span>
          </label>
          ${quotaLimit ? `<div class="field">
            <span class="field-label">本月用量</span>
            <div id="chQuotaUsage">${quotaUsageBlock(quota)}</div>
            <div class="quota-actions">
              <button type="button" class="btn btn-ghost btn-sm" id="chQuotaReset">${ICON("refresh")}重置本月用量</button>
            </div>
          </div>` : ""}
        </div>
      </div>`;
    openModal({
      title: channel ? `编辑渠道：${channel.name}` : `新建${role === "fetch" ? "抓取" : "搜索"}渠道`,
      subtitle: "保存后可在列表里直接测试。",
      body,
      submitLabel: channel ? "保存" : "创建",
      onSubmit: async (fd) => {
        let headers = null;
        const rawHeaders = val(fd, "headers");
        if (rawHeaders) {
          try { headers = JSON.parse(rawHeaders); } catch { throw new Error("Headers 不是合法 JSON"); }
        }
        const type = val(fd, "type");
        if (type === "searxng" && !val(fd, "base_url").trim()) {
          throw new Error("SearXNG 需要填写 Base URL，例如 http://127.0.0.1:8888");
        }
        const rawQuota = val(fd, "monthly_quota");
        const monthlyQuota = rawQuota === "" ? 0 : Number(rawQuota);
        if (!Number.isInteger(monthlyQuota) || monthlyQuota < 0) {
          throw new Error("月度额度需要填写 0 或正整数");
        }
        const payload = {
          name: val(fd, "name"),
          type,
          monthly_quota: monthlyQuota,
          config: {
            base_url: val(fd, "base_url"),
            proxy: val(fd, "proxy"),
            timeout_seconds: Number(val(fd, "timeout_seconds") || 0),
            headers,
            user_agent: val(fd, "user_agent"),
          },
        };
        const apiKey = val(fd, "api_key");
        if (apiKey) payload.config.api_key = apiKey;
        const { resp, data } = channel
          ? await api(`/admin/api/channels/${channel.id}`, { method: "PATCH", body: payload })
          : await api("/admin/api/channels", { method: "POST", body: payload });
        if (!resp.ok) throw new Error(data?.error?.message || "保存失败");
        toast(channel ? "渠道已保存" : "渠道已创建");
        await loadOverview();
        render();
      },
    });

    const typeEl = $("chType");
    const baseEl = $("chBaseUrl");
    const hintEl = $("chTypeHint");
    const siteEl = $("chSite");
    const syncSite = (type) => {
      if (!siteEl) return;
      const site = PROVIDER_SITES[type];
      siteEl.hidden = !site;
      if (site) {
        siteEl.href = site;
        siteEl.title = `打开 ${type} 官网`;
      }
    };
    syncSite(initialType);
    if (typeEl && baseEl && hintEl) {
      let lastDefault = initialDefault;
      typeEl.onchange = () => {
        const nextDefault = CHANNEL_DEFAULTS[typeEl.value] || "";
        const current = baseEl.value.trim();
        if (current === "" || current === lastDefault) baseEl.value = nextDefault;
        lastDefault = nextDefault;
        baseEl.placeholder = nextDefault;
        hintEl.textContent = CHANNEL_HINTS[typeEl.value] || "";
        syncSite(typeEl.value);
      };
    }

    const resetButton = $("chQuotaReset");
    if (resetButton) {
      resetButton.onclick = async () => {
        const confirmed = await confirmDialog({
          title: "重置本月用量？",
          message: `将清零「${channel.name}」本月${quota.period ? `（${quota.period}）` : ""}的调用计数，月度额度设置不变，此操作不可撤销。`,
          confirmLabel: "重置",
          danger: true,
          stack: true,
        });
        if (!confirmed) return;
        resetButton.disabled = true;
        resetButton.classList.add("is-busy");
        const { resp, data } = await api(`/admin/api/channels/${channel.id}/quota/reset`, { method: "POST" });
        resetButton.disabled = false;
        resetButton.classList.remove("is-busy");
        if (!resp.ok) return toast(data?.error?.message || "重置失败", "err");
        toast("本月用量已重置");
        await loadOverview();
        render();
        const fresh = state.channels.find((item) => item.id === channel.id) || channel;
        const nextQuota = (data && data.quota) || quotaInfo(fresh);
        const usage = $("chQuotaUsage");
        if (usage && nextQuota) usage.innerHTML = quotaUsageBlock(nextQuota);
      };
    }
  }

  /* ---------------- channel test ---------------- */
  function openTestAllModal(role = "search") {
    const isSearch = role === "search";
    const count = state.channels.filter((channel) => (channel.capabilities || []).includes(role)).length;
    if (!count) return toast(`还没有${isSearch ? "搜索" : "抓取"}渠道`, "err");
    const field = isSearch
      ? `<label class="field full"><span class="field-label">搜索 query</span><input name="query" value="${esc(TEST_DEFAULT.search)}"></label>`
      : `<label class="field full"><span class="field-label">抓取 URL</span><input name="url" value="${esc(TEST_DEFAULT.fetch)}"></label>`;
    openModal({
      title: `测试全部${isSearch ? "搜索" : "抓取"}渠道`,
      subtitle: `${count} 个渠道会并发测试，结果同时写入调用日志。`,
      body: `<div class="form-grid cols-1">${field}</div><p class="hint mt-12">最近测试状态会在列表中更新；耗时会真实反映上游响应。</p>`,
      submitLabel: "开始测试",
      closeOnSuccess: false,
      onSubmit: async (fd) => {
        const payload = { role };
        if (isSearch) payload.query = val(fd, "query");
        else payload.url = val(fd, "url");
        const { resp, data } = await api("/admin/api/channels/test", { method: "POST", body: payload });
        if (!resp.ok) throw new Error(data?.error?.message || "测试失败");
        await loadOverview();
        render();
        showChannelTestResults(data, role);
      },
    });
  }

  function showChannelTestResults(data, role) {
    const results = data.results || [];
    const rows = results.map((result) => {
      const check = result.check || {};
      const detail = check.ok
        ? (check.kind === "search"
          ? `${check.result_count || 0} 条结果${check.title ? `，首条：${check.title}` : ""}`
          : `${check.title || "(无标题)"} · ${check.content_chars || 0} 字符`)
        : (check.error || "失败");
      return `<tr>
        <td data-label="渠道"><div class="cell-title">${esc(result.name)}</div>${result.enabled === false ? '<div class="cell-sub">已停用</div>' : ""}</td>
        <td data-label="状态"><span class="tag ${check.ok ? "ok" : "err"}">${check.ok ? "正常" : "失败"}</span></td>
        <td data-label="耗时" class="col-num">${fmtDuration(check.latency_ms)}</td>
        <td data-label="结果 / 错误"><span class="hint">${esc(detail)}</span></td>
      </tr>`;
    }).join("");
    openOverlay(`<div class="overlay">
      <div class="modal wide" role="dialog" aria-modal="true" aria-labelledby="testResultsTitle">
        <div class="modal-head">
          <div><h2 id="testResultsTitle">${role === "search" ? "搜索" : "抓取"}渠道测试结果</h2><p class="card-sub">${data.ok_count || 0} 正常 / ${data.fail_count || 0} 失败 · ${esc(fmtTime(new Date().toISOString()))}</p></div>
          <button type="button" class="icon-btn" id="testResultsClose" aria-label="关闭">${ICON("close")}</button>
        </div>
        <div class="modal-body">
          <div class="table-wrap"><table class="table">
            <thead><tr><th>渠道</th><th>状态</th><th class="col-num">耗时</th><th>结果 / 错误</th></tr></thead>
            <tbody>${rows || `<tr><td colspan="4" data-label="">${emptyState("play", "没有测试结果")}</td></tr>`}</tbody>
          </table></div>
        </div>
        <div class="modal-foot"><span class="spacer"></span><button class="btn btn-primary" id="testResultsDone">关闭</button></div>
      </div>
    </div>`);
    $("testResultsClose").onclick = () => closeOverlay();
    $("testResultsDone").onclick = () => closeOverlay();
  }

  function openChannelTestModal(channel, role = "search") {
    if (!channel) return;
    const isSearch = role === "search";
    const field = isSearch
      ? `<label class="field full"><span class="field-label">搜索 query</span><input name="query" value="${esc(TEST_DEFAULT.search)}" placeholder="例如：deepseek harness" autofocus></label>`
      : `<label class="field full"><span class="field-label">抓取 URL</span><input name="url" value="${esc(TEST_DEFAULT.fetch)}" placeholder="例如：https://ipinfo.io/" autofocus></label>`;
    openModal({
      title: `测试渠道：${channel.name}`,
      subtitle: "结果会更新列表里的「最近测试」，并写入调用日志。",
      body: `<div class="form-grid cols-1">${field}</div><pre id="testResult" hidden></pre>`,
      submitLabel: "运行",
      closeOnSuccess: false,
      onSubmit: async (fd) => {
        const target = isSearch ? val(fd, "query") : val(fd, "url");
        if (!target) throw new Error(isSearch ? "请填写搜索 query" : "请填写抓取 URL");
        const payload = isSearch ? { query: target } : { url: target };
        const { resp, data } = await api(`/admin/api/channels/${channel.id}/test`, { method: "POST", body: payload });
        await loadOverview();
        render();
        const output = $("testResult");
        if (!output) return;
        output.hidden = false;
        if (!resp.ok) {
          output.textContent = `✗ 测试失败：${data?.error?.message || "未知错误"}`;
          return;
        }
        const check = data.check || {};
        const summary = `✓ ${check.kind || "test"} · ${fmtDuration(check.latency_ms)}`
          + (check.title ? ` · ${check.title}` : "")
          + (check.kind === "search" ? ` · ${check.result_count || 0} 条结果` : ` · ${check.content_chars || 0} 字符`);
        output.textContent = `${summary}\n\n${JSON.stringify(data.result, null, 2)}`;
      },
    });
  }

  /* ---------------- route modal ---------------- */
  function routeReaches(startID, targetID, seen = new Set()) {
    if (!startID || !targetID) return false;
    if (startID === targetID) return true;
    if (seen.has(startID)) return false;
    seen.add(startID);
    const start = state.routes.find((route) => route.id === startID);
    if (!start) return false;
    return [...(start.search_groups || []), ...(start.fetch_groups || [])].some((id) => routeReaches(id, targetID, seen));
  }

  function openRouteModal(route, role) {
    role = role || (route && route.role) || "search";
    const isSearch = role === "search";
    const label = isSearch ? "搜索" : "抓取";
    const selectedIDs = new Set((route && route[isSearch ? "search_order" : "fetch_order"]) || []);

    const allTargets = [
      ...state.channels.filter((channel) => (channel.capabilities || []).includes(role)).map((channel) => ({
        kind: "channel", id: channel.id, name: channel.name, detail: channel.type,
      })),
      ...state.routes.filter((item) => item.role === role && item.id !== (route && route.id))
        .filter((item) => !route || !routeReaches(item.id, route.id) || selectedIDs.has(item.id))
        .map((item) => ({ kind: "group", id: item.id, name: item.name, detail: item.mode })),
    ];

    let targets = [];
    if (route) {
      const refs = route[isSearch ? "search_order" : "fetch_order"] || [];
      if (refs.length) {
        targets = refs.map((id) => allTargets.find((target) => target.id === id)).filter(Boolean);
      } else {
        const channels = route[isSearch ? "search_channels" : "fetch_channels"] || [];
        const groups = route[isSearch ? "search_groups" : "fetch_groups"] || [];
        targets = [...channels, ...groups].map((id) => allTargets.find((target) => target.id === id)).filter(Boolean);
      }
    }

    const body = `
      <div class="modal-section">
        <h3>基本信息</h3>
        <div class="form-grid">
          <label class="field">
            <span class="field-label">名称 <span class="req">*</span></span>
            <input name="name" required value="${esc(route ? route.name : "")}" placeholder="例如：${isSearch ? "内部搜索" : "网页抓取"}">
          </label>
          <label class="field">
            <span class="field-label">模式</span>
            <select name="mode" id="routeMode">
              <option value="fallback" ${!route || route.mode === "fallback" ? "selected" : ""}>fallback（顺序回退）</option>
              <option value="merge" ${route && route.mode === "merge" ? "selected" : ""}>merge（并发聚合去重）</option>
              <option value="balance" ${route && route.mode === "balance" ? "selected" : ""}>balance（负载均衡）</option>
            </select>
            <span class="field-help" id="modeHint">${MODE_HINT[route ? route.mode : "fallback"] || MODE_HINT.fallback}</span>
          </label>
          <label class="field full">
            <span class="field-label">描述</span>
            <input name="description" value="${esc(route ? route.description : "")}" placeholder="可选，仅用于说明">
          </label>
        </div>
      </div>
      <div class="modal-section">
        <h3>执行策略</h3>
        <div class="form-grid">
          <label class="field" id="balanceField" ${route && route.mode === "balance" ? "" : "hidden"}>
            <span class="field-label">均衡策略</span>
            <select name="balance" id="routeBalance">
              <option value="round_robin" ${!route || route.balance !== "random" ? "selected" : ""}>round_robin（轮询）</option>
              <option value="random" ${route && route.balance === "random" ? "selected" : ""}>random（随机起点）</option>
            </select>
          </label>
          <label class="field" id="retryField" ${route && route.mode === "merge" ? "hidden" : ""}>
            <span class="field-label">最大重试次数</span>
            <select name="max_retries" id="routeRetries">
              <option value="0" ${!route || !route.max_retries ? "selected" : ""}>0（不限，逐个尝试）</option>
              <option value="1" ${route && route.max_retries === 1 ? "selected" : ""}>1 次</option>
              <option value="2" ${route && route.max_retries === 2 ? "selected" : ""}>2 次</option>
              <option value="3" ${route && route.max_retries === 3 ? "selected" : ""}>3 次</option>
              <option value="5" ${route && route.max_retries === 5 ? "selected" : ""}>5 次</option>
            </select>
            <span class="field-help">失败后最多再尝试 N 个目标；仅 fallback / balance 生效。</span>
          </label>
        </div>
        <label class="check-row mt-12">
          <input type="checkbox" name="fallback_on_empty" ${route && route.fallback_on_empty ? "checked" : ""}>
          <span>空结果时继续尝试下一个目标（fallback / balance 模式）</span>
        </label>
      </div>
      <div class="modal-section">
        <h3>${label}目标 <span class="tag" id="targetCount">0</span> <span class="hint">顺序就是执行顺序，渠道和组可以混排</span></h3>
        <div class="target-add">
          <select id="routeTargetSelect"></select>
          <button type="button" class="btn btn-secondary" id="addTargetBtn">${ICON("plus")}添加</button>
        </div>
        <div class="picker-order" id="routeTargets"></div>
      </div>`;

    openModal({
      title: route ? `编辑${label}路由：${route.name}` : `新建${label}路由`,
      subtitle: "渠道按顺序回退；组会按子路由自己的模式执行，可以继续嵌套。",
      body,
      submitLabel: route ? "保存" : "创建",
      onSubmit: async (fd) => {
        const channelRefs = targets.filter((target) => target.kind === "channel").map((target) => target.id);
        const groupRefs = targets.filter((target) => target.kind === "group").map((target) => target.id);
        const order = targets.map((target) => target.id);
        const payload = {
          name: val(fd, "name"),
          description: val(fd, "description"),
          role,
          mode: val(fd, "mode"),
          balance: val(fd, "balance"),
          max_retries: Number(val(fd, "max_retries") || 0),
          fallback_on_empty: fd.get("fallback_on_empty") !== null,
          search_channels: isSearch ? channelRefs : [],
          search_groups: isSearch ? groupRefs : [],
          search_order: isSearch ? order : [],
          fetch_channels: isSearch ? [] : channelRefs,
          fetch_groups: isSearch ? [] : groupRefs,
          fetch_order: isSearch ? [] : order,
        };
        const { resp, data } = route
          ? await api(`/admin/api/routes/${route.id}`, { method: "PATCH", body: payload })
          : await api("/admin/api/routes", { method: "POST", body: payload });
        if (!resp.ok) throw new Error(data?.error?.message || "保存失败");
        toast(route ? "路由已保存" : "路由已创建");
        await loadOverview();
        render();
      },
    });

    const modeEl = $("routeMode");
    const balanceField = $("balanceField");
    const retryField = $("retryField");
    const modeHint = $("modeHint");
    const syncMode = () => {
      if (balanceField) balanceField.hidden = modeEl.value !== "balance";
      if (retryField) retryField.hidden = modeEl.value === "merge";
      if (modeHint) modeHint.textContent = MODE_HINT[modeEl.value] || "";
    };
    if (modeEl) {
      modeEl.onchange = syncMode;
      syncMode();
    }

    const targetSelect = $("routeTargetSelect");
    const targetList = $("routeTargets");

    function renderSelect() {
      const remaining = allTargets.filter((target) => !targets.some((item) => item.id === target.id));
      if (!remaining.length) {
        targetSelect.innerHTML = `<option value="">没有更多可添加的目标</option>`;
        targetSelect.disabled = true;
        return;
      }
      targetSelect.disabled = false;
      const channelOptions = remaining.filter((target) => target.kind === "channel")
        .map((target) => `<option value="channel:${esc(target.id)}">${esc(target.name)}（${esc(target.detail)}）</option>`).join("");
      const groupOptions = remaining.filter((target) => target.kind === "group")
        .map((target) => `<option value="group:${esc(target.id)}">${esc(target.name)}（${esc(target.detail)}）</option>`).join("");
      targetSelect.innerHTML = `<option value="">选择要添加的${label}渠道或组…</option>`
        + (channelOptions ? `<optgroup label="渠道">${channelOptions}</optgroup>` : "")
        + (groupOptions ? `<optgroup label="${label}组">${groupOptions}</optgroup>` : "");
    }

    function renderTargets() {
      targetList.innerHTML = targets.length
        ? targets.map((target, index) => `<div class="order-row">
            <span class="idx">${index + 1}</span>
            <span class="tag ${target.kind === "group" ? "accent" : ""}">${target.kind === "group" ? "组" : "渠道"}</span>
            <span class="grow">${esc(target.name)}${target.detail && target.detail !== target.name ? ` <span class="hint">${esc(target.detail)}</span>` : ""}</span>
            <button type="button" class="btn btn-ghost btn-xs" data-move="${index}:-1" ${index === 0 ? "disabled" : ""} title="上移" aria-label="上移">↑</button>
            <button type="button" class="btn btn-ghost btn-xs" data-move="${index}:1" ${index === targets.length - 1 ? "disabled" : ""} title="下移" aria-label="下移">↓</button>
            <button type="button" class="icon-btn sm danger" data-remove="${index}" title="移除" aria-label="移除">${ICON("close")}</button>
          </div>`).join("")
        : `<div class="order-empty">还没有目标，从上面的下拉框添加一个渠道或组。</div>`;
      $("targetCount").textContent = targets.length;
      renderSelect();
    }

    $("addTargetBtn").onclick = () => {
      if (!targetSelect.value) return;
      const [kind, id] = targetSelect.value.split(":");
      const target = allTargets.find((item) => item.kind === kind && item.id === id);
      if (!target || targets.some((item) => item.id === id)) return;
      targets.push(target);
      renderTargets();
      targetSelect.value = "";
    };
    $("modalForm").addEventListener("click", (event) => {
      const move = event.target.closest("[data-move]");
      if (move) {
        const [indexRaw, deltaRaw] = move.dataset.move.split(":");
        const index = Number(indexRaw);
        const next = index + Number(deltaRaw);
        if (next < 0 || next >= targets.length) return;
        [targets[index], targets[next]] = [targets[next], targets[index]];
        renderTargets();
        return;
      }
      const remove = event.target.closest("[data-remove]");
      if (remove) {
        targets.splice(Number(remove.dataset.remove), 1);
        renderTargets();
      }
    });
    renderTargets();
  }

  /* ---------------- API key modals ---------------- */
  function openKeyModal() {
    const searchRoutes = state.routes.filter((route) => route.role === "search");
    const fetchRoutes = state.routes.filter((route) => route.role === "fetch");
    if (!searchRoutes.length && !fetchRoutes.length) return toast("先创建搜索路由或抓取路由", "err");
    const options = (routes, placeholder) => `<option value="">${placeholder}</option>` + routes.map((route) =>
      `<option value="${esc(route.id)}" ${routes[0].id === route.id ? "selected" : ""}>${esc(route.name)}（${esc(route.mode)}）</option>`).join("");
    openModal({
      title: "新建 API Key",
      subtitle: "创建后只明文展示一次，请立即复制保存。",
      body: `<div class="form-grid">
        <label class="field full"><span class="field-label">名称</span><input name="name" placeholder="例如：内部服务 / DSH" required></label>
        <label class="field"><span class="field-label">搜索路由</span><select name="search_route_id">${options(searchRoutes, "不使用搜索")}</select></label>
        <label class="field"><span class="field-label">抓取路由</span><select name="fetch_route_id">${options(fetchRoutes, "不使用抓取")}</select></label>
        <p class="field-help full">至少选择一条路由；DSH 通常两条都选。</p>
      </div>`,
      submitLabel: "创建",
      closeOnSuccess: false,
      onSubmit: async (fd) => {
        const searchRouteID = val(fd, "search_route_id");
        const fetchRouteID = val(fd, "fetch_route_id");
        if (!searchRouteID && !fetchRouteID) throw new Error("至少选择一条搜索路由或抓取路由");
        const { resp, data } = await api("/admin/api/keys", {
          method: "POST",
          body: { name: val(fd, "name"), search_route_id: searchRouteID, fetch_route_id: fetchRouteID },
        });
        if (!resp.ok) throw new Error(data?.error?.message || "创建失败");
        await loadOverview();
        render();
        showTokenModal(data.token, data.key.name);
      },
    });
  }

  function showTokenModal(token, name) {
    openOverlay(`<div class="overlay">
      <div class="modal sm" role="dialog" aria-modal="true" aria-labelledby="tokenTitle">
        <div class="modal-head">
          <div><h2 id="tokenTitle">API Key 已创建</h2><p class="card-sub">${esc(name)}</p></div>
          <button type="button" class="icon-btn" id="tokenClose" aria-label="关闭">${ICON("close")}</button>
        </div>
        <div class="modal-body">
          <p class="hint">这是唯一一次明文展示，关闭后无法找回。请现在复制并保存到安全的地方。</p>
          <pre id="tokenValue">${esc(token)}</pre>
        </div>
        <div class="modal-foot">
          <span class="spacer"></span>
          <button class="btn btn-secondary" id="copyToken">${ICON("copy")}复制</button>
          <button class="btn btn-primary" id="closeToken">完成</button>
        </div>
      </div>
    </div>`);
    $("tokenClose").onclick = () => closeOverlay();
    $("closeToken").onclick = () => closeOverlay();
    $("copyToken").onclick = () => copyText(token, "API Key 已复制");
  }

  function openKeyTestModal(key) {
    if (!key) return;
    const searchRoute = state.routes.find((route) => route.id === key.search_route_id);
    const fetchRoute = state.routes.find((route) => route.id === key.fetch_route_id);
    const subtitle = `前缀 ${key.prefix} · 搜索 ${key.search_route_name || "未绑定"}`
      + (searchRoute ? `（${searchRoute.mode}）` : "")
      + ` · 抓取 ${key.fetch_route_name || "未绑定"}`
      + (fetchRoute ? `（${fetchRoute.mode}）` : "")
      + (key.enabled ? "" : " · Key 已停用");
    openOverlay(`<div class="overlay">
      <div class="modal wide" role="dialog" aria-modal="true" aria-labelledby="keyTestTitle">
        <div class="modal-head">
          <div><h2 id="keyTestTitle">测试 API Key：${esc(key.name)}</h2><p class="card-sub">${esc(subtitle)}</p></div>
          <button type="button" class="icon-btn" id="ktClose" aria-label="关闭">${ICON("close")}</button>
        </div>
        <div class="modal-body">
          <div class="segmented" id="ktTabs" role="tablist">
            <button type="button" class="active" data-kt-tab="search" role="tab" aria-selected="true">搜索</button>
            <button type="button" data-kt-tab="fetch" role="tab" aria-selected="false">抓取</button>
          </div>
          <div id="ktPanelSearch">
            <div class="row mt-16">
              <input class="grow" id="ktQuery" placeholder="搜索词，例如 deepseek" value="${esc(TEST_DEFAULT.search)}">
              <select id="ktCount" class="select-sm" aria-label="结果数量">
                <option value="5">5 条</option>
                <option value="8" selected>8 条</option>
                <option value="10">10 条</option>
                <option value="20">20 条</option>
              </select>
              <button class="btn btn-primary" id="ktSearch" ${key.search_route_id ? "" : "disabled"}>${ICON("play")}测试搜索</button>
            </div>
            <div class="hint mt-8" id="ktStatus"></div>
            <div class="mt-12" id="ktResults"></div>
          </div>
          <div id="ktPanelFetch" hidden>
            <div class="row mt-16">
              <input class="grow" id="ktURL" placeholder="例如：https://ipinfo.io/" value="${esc(TEST_DEFAULT.fetch)}">
              <button class="btn btn-primary" id="ktFetch" ${key.fetch_route_id ? "" : "disabled"}>${ICON("play")}测试抓取</button>
            </div>
            <div class="hint mt-8" id="ktFetchStatus"></div>
            <div class="hint mt-8" id="ktFetchMeta"></div>
            <pre id="ktFetchContent" hidden></pre>
          </div>
          <p class="hint mt-16">测试会按这个 Key 绑定的路由真实调用渠道，并写入「调用日志」（协议 key-test）。</p>
        </div>
        <div class="modal-foot"><span class="spacer"></span><button class="btn btn-secondary" id="ktCloseBtn">关闭</button></div>
      </div>
    </div>`);
    $("ktClose").onclick = () => closeOverlay();
    $("ktCloseBtn").onclick = () => closeOverlay();
    $("ktTabs").querySelectorAll("[data-kt-tab]").forEach((tab) => {
      tab.onclick = () => {
        const name = tab.dataset.ktTab;
        $("ktTabs").querySelectorAll("[data-kt-tab]").forEach((item) => {
          const active = item === tab;
          item.classList.toggle("active", active);
          item.setAttribute("aria-selected", active ? "true" : "false");
        });
        $("ktPanelSearch").hidden = name !== "search";
        $("ktPanelFetch").hidden = name !== "fetch";
      };
    });
    $("ktSearch").onclick = () => runKeyTestSearch(key);
    $("ktFetch").onclick = () => runKeyTestFetch(key);
    $("ktQuery").onkeydown = (event) => { if (event.key === "Enter") runKeyTestSearch(key); };
    $("ktURL").onkeydown = (event) => { if (event.key === "Enter") runKeyTestFetch(key); };
    $("ktQuery").focus();
  }

  async function runKeyTestSearch(key) {
    const query = $("ktQuery").value.trim();
    if (!query) return $("ktQuery").focus();
    const button = $("ktSearch");
    const status = $("ktStatus");
    button.disabled = true;
    button.classList.add("is-busy");
    status.className = "hint mt-8";
    status.textContent = "测试中…";
    $("ktResults").replaceChildren();
    try {
      const { resp, data, ms } = await api(`/admin/api/keys/${key.id}/test`, {
        method: "POST",
        body: { query, count: Number($("ktCount").value) },
      });
      if (!resp.ok) {
        status.className = "form-error mt-8";
        status.textContent = `HTTP ${resp.status} · ${data?.error?.message || "测试失败"}`;
        return;
      }
      const route = data.route || {};
      status.className = "hint mt-8";
      status.textContent = `HTTP ${resp.status} · ${fmtDuration(ms)} · 路由 ${route.name || "-"}（${route.mode || "-"}） · 渠道 ${(route.channels || []).join(", ") || "-"}`;
      const items = data.results || [];
      $("ktResults").innerHTML = items.map((item) => {
        const href = safeURL(item.url);
        const title = href
          ? `<a href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(item.title || item.url)}</a>`
          : esc(item.title || item.url);
        return `<div class="result">${title}<div class="host">${esc(hostOf(item.url))}${item.date ? ` · ${esc(item.date)}` : ""}</div>${item.snippet ? `<p class="snippet">${esc(item.snippet)}</p>` : ""}</div>`;
      }).join("") || `<p class="hint">没有结果。</p>`;
      await loadOverview();
    } finally {
      button.disabled = false;
      button.classList.remove("is-busy");
    }
  }

  async function runKeyTestFetch(key) {
    const url = $("ktURL").value.trim();
    if (!url) return $("ktURL").focus();
    const button = $("ktFetch");
    const status = $("ktFetchStatus");
    button.disabled = true;
    button.classList.add("is-busy");
    status.className = "hint mt-8";
    status.textContent = "抓取中…";
    $("ktFetchContent").hidden = true;
    try {
      const { resp, data, ms } = await api(`/admin/api/keys/${key.id}/test`, {
        method: "POST",
        body: { url },
      });
      if (!resp.ok) {
        status.className = "form-error mt-8";
        status.textContent = `HTTP ${resp.status} · ${data?.error?.message || "测试失败"}`;
        return;
      }
      const route = data.route || {};
      const fetched = data.fetch || {};
      status.className = "hint mt-8";
      status.textContent = `HTTP ${resp.status} · ${fmtDuration(ms)} · 渠道 ${(route.channels || []).join(", ") || "-"}`;
      $("ktFetchMeta").textContent = `${fetched.title || "(无标题)"} · ${(fetched.content || "").length} 字符`;
      $("ktFetchContent").textContent = fetched.content || "";
      $("ktFetchContent").hidden = false;
      await loadOverview();
    } finally {
      button.disabled = false;
      button.classList.remove("is-busy");
    }
  }

  /* ---------------- log detail ---------------- */
  function openLogDetail(entry) {
    const shortRows = [
      ["时间", fmtTime(entry.time)],
      ["协议", PROTOCOL_LABEL[entry.protocol] || entry.protocol],
      ["状态", entry.status],
      ["耗时", fmtDuration(entry.duration_ms)],
      ["Key", entry.api_key_name || entry.api_key_prefix || "—"],
      ["路由", entry.route_name || "—"],
      ["模式", entry.mode || "—"],
      ["结果数", entry.result_count || 0],
    ];
    const wideRows = [
      ["渠道", (entry.channels || []).join(", ") || "—", true],
      ["Query", entry.query || "—", true],
      ["URL", entry.url || "—", true],
      ["错误", entry.error || "—", false],
      ["请求 ID", entry.request_id || "—", true],
      ["来源", [entry.remote_addr, entry.user_agent].filter(Boolean).join(" · ") || "—", false],
    ];
    openOverlay(`<div class="drawer-mask">
      <aside class="drawer" role="dialog" aria-modal="true" aria-labelledby="logDetailTitle" tabindex="-1">
        <div class="drawer-head">
          <div><h2 id="logDetailTitle">调用详情</h2><p class="card-sub">${esc(PROTOCOL_LABEL[entry.protocol] || entry.protocol)} · ${esc(fmtTime(entry.time))}</p></div>
          <button type="button" class="icon-btn" id="logDetailClose" aria-label="关闭">${ICON("close")}</button>
        </div>
        <div class="drawer-body">
          <div class="kv-grid">
            ${shortRows.map(([key, value]) => kv(key, value)).join("")}
            ${wideRows.map(([key, value, mono]) => kv(key, value, mono, true)).join("")}
          </div>
          <details class="plain mt-16">
            <summary>原始 JSON</summary>
            <pre class="mt-8">${esc(JSON.stringify(entry, null, 2))}</pre>
          </details>
        </div>
        <div class="drawer-foot">
          <span class="spacer"></span>
          <button class="btn btn-secondary" id="copyLogJSON">${ICON("copy")}复制 JSON</button>
          <button class="btn btn-primary" id="logDetailDone">关闭</button>
        </div>
      </aside>
    </div>`);
    $("logDetailClose").onclick = () => closeOverlay();
    $("logDetailDone").onclick = () => closeOverlay();
    $("copyLogJSON").onclick = () => copyText(JSON.stringify(entry, null, 2), "日志 JSON 已复制");
  }

  /* ---------------- logs ---------------- */
  async function loadLogs() {
    if (state.logs.loading) return;
    state.logs.loading = true;
    try {
      const filter = state.logs.filter;
      const params = new URLSearchParams();
      if (filter.protocol) params.set("protocol", filter.protocol);
      if (filter.status) params.set("status", filter.status);
      if (filter.route_id) params.set("route_id", filter.route_id);
      if (filter.key_id) params.set("key_id", filter.key_id);
      if (filter.q) params.set("q", filter.q);
      params.set("limit", state.logs.limit);
      params.set("offset", state.logs.offset);
      const { resp, data } = await api(`/admin/api/logs?${params.toString()}`);
      if (!resp.ok) {
        if (resp.status !== 401) toast(data?.error?.message || "日志加载失败", "err");
        return;
      }
      state.logs.entries = data.entries || [];
      state.logs.total = data.total || 0;
      renderLogRows();
    } finally {
      state.logs.loading = false;
    }
  }

  function renderLogRows() {
    const body = $("logsBody");
    if (!body) return;
    body.innerHTML = state.logs.entries.map((entry) => {
      const target = entry.query || entry.url || "";
      const channels = (entry.channels || []).length
        ? `<div class="chips">${(entry.channels || []).map((name) => `<span class="chip">${esc(name)}</span>`).join("")}</div>`
        : '<span class="muted">—</span>';
      return `<tr class="clickable" data-log="${esc(entry.id)}" tabindex="0" title="查看详情">
        <td data-label="时间">${timeCell(entry.time)}</td>
        <td data-label="协议">${protocolTag(entry.protocol)}</td>
        <td data-label="Key">${esc(entry.api_key_name || entry.api_key_prefix || "—")}</td>
        <td data-label="路由">${esc(entry.route_name || "—")}</td>
        <td data-label="渠道">${channels}</td>
        <td data-label="状态">${httpTag(entry.status)}</td>
        <td data-label="耗时" class="col-num">${fmtDuration(entry.duration_ms)}</td>
        <td data-label="Query / URL"><span class="truncate">${esc(target || entry.error || "—")}</span></td>
      </tr>`;
    }).join("") || `<tr><td colspan="8" data-label="">${emptyState("logs", "没有匹配的调用记录")}</td></tr>`;
    body.querySelectorAll("[data-log]").forEach((row) => {
      row.onclick = () => {
        const entry = state.logs.entries.find((item) => item.id === row.dataset.log);
        if (entry) openLogDetail(entry);
      };
      row.onkeydown = (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          row.click();
        }
      };
    });
    const total = state.logs.total;
    const from = total ? state.logs.offset + 1 : 0;
    const to = Math.min(state.logs.offset + state.logs.limit, total);
    $("logsPage").textContent = total ? `第 ${from}–${to} 条 · 共 ${total} 条` : "共 0 条";
    $("logPrev").disabled = state.logs.offset <= 0;
    $("logNext").disabled = state.logs.offset + state.logs.limit >= total;
  }

  function stopLogAuto() {
    if (state.logs.timer) {
      clearInterval(state.logs.timer);
      state.logs.timer = null;
    }
  }

  function startLogAuto() {
    stopLogAuto();
    state.logs.timer = setInterval(() => {
      if (state.page === "logs" && state.authed) loadLogs();
    }, 3000);
  }

  /* ---------------- utils ---------------- */
  function safeURL(raw) {
    try {
      const url = new URL(raw);
      return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
    } catch {
      return null;
    }
  }

  function hostOf(raw) {
    try { return new URL(raw).host; } catch { return ""; }
  }

  /* ---------------- theme / nav / init ---------------- */
  function effectiveTheme() {
    const root = document.documentElement.dataset.theme;
    if (root === "light" || root === "dark") return root;
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  function toggleTheme() {
    const next = effectiveTheme() === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("sg-theme", next);
  }

  function setNav(open) {
    document.body.classList.toggle("nav-open", open);
    $("scrim").hidden = !open;
    $("menuBtn").setAttribute("aria-expanded", open ? "true" : "false");
  }

  function applyHash() {
    const page = (location.hash || "#/overview").replace(/^#\/?/, "").split("?")[0];
    state.page = PAGES[page] ? page : "overview";
  }

  const savedTheme = localStorage.getItem("sg-theme");
  if (savedTheme === "light" || savedTheme === "dark") document.documentElement.dataset.theme = savedTheme;
  document.querySelectorAll("[data-theme-toggle]").forEach((button) => { button.onclick = toggleTheme; });

  $("menuBtn").onclick = () => setNav(!document.body.classList.contains("nav-open"));
  $("scrim").onclick = () => setNav(false);
  $("logout").onclick = () => {
    state.admin = "";
    localStorage.removeItem("sg-admin");
    state.overview = null;
    state.channels = [];
    state.routes = [];
    state.keys = [];
    state.recentLogs = null;
    state.loginError = "";
    state.logs = {
      entries: [], total: 0, offset: 0, limit: 50,
      filter: { protocol: "", status: "", route_id: "", key_id: "", q: "" },
      auto: false, timer: null, loading: false,
    };
    stopLogAuto();
    setAuthed(false);
    render();
  };

  window.addEventListener("hashchange", () => {
    stopLogAuto();
    setNav(false);
    applyHash();
    render();
  });

  window.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && document.body.classList.contains("nav-open")) setNav(false);
  });

  window.addEventListener("resize", () => {
    if (window.innerWidth > 900 && document.body.classList.contains("nav-open")) setNav(false);
  });

  applyHash();
  if (state.admin) {
    loadOverview().then((ok) => {
      setAuthed(ok);
      render();
    });
  } else {
    setAuthed(false);
    render();
  }
})();
