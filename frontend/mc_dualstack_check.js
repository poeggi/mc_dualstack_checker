// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// The backend's base URL ("" when none is configured), as the web host
// put it into the page.
var apiBase = document.body.dataset.api || "";
var backendReady = apiBase !== "";
// Seconds the backend caches a probe result.
var CACHE_TTL = 60;
// Up to this age of the backend's copy, the page answers a probe itself:
// half the backend's cache time.
var REUSE_SECONDS = CACHE_TTL / 2;
// Milliseconds the page takes to give such an answer, so the check still
// shows its brief loading state.
var REUSE_DELAY_MS = 100;
// The web host's copy of the backend's health counts as current up to
// HOST_HEALTH_TTL seconds of age. An older copy is refreshed right after it
// is served, within HOST_REFRESH_MS.
var HOST_HEALTH_TTL = 7;
var HOST_REFRESH_MS = 6500;
// Milliseconds the page waits for the backend's answer to a probe. The
// backend answers within 12 s: a name lookup and a probe take at most 10 s.
var PROBE_TIMEOUT_MS = 12000;
// Default ports per edition. The IPv6 port defaults to the IPv4 port;
// Bedrock servers commonly listen on 19133 for IPv6, so that is the first
// IPv6 fallback.
var EDITIONS = { bedrock: { v4: 19132, v6: 19133 }, java: { v4: 25565, v6: 25565 } };
var DEFAULT_EDITION = "bedrock";

var $ = function (id) { return document.getElementById(id); };
var form = $("mcform");
var hostEl = $("f-host"), port4El = $("f-port4"), port6El = $("f-port6");
var editionEl = $("f-edition"), nofallbackEl = $("f-nofallback"), submitBtn = $("f-submit");

// -- Reload after a long time in background (iOS home screen app) --
(function () {
    var THRESHOLD = 15 * 60 * 1000;
    var hiddenAt = null;
    document.addEventListener("visibilitychange", function () {
        if (document.hidden) {
            hiddenAt = Date.now();
        } else {
            if (hiddenAt && (Date.now() - hiddenAt) > THRESHOLD) window.location.reload();
            hiddenAt = null;
        }
    });
})();

// -- Time-ago counter --------------------------------------------
var queriedAt = 0;
var cacheExpiry = 0; // epoch seconds until which a cached answer stays fresh, 0 = live

function timeAgo(ts) {
    var sec = Math.floor(Date.now() / 1000) - ts;
    if (sec < 5) return "just now";
    if (sec < 60) return ">" + sec + "s ago";
    var min = Math.floor(sec / 60);
    if (min < 60) return ">" + min + "m ago";
    var hr = Math.floor(min / 60);
    if (hr < 24) return ">" + hr + "h ago";
    return ">" + Math.floor(hr / 24) + "d ago";
}
function formatQueried(ts) {
    var now = new Date();
    var d = new Date(ts * 1000);
    var pad = function (n) { return String(n).padStart(2, "0"); };
    var time = pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    var nowDay = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    var qDay = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    var diffDay = Math.round((nowDay - qDay) / 86400000);
    var dayLabel;
    if (diffDay === 0) dayLabel = "today";
    else if (diffDay === 1) dayLabel = "yesterday";
    else dayLabel = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate());
    return "Queried " + dayLabel + " " + time + " - " + timeAgo(ts);
}
function formatCacheExpiry(exp) {
    var sec = exp - Math.floor(Date.now() / 1000);
    if (sec <= 0) return "can refresh now";
    if (sec < 60) return "can refresh in " + sec + "s";
    var min = Math.floor(sec / 60), s = sec % 60;
    return "can refresh in " + min + "m" + (s ? " " + s + "s" : "");
}
function updateTimeAgo() {
    if (!queriedAt) return;
    $("time-ago").textContent = cacheExpiry ? "Cached - " + formatCacheExpiry(cacheExpiry) : formatQueried(queriedAt);
}
setInterval(updateTimeAgo, 1000);

// -- Form helpers ------------------------------------------------
function updatePort4Placeholder() {
    if (!port4El.value.trim()) port4El.placeholder = String(EDITIONS[editionEl.value].v4);
}
editionEl.addEventListener("change", updatePort4Placeholder);
port4El.addEventListener("input", function () { port6El.placeholder = port4El.value.trim() || "as IPv4"; });

function validPort(val) {
    if (val === "") return true;
    var n = parseInt(val, 10);
    return !isNaN(n) && n >= 1 && n <= 65535 && String(n) === val;
}
function validatePortField(el) {
    el.classList.toggle("input-error", !validPort(el.value.trim()));
}
port4El.addEventListener("blur", function () { validatePortField(port4El); });
port6El.addEventListener("blur", function () { validatePortField(port6El); });

function setBusy(busy) {
    submitBtn.classList.toggle("btn-active", busy);
    submitBtn.disabled = busy;
    $("checking-indicator").hidden = !busy;
}

function showNotice(msg) {
    var n = $("notice");
    n.textContent = msg ? "\u26A0 " + msg : "";
    n.hidden = !msg;
}

// -- Query state <-> URL -----------------------------------------
function readForm() {
    return {
        host: hostEl.value.trim(),
        port4: port4El.value.trim(),
        port6: port6El.value.trim(),
        edition: editionEl.value,
        nofallback: nofallbackEl.checked
    };
}
function toParams(q) {
    var p = new URLSearchParams();
    p.set("host", q.host);
    if (q.port4 && +q.port4 !== EDITIONS[q.edition].v4) p.set("port4", q.port4);
    if (q.port6) p.set("port6", q.port6);
    if (q.edition !== DEFAULT_EDITION) p.set("edition", q.edition);
    if (q.nofallback) p.set("nofallback", "1");
    return p;
}
function fromParams(p) {
    var edition = p.get("edition") === "java" ? "java" : "bedrock";
    return {
        host: (p.get("host") || "").trim(),
        port4: p.get("port4") || p.get("port") || "",
        port6: p.get("port6") || "",
        edition: edition,
        nofallback: p.has("nofallback")
    };
}
function fillForm(q) {
    hostEl.value = q.host;
    port4El.value = q.port4;
    port6El.value = q.port6;
    editionEl.value = q.edition;
    nofallbackEl.checked = q.nofallback;
    updatePort4Placeholder();
    port6El.placeholder = q.port4 || "as IPv4";
}

// -- API ---------------------------------------------------------
// Probes go straight to the backend at apiBase. An answer that takes
// longer than PROBE_TIMEOUT_MS counts as unreachable. Errors are thrown as
// { rateLimited, retry } | { unreachable } | { message }.
function api(endpoint, params) {
    var q = new URLSearchParams(params || {}).toString();
    var ctrl = new AbortController();
    var timer = setTimeout(function () { ctrl.abort(); }, PROBE_TIMEOUT_MS);
    return fetch(apiBase + "/" + endpoint + (q ? "?" + q : ""), { cache: "no-store", signal: ctrl.signal }).then(
        function (res) {
            return res.json().catch(function () { return {}; }).then(function (body) {
                clearTimeout(timer);
                if (res.status === 429) throw { rateLimited: true, retry: parseInt(res.headers.get("Retry-After"), 10) || 10 };
                if (!res.ok) throw { message: body.error || ("Request failed (" + res.status + ")") };
                return body;
            });
        },
        function () {
            clearTimeout(timer);
            throw { unreachable: true };
        }
    );
}

// -- Check orchestration -----------------------------------------
// The backend only resolves names and runs single probes. Everything
// else (literal IP handling, fallback order, per-family independence,
// the log) happens here.
var retryTimer = null;

function literalIP(host) {
    var h = host.replace(/^\[|\]$/g, "");
    var m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(h);
    if (m && m.slice(1).every(function (o) { return +o <= 255; })) return { family: 4, ip: h };
    if (h.indexOf(":") >= 0 && /^[0-9a-fA-F:.]+$/.test(h)) return { family: 6, ip: h };
    return null;
}

// Probes one family on the given ports in order and stops at the first
// answer. For a host name the first probe also resolves it on the backend;
// later ports reuse the address it returned.
function checkFamily(fam, target, ports, edition, log) {
    var family = "IPv" + fam, record = fam === 4 ? "A" : "AAAA";
    var result = { state: "offline", ip: target.ip || "", ports_tried: [], statuses: [] };
    function tryPort(i) {
        if (i >= ports.length) {
            if (result.rejected) {
                log.push("UNREACHABLE: " + family + " was rejected on the way");
                result.state = "unreachable";
            } else {
                log.push("OFFLINE: " + family + " no server on any port");
            }
            return result;
        }
        var port = ports[i];
        if (i > 0) log.push("Port " + ports[i - 1] + " failed, retrying port " + port);
        log.push("Checking " + family + ": " + (result.ip || target.host) + " port " + port);
        var params = { port: port, edition: edition };
        if (result.ip) params.ip = result.ip; else params.family = fam;
        if (target.host) params.host = target.host;
        return ping(params).then(function (r) {
            if (r.state === "no_dns") {
                log.push("Resolved " + family + ": no " + record + " record");
                return { state: "no_dns", reason: "No " + record + " record found" };
            }
            if (r.state === "dns_error") {
                log.push("ERROR: " + family + " lookup failed: " + (r.error || "unknown"));
                return { state: "dns_error", reason: record + " lookup failed (" + (r.error || "error") + ")" };
            }
            if (!result.ip && r.ip) {
                result.ip = r.ip;
                log.push("Resolved " + family + ": " + r.ip);
            }
            result.ports_tried.push(port);
            if (r.state === "online") {
                var how = (r.rtt_ms ? r.rtt_ms + "ms" : "") + (r.cached ? ", cached " + r.age_s + "s ago" : "");
                log.push("ONLINE: " + family + " responded on port " + port + (how ? " (" + how.replace(/^, /, "") + ")" : ""));
                result.state = "online"; result.port = port; result.info = r.info;
                result.rtt_ms = r.rtt_ms; result.cached = r.cached; result.age_s = r.age_s;
                return result;
            }
            if (r.state === "no_route") {
                log.push("ERROR: checker has no " + family + " connectivity: " + r.error);
                result.state = "no_route";
                result.reason = "The checker host has no " + family + " connectivity" + (r.error ? " (" + r.error + ")" : "");
                result.cached = r.cached; result.age_s = r.age_s;
                return result;
            }
            var reason = r.error || "no response";
            log.push(family + " port " + port + ": " + reason);
            result.statuses.push({ port: port, text: reason.charAt(0).toUpperCase() + reason.slice(1) });
            if (r.state === "unreachable") result.rejected = true;
            result.cached = r.cached; result.age_s = r.age_s;
            return tryPort(i + 1);
        });
    }
    return tryPort(0);
}

// -- Answer reuse --------------------------------------------------
// While the backend's copy of a probe answer is younger than
// REUSE_SECONDS, the page gives the answer the backend would give:
// marked cached, with the age it has by now. Answers are kept per browser
// tab, keyed by the request, so reloads reuse them too. Only the states
// the backend caches are kept.
var REUSE_PREFIX = "ping:";
var CACHED_STATES = { online: true, offline: true, unreachable: true, no_route: true };

function keptAge(kept, now) {
    return kept.r.age_s + Math.floor((now - kept.at) / 1000);
}
function ping(params) {
    var key = REUSE_PREFIX + new URLSearchParams(params).toString();
    try {
        var kept = JSON.parse(sessionStorage.getItem(key) || "null");
        if (kept) {
            var age = keptAge(kept, Date.now());
            if (age < REUSE_SECONDS) {
                kept.r.cached = true;
                kept.r.age_s = age;
                return new Promise(function (resolve) {
                    setTimeout(function () { resolve(kept.r); }, REUSE_DELAY_MS);
                });
            }
        }
    } catch (e) {}
    return api("ping", params).then(function (r) {
        if (CACHED_STATES[r.state]) keep(key, { at: Date.now(), r: r });
        return r;
    });
}
function keep(key, entry) {
    try {
        for (var i = sessionStorage.length - 1; i >= 0; i--) {
            var k = sessionStorage.key(i);
            if (k.indexOf(REUSE_PREFIX) !== 0) continue;
            var old = JSON.parse(sessionStorage.getItem(k) || "null");
            if (!old || keptAge(old, entry.at) >= REUSE_SECONDS) sessionStorage.removeItem(k);
        }
        sessionStorage.setItem(key, JSON.stringify(entry));
    } catch (e) {}
}

// hostHealth asks this host for its copy of the backend's health. It
// resolves to { up, data, current }: up when the web host reached the
// backend, current when the copy is at most HOST_HEALTH_TTL old.
function hostHealth() {
    return fetch("api/backend-health", { cache: "no-store" }).then(function (res) {
        var age = parseInt(res.headers.get("Age"), 10);
        return res.json().catch(function () { return {}; }).then(function (body) {
            return { up: res.ok && !!body.ok, data: body, current: age <= HOST_HEALTH_TTL };
        });
    }, function () {
        return { up: false, data: {}, current: true };
    });
}

// currentHealth is the web host's current view of the backend. When its
// copy is older, or there is none yet, it asks again once the web host has
// refreshed it.
function currentHealth() {
    return hostHealth().then(function (h) {
        if (h.current) return h;
        return new Promise(function (resolve) { setTimeout(resolve, HOST_REFRESH_MS); }).then(hostHealth);
    });
}

function runCheck(q) {
    if (retryTimer) {
        clearInterval(retryTimer); retryTimer = null;
        $("checking-indicator").querySelector(".checking-label").textContent = "Checking\u2026";
    }
    showNotice("");
    $("results").hidden = true;
    document.title = q.host + " - Minecraft Server Dualstack Checker";
    if (!backendReady) {
        showNotice("No checker backend is configured yet. The check cannot run.");
        return;
    }
    setBusy(true);

    var edition = q.edition;
    var defaults = EDITIONS[edition];
    var port4 = q.port4 ? parseInt(q.port4, 10) : defaults.v4;
    var port6 = q.port6 ? parseInt(q.port6, 10) : port4;
    // Probe order: the requested port, then the edition defaults (deduplicated).
    var portOrder = function (first, fallbacks) {
        return [first].concat(q.nofallback ? [] : fallbacks)
            .filter(function (p, i, a) { return a.indexOf(p) === i; });
    };
    var ports4 = portOrder(port4, [defaults.v4]);
    var ports6 = portOrder(port6, [defaults.v6, defaults.v4]);

    var startedAt = Math.floor(Date.now() / 1000);
    var log = [], log4 = [], log6 = [];
    var literal = literalIP(q.host);
    var target = literal ? { ip: literal.ip } : { host: q.host };
    var probe = function (fam, ports, famLog) {
        if (!literal || literal.family === fam) return checkFamily(fam, target, ports, edition, famLog);
        return { state: "omitted", reason: "Input is a literal IPv" + literal.family + " address" };
    };
    if (literal) log.push("Input is a literal IPv" + literal.family + " address, skipping DNS");

    Promise.all([probe(4, ports4, log4), probe(6, ports6, log6)]).then(function (both) {
        setBusy(false);
        render({ queried_at: startedAt, ipv4: both[0], ipv6: both[1], log: log.concat(log4, log6) });
    }).catch(function (err) {
        if (err && err.rateLimited) { startRetryCountdown(err.retry); return; }
        setBusy(false);
        if (err && err.unreachable) {
            // The web host's view of the API tells the two cases apart.
            currentHealth().then(function (h) {
                showNotice(h.up ? "The API is online, but your browser cannot reach it." : "API unavailable.");
            });
        } else {
            showNotice(err && err.message ? err.message : "Check failed.");
        }
    });
}

function startRetryCountdown(secs) {
    var label = $("checking-indicator").querySelector(".checking-label");
    var tick = function () {
        if (secs <= 0) {
            clearInterval(retryTimer); retryTimer = null;
            setBusy(false);
            label.textContent = "Checking\u2026";
            showNotice("Too many requests - ready, try again.");
            return;
        }
        showNotice("Too many requests, slow down!");
        label.textContent = "Wait " + secs + "s\u2026";
        secs--;
    };
    tick();
    retryTimer = setInterval(tick, 1000);
}

// -- Render ------------------------------------------------------
function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
}
function row(key, valueNode, dim) {
    var r = el("div", "ip-row");
    r.appendChild(el("span", "ip-row-key text-label text-caps", key));
    var v = el("span", "ip-row-val" + (dim ? " text-dim" : ""));
    if (typeof valueNode === "string") v.textContent = valueNode; else v.appendChild(valueNode);
    r.appendChild(v);
    return r;
}
function portList(ports, used) {
    var frag = document.createDocumentFragment();
    ports.forEach(function (p, i) {
        frag.appendChild(el("span", p === used ? "port-used text-strong" : "port-struck", String(p)));
        if (i < ports.length - 1) frag.appendChild(document.createTextNode(" "));
    });
    return frag;
}

// One line per port tried, naming the port when there are several.
function statusLines(statuses) {
    var frag = document.createDocumentFragment();
    statuses.forEach(function (s, i) {
        if (i > 0) frag.appendChild(document.createElement("br"));
        frag.appendChild(document.createTextNode(statuses.length > 1 ? s.port + ": " + s.text : s.text));
    });
    return frag;
}

var cardCounter = 0;

function ipCard(r, label) {
    var state = r.state;
    var cls = state === "online" ? "online" : state === "offline" || state === "unreachable" ? "offline" : "unknown";
    var card = el("div", "card ip-card " + cls);
    var head = el("div", "ip-card-head text-strong");
    head.appendChild(el("span", "", label));
    var badges = el("span", "badge-group");
    if (state === "online" || state === "offline" || state === "unreachable" || state === "no_route") {
        badges.appendChild(el("span", r.cached ? "badge cache" : "badge live", r.cached ? "Cached" : "Live"));
    }
    if (state === "online" && r.ports_tried.length > 1) badges.appendChild(el("span", "badge fallback", "Fallback"));
    var badgeText = {
        online: "Online", offline: "Offline", unreachable: "Unreachable", no_dns: "No DNS",
        dns_error: "No DNS", omitted: "Omitted", no_route: "Unavailable"
    }[state] || state;
    badges.appendChild(el("span", "badge " + cls, badgeText));
    head.appendChild(badges);
    card.appendChild(head);
    if (r.ip) card.appendChild(el("div", "ip-resolved text-small text-muted", r.ip));

    var rows = el("div", "ip-rows");
    card.appendChild(rows);

    if (state === "online") {
        var info = r.info || {};
        rows.appendChild(row("Port", portList(r.ports_tried, r.port)));
        if (r.rtt_ms) rows.appendChild(row("Latency", r.rtt_ms + "ms" + (r.cached ? ", cached " + r.age_s + "s ago" : "")));
        rows.appendChild(row("Players", info.players_online + " / " + info.players_max));
        if (info.motd) rows.appendChild(row("MOTD", info.motd));
        if (info.version) rows.appendChild(row("Version", info.version));

        var extra = [
            ["Map", info.map], ["Gamemode", info.gamemode], ["Protocol", info.protocol],
            ["Edition", info.edition], ["Server ID", info.server_id]
        ].filter(function (kv) { return kv[1]; });
        if (extra.length) {
            var id = "card-extra-" + (++cardCounter);
            var cb = el("input", "card-extra-toggle");
            cb.type = "checkbox"; cb.id = id; cb.setAttribute("aria-hidden", "true");
            rows.appendChild(cb);
            var extraWrap = el("div", "ip-rows-extra");
            extra.forEach(function (kv) { extraWrap.appendChild(row(kv[0], kv[1])); });
            rows.appendChild(extraWrap);
            var toggle = el("label", "ip-row ip-row-toggle");
            toggle.htmlFor = id;
            var tl = el("span", "toggle-label text-small");
            tl.appendChild(el("span", "toggle-more", "\u25BC More"));
            tl.appendChild(el("span", "toggle-less", "\u25B2 Less"));
            toggle.appendChild(tl);
            rows.appendChild(toggle);
        }
    } else if (state === "offline" || state === "unreachable") {
        rows.appendChild(row("Status", statusLines(r.statuses), true));
        rows.appendChild(row("Ports tried", portList(r.ports_tried, -1)));
    } else {
        rows.appendChild(row("Info", r.reason || "", true));
    }
    return card;
}

function render(data) {
    queriedAt = data.queried_at;
    cacheExpiry = 0;
    [data.ipv4, data.ipv6].forEach(function (r) {
        if (r.cached) cacheExpiry = Math.max(cacheExpiry, queriedAt + CACHE_TTL - (r.age_s || 0));
    });
    updateTimeAgo();

    var grid = $("ip-grid");
    grid.textContent = "";
    grid.appendChild(ipCard(data.ipv4, "IPv4"));
    grid.appendChild(ipCard(data.ipv6, "IPv6"));

    var showDebug = [data.ipv4.state, data.ipv6.state].some(function (st) {
        return st === "offline" || st === "unreachable" || st === "no_route" || st === "dns_error";
    });
    var dbg = $("debug-log");
    dbg.textContent = "";
    data.log.forEach(function (line) {
        var cls = "";
        if (line.indexOf("ONLINE") === 0) cls = "d-ok";
        else if (/^(ERROR|OFFLINE|UNREACHABLE)/.test(line)) cls = "d-err";
        else if (line.indexOf("failed") >= 0 || line.indexOf("retrying") >= 0) cls = "d-warn";
        dbg.appendChild(el("div", cls, line));
    });
    $("debug-card").hidden = !showDebug;
    $("results").hidden = false;
}

// -- Startup, submit, deep links ---------------------------------
form.addEventListener("submit", function (e) {
    e.preventDefault();
    var q = readForm();
    if (!q.host) { hostEl.focus(); return; }
    validatePortField(port4El);
    validatePortField(port6El);
    if (!validPort(q.port4) || !validPort(q.port6)) return;
    history.pushState(null, "", "?" + toParams(q).toString());
    runCheck(q);
});

function loadFromURL() {
    var q = fromParams(new URLSearchParams(window.location.search));
    fillForm(q);
    if (q.host && validPort(q.port4) && validPort(q.port6)) runCheck(q);
}
window.addEventListener("popstate", loadFromURL);

loadFromURL();
