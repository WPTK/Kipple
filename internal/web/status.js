(function () {
  "use strict";
  var $ = function (id) { return document.getElementById(id); };
  var H = { "X-Kipple-Client": "web" };
  var es = null, refreshTimer = null, retryTimer = null, retryMs = 1000, lastBeat = 0, beatTimer = null;
  var titles = {}, runs = {};

  function api(method, path, body, raw) {
    var opt = { method: method, headers: Object.assign({}, H), credentials: "same-origin" };
    if (body !== undefined) {
      if (raw) { opt.body = body; opt.headers["Content-Type"] = "text/xml"; }
      else { opt.body = JSON.stringify(body); opt.headers["Content-Type"] = "application/json"; }
    }
    return fetch(path, opt);
  }
  function show(loggedIn) { $("login").hidden = loggedIn; $("app").hidden = !loggedIn; }
  function err(msg) { $("err").textContent = msg || ""; }
  function when(t) {
    if (!t) return "never";
    var s = Math.max(0, Math.floor(Date.now() / 1000 - t));
    if (s < 90) return s + "s ago";
    if (s < 5400) return Math.round(s / 60) + "m ago";
    if (s < 129600) return Math.round(s / 3600) + "h ago";
    return Math.round(s / 86400) + "d ago";
  }
  function until(t) {
    var s = t - Math.floor(Date.now() / 1000);
    return s <= 0 ? "due" : "in " + (s < 5400 ? Math.round(s / 60) + "m" : Math.round(s / 3600) + "h");
  }
  function cell(tr, text, cls) { var td = document.createElement("td"); td.textContent = text; if (cls) td.className = cls; tr.appendChild(td); return td; }

  function loadFeeds() {
    return api("GET", "/api/health/feeds").then(function (r) {
      if (r.status === 401) { stop(); show(false); return; }
      return r.json().then(function (d) {
        var tb = $("feeds"); tb.textContent = "";
        d.feeds.forEach(function (f) {
          titles[String(f.id)] = f.title;
          var tr = document.createElement("tr");
          cell(tr, f.title).title = f.url;
          cell(tr, f.status, f.status);
          cell(tr, when(f.last_fetch_at));
          cell(tr, when(f.last_success_at));
          cell(tr, String(f.consecutive_failures), f.consecutive_failures ? "failing" : "");
          cell(tr, f.enabled ? until(f.next_fetch_at) : "-");
          var notes = f.notices.slice();
          if (f.disabled_reason && f.status !== "archive") notes.unshift("disabled: " + f.disabled_reason);
          if (f.host_throttled_until) notes.push("host held " + until(f.host_throttled_until));
          if (f.last_error && f.last_error_at && (!f.last_success_at || f.last_error_at >= f.last_success_at)) notes.unshift((f.last_error_class || "error") + ": " + f.last_error);
          cell(tr, notes.join("; "), f.redirect_pending ? "warn" : "mut");
          tb.appendChild(tr);
        });
        $("summary").textContent = d.feeds.length + " feeds, " + d.unread_total + " unread";
      });
    });
  }
  function loadStatus() {
    return api("GET", "/api/status").then(function (r) { return r.ok ? r.json() : null; }).then(function (s) {
      if (!s) return;
      $("runs").textContent = s.runs.length ? s.runs.map(function (x) { return x.kind + " " + x.done + "/" + x.total + " (" + x.new_items + " new, " + x.errors + " errors)"; }).join("; ") + " - " + s.inflight + " in flight" : "idle";
    });
  }
  function scheduleReload() {
    if (refreshTimer) return;
    refreshTimer = setTimeout(function () { refreshTimer = null; loadFeeds(); loadStatus(); }, 800);
  }
  function log(text, cls, raw) {
    var d = $("log"), line = document.createElement("div");
    line.textContent = new Date().toLocaleTimeString() + "  " + text;
    if (cls) line.className = cls;
    if (raw) line.title = raw;
    d.appendChild(line);
    while (d.childNodes.length > 200) d.removeChild(d.firstChild);
    d.scrollTop = d.scrollHeight;
  }
  function feedName(id) { return titles[String(id)] || "feed #" + id; }
  // Runs are scheduler refreshes (new items) or item runs such as filter_apply
  // and auto_read, which report "changed" instead.
  function runLine(r) {
    var what = r.changed !== undefined ? r.changed + " changed" : (r.new_items || 0) + " new";
    return (r.kind || "refresh") + " " + r.done + "/" + r.total + " (" + what + ", " + (r.errors || 0) + " errors)";
  }
  // Turn one server event into a short line. Raw JSON stays in the line's title.
  function describe(type, raw) {
    var d = {};
    try { d = JSON.parse(raw) || {}; } catch (x) { return [type, ""]; }
    var r;
    switch (type) {
      case "fetch.done":
        if (d.error) return ["fetch " + d.outcome + "  " + feedName(d.feed_id) + "  " + (d.error_class || "error") + ": " + d.error, "failing"];
        return ["fetch " + d.outcome + "  " + feedName(d.feed_id) + "  " + (d.new_items || 0) + " new", ""];
      case "run.start":
        r = runs[d.run_id] = { kind: d.kind, done: 0, total: d.total || 0, new_items: 0, errors: 0 };
        return [runLine(r), ""];
      case "run.progress":
        // run.progress carries no kind: keep the one run.start gave.
        r = runs[d.run_id] = { kind: (runs[d.run_id] || {}).kind, done: d.done, total: d.total, new_items: d.new_items, errors: d.errors, changed: d.changed };
        return [runLine(r), ""];
      case "run.done":
        r = runs[d.run_id] || { done: 0, total: 0 };
        if (d.kind) r.kind = d.kind;
        r.new_items = d.new_items; r.errors = d.errors; r.done = r.total;
        if (d.changed !== undefined) r.changed = d.changed;
        delete runs[d.run_id];
        return [runLine(r) + " done", d.errors ? "failing" : ""];
      case "items.state":
        return ["items changed (" + (d.ids ? d.ids.length : 0) + ")", ""];
      case "feed.changed":
        return ["feed changed" + (d.feed_id ? "  " + feedName(d.feed_id) : ""), ""];
      case "counts":
        return ["counts  " + (d.unread_total || 0) + " unread, " + (d.muted || 0) + " muted", ""];
      case "fulltext.ready":
        return ["full text ready (" + (d.ids ? d.ids.length : 0) + ")", ""];
      case "filters.changed":
        return ["filters changed", ""];
      case "folder.changed":
        return ["folders changed", ""];
      case "saved_searches.changed":
        return ["saved searches changed", ""];
      case "resync":
        return ["resync requested", "warn"];
    }
    return [type, ""];
  }
  function conn(text) { $("conn").textContent = text; }
  // The server sends a named heartbeat every 15 s; a long silence means the
  // stream is stuck even though the browser still thinks it is open.
  function checkBeat() {
    if (es && es.readyState === 1 && lastBeat && Date.now() - lastBeat > 45000) conn("events: stalled");
  }
  function start() {
    stop();
    es = new EventSource("/api/events");
    var mine = es;
    es.onopen = function () { retryMs = 1000; lastBeat = Date.now(); conn("events: live"); };
    es.onerror = function () {
      if (mine !== es) return;
      if (es.readyState !== 2) { conn("events: reconnecting"); return; } // the browser retries by itself
      // CLOSED: the browser will not retry (non-200, e.g. 401 once the session expired).
      es = null;
      api("GET", "/api/auth/me").then(function (r) {
        if (r.status === 401) { conn("events: signed out"); show(false); return; }
        conn("events: stopped, retrying in " + Math.round(retryMs / 1000) + "s");
        retryTimer = setTimeout(function () { retryTimer = null; start(); }, retryMs);
        retryMs = Math.min(retryMs * 2, 60000);
      }).catch(function () {
        conn("events: stopped, retrying in " + Math.round(retryMs / 1000) + "s");
        retryTimer = setTimeout(function () { retryTimer = null; start(); }, retryMs);
        retryMs = Math.min(retryMs * 2, 60000);
      });
    };
    es.addEventListener("heartbeat", function () { lastBeat = Date.now(); conn("events: live"); });
    if (!beatTimer) beatTimer = setInterval(checkBeat, 10000);
    // Every event the server publishes goes to the log. The feed table has no
    // folder, filter or saved-search column, so those three only log.
    var logOnly = { "items.state": true, "folder.changed": true, "filters.changed": true, "saved_searches.changed": true };
    ["run.start", "run.progress", "run.done", "fetch.done", "items.state", "counts", "feed.changed", "fulltext.ready",
      "filters.changed", "folder.changed", "saved_searches.changed", "resync"].forEach(function (t) {
      es.addEventListener(t, function (e) {
        var l = describe(t, e.data); log(l[0], l[1], e.data);
        if (!logOnly[t]) scheduleReload();
      });
    });
  }
  function stop() {
    if (es) { es.close(); es = null; }
    if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
  }

  var openTried = false; // one open-mode sign-in per page load, so a dropped cookie cannot loop
  function openRefused(r) {
    return r.json().then(function (e) { err("Kipple has no password, and " + (e.message || "this address may not use it") + "."); },
      function () { err("Kipple refused this request (" + r.status + ")."); });
  }
  function boot() {
    api("GET", "/api/auth/me").then(function (r) {
      if (r.status === 403) { show(false); $("login").hidden = true; return openRefused(r); }
      if (r.status === 401) {
        show(false);
        api("GET", "/api/instance").then(function (i) { return i.ok ? i.json() : null; }).then(function (d) {
          if (!d) return;
          if (d.setup) { $("login").hidden = true; err("Setup is pending: open Kipple to finish it."); return; }
          if (d.auth !== "open") return;
          // Open mode (no password): sign in without one, from where that is allowed.
          $("login").hidden = true;
          if (openTried) { err("Signing in without a password did not stick (are cookies blocked?)."); return; }
          openTried = true;
          return api("POST", "/api/auth/open").then(function (o) {
            if (o.status === 204) { boot(); return; }
            return openRefused(o);
          });
        }).catch(function () { err("cannot reach the server"); });
        return;
      }
      if (!r.ok) { err("the server answered " + r.status); return; }
      show(true); start(); loadFeeds(); loadStatus();
    }).catch(function () { err("cannot reach the server"); });
  }
  $("loginForm").addEventListener("submit", function (e) {
    e.preventDefault();
    var f = e.target;
    err("");
    api("POST", "/api/auth/login", { username: f.username.value, password: f.password.value }).then(function (r) {
      if (r.status === 204) { f.password.value = ""; boot(); }
      else if (r.status === 429) err("Too many failed attempts. Try again later.");
      else err("Sign-in failed.");
    });
  });
  $("logout").addEventListener("click", function () {
    api("POST", "/api/auth/logout").then(function () { stop(); show(false); });
  });
  $("refresh").addEventListener("click", function () {
    err("");
    api("POST", "/api/refresh").then(function (r) { if (!r.ok) err("Refresh failed (" + r.status + ")."); else scheduleReload(); });
  });
  $("export").addEventListener("click", function (e) {
    e.preventDefault();
    api("GET", "/api/opml").then(function (r) { return r.ok ? r.blob() : Promise.reject(); }).then(function (b) {
      var a = document.createElement("a"); a.href = URL.createObjectURL(b); a.download = "kipple.opml"; a.click();
      setTimeout(function () { URL.revokeObjectURL(a.href); }, 5000);
    }).catch(function () { err("Export failed."); });
  });
  $("importBtn").addEventListener("click", function () { $("importFile").click(); });
  $("importFile").addEventListener("change", function (e) {
    var file = e.target.files[0]; if (!file) return;
    file.text().then(function (t) { return api("POST", "/api/opml", t, true); }).then(function (r) {
      return r.json().then(function (d) {
        if (!r.ok) { err("Import failed: " + (d.error || r.status)); return; }
        err(""); log("import  " + d.feeds_added + " added, " + d.feeds_existing.length + " existing");
        scheduleReload();
      });
    }).catch(function () { err("Import failed."); });
    e.target.value = "";
  });

  boot();
  setInterval(function () { if (!$("app").hidden) loadFeeds(); }, 60000);
})();
