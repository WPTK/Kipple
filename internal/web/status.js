(function () {
  "use strict";
  var $ = function (id) { return document.getElementById(id); };
  var H = { "X-Kipple-Client": "web" };
  var es = null, refreshTimer = null;
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
          if (f.last_error && f.last_error_at && (!f.last_success_at || f.last_error_at >= f.last_success_at)) notes.unshift((f.last_error_class || "error") + ": " + f.last_error);
          cell(tr, notes.join("; "), f.migrated ? "warn" : "mut");
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
  function runLine(r) { return "refresh " + r.done + "/" + r.total + " (" + r.new_items + " new, " + r.errors + " errors)"; }
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
        r = runs[d.run_id] = { done: 0, total: d.total || 0, new_items: 0, errors: 0 };
        return [runLine(r), ""];
      case "run.progress":
        r = runs[d.run_id] = { done: d.done, total: d.total, new_items: d.new_items, errors: d.errors };
        return [runLine(r), ""];
      case "run.done":
        r = runs[d.run_id] || { done: 0, total: 0 };
        r.new_items = d.new_items; r.errors = d.errors; r.done = r.total;
        delete runs[d.run_id];
        return [runLine(r) + " done", d.errors ? "failing" : ""];
      case "items.state":
        return ["items changed (" + (d.ids ? d.ids.length : 0) + ")", ""];
      case "feed.changed":
        return ["feed changed" + (d.feed_id ? "  " + feedName(d.feed_id) : ""), ""];
      case "resync":
        return ["resync requested", "warn"];
    }
    return [type, ""];
  }
  function start() {
    stop();
    es = new EventSource("/api/events");
    es.onopen = function () { $("conn").textContent = "events: live"; };
    es.onerror = function () { $("conn").textContent = "events: reconnecting"; };
    ["run.start", "run.progress", "run.done", "fetch.done", "items.state", "counts", "feed.changed", "resync"].forEach(function (t) {
      es.addEventListener(t, function (e) {
        var l = describe(t, e.data); log(l[0], l[1], e.data);
        if (t !== "items.state") scheduleReload();
      });
    });
  }
  function stop() { if (es) { es.close(); es = null; } }

  function boot() {
    api("GET", "/api/auth/me").then(function (r) {
      if (r.status === 401) { show(false); return; }
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
