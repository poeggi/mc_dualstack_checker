// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// The provider (see providers.js) is chosen server-side and read once at
// startup. null = no backend configured, checks disabled.
var provider = null;
// Default ports per edition. Bedrock servers commonly listen on 19133 for
// IPv6, so that is the IPv6 default and the first IPv6 fallback.
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
    if (sec < 60) return "> " + sec + "s ago";
    var min = Math.floor(sec / 60);
    if (min < 60) return "> " + min + "m ago";
    var hr = Math.floor(min / 60);
    if (hr < 24) return "> " + hr + "h ago";
    return "> " + Math.floor(hr / 24) + "d ago";
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
    if (sec <= 0) return "fresh now";
    if (sec < 60) return "fresh in " + sec + "s";
    var min = Math.floor(sec / 60), s = sec % 60;
    return "fresh in " + min + "m" + (s ? " " + s + "s" : "");
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

// -- Check orchestration -----------------------------------------
// The provider only resolves names and runs single probes. Everything
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

// Probes one family on the given ports in order and stops at the first answer.
function checkFamily(family, ip, ports, edition, hostLabel, log) {
    var result = { state: "offline", ip: ip, ports_tried: [] };
    function tryPort(i) {
        if (i >= ports.length) {
            log.push("OFFLINE: " + family + " did not respond on any port");
            return result;
        }
        var port = ports[i];
        if (i > 0) log.push("Port " + ports[i - 1] + " failed, retrying port " + port);
        log.push("Checking " + family + ": " + ip + " port " + port);
        result.ports_tried.push(port);
        return provider.ping(ip, port, edition, hostLabel).then(function (r) {
            if (r.state === "online") {
                var how = (r.rtt_ms ? r.rtt_ms + " ms" : "") + (r.cached ? ", cached " + r.age_s + " s ago" : "");
                log.push("ONLINE: " + family + " responded on port " + port + (how ? " (" + how.replace(/^, /, "") + ")" : ""));
                result.state = "online"; result.port = port; result.info = r.info;
                result.rtt_ms = r.rtt_ms; result.cached = r.cached; result.age_s = r.age_s;
                return result;
            }
            if (r.state === "no_route") {
                log.push("ERROR: checker has no " + family + " connectivity: " + r.error);
                result.state = "no_route";
                result.reason = "The checker host has no " + family + " connectivity";
                result.cached = r.cached; result.age_s = r.age_s;
                return result;
            }
            log.push(family + " port " + port + ": " + (r.error || "no response"));
            result.cached = r.cached; result.age_s = r.age_s;
            return tryPort(i + 1);
        });
    }
    return tryPort(0);
}

function runCheck(q) {
    if (retryTimer) { clearInterval(retryTimer); retryTimer = null; }
    showNotice("");
    $("results").hidden = true;
    document.title = q.host + " - Minecraft Server Dualstack Checker";
    if (!provider) {
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
    var hostLabel = literal ? "" : q.host;

    var dns;
    if (literal) {
        log.push("Input is a literal IPv" + literal.family + " address, skipping DNS");
        dns = Promise.resolve(literal.family === 4 ? { a: [literal.ip], aaaa: [] } : { a: [], aaaa: [literal.ip] });
    } else {
        dns = provider.resolve(q.host).then(function (r) {
            var errs = r.errors || {};
            var dnsLine = function (family, record, ips, err) {
                if (ips[0]) return "Resolved " + family + ": " + ips[0];
                if (err) return "ERROR: " + family + " lookup failed: " + err;
                return "Resolved " + family + ": no " + record + " record";
            };
            log.push(dnsLine("IPv4", "A", r.a, errs.a));
            log.push(dnsLine("IPv6", "AAAA", r.aaaa, errs.aaaa));
            return r;
        });
    }

    dns.then(function (r) {
        var ip4 = r.a[0], ip6 = r.aaaa[0];
        var errs = r.errors || {};
        var skip = function (family) {
            var record = family === 4 ? "A" : "AAAA";
            if (literal) return { state: "omitted", reason: "Input is a literal IPv" + literal.family + " address" };
            if (errs[record.toLowerCase()]) return { state: "dns_error", reason: record + " lookup failed" };
            return { state: "no_dns", reason: "No " + record + " record found" };
        };
        return Promise.all([
            ip4 ? checkFamily("IPv4", ip4, ports4, edition, hostLabel, log4) : skip(4),
            ip6 ? checkFamily("IPv6", ip6, ports6, edition, hostLabel, log6) : skip(6)
        ]);
    }).then(function (both) {
        setBusy(false);
        render({ queried_at: startedAt, ipv4: both[0], ipv6: both[1], log: log.concat(log4, log6) });
    }).catch(function (err) {
        if (err && err.rateLimited) { startRetryCountdown(err.retry); return; }
        setBusy(false);
        if (err && err.unreachable) {
            showNotice("Cannot reach " + provider.label + ". Try again shortly.");
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
    r.appendChild(el("span", "ip-row-key", key));
    var v = el("span", "ip-row-val" + (dim ? " dim" : ""));
    if (typeof valueNode === "string") v.textContent = valueNode; else v.appendChild(valueNode);
    r.appendChild(v);
    return r;
}
function portList(ports, used) {
    var frag = document.createDocumentFragment();
    ports.forEach(function (p, i) {
        frag.appendChild(el("span", p === used ? "port-used" : "port-struck", String(p)));
        if (i < ports.length - 1) frag.appendChild(document.createTextNode(" "));
    });
    return frag;
}

var cardCounter = 0;

function ipCard(r, label) {
    var state = r.state;
    var cls = state === "online" ? "online" : state === "offline" ? "offline" : "unknown";
    var card = el("div", "ip-card " + cls);
    var head = el("div", "ip-card-head");
    head.appendChild(el("span", "", label));
    var badges = el("span", "badge-group");
    if (state === "online" || state === "offline" || state === "no_route") {
        badges.appendChild(el("span", r.cached ? "badge cache" : "badge live", r.cached ? "Cached" : "Live"));
    }
    if (state === "online" && r.ports_tried.length > 1) badges.appendChild(el("span", "badge fallback", "Fallback"));
    var badgeText = {
        online: "Online", offline: "Offline", no_dns: "No DNS record",
        dns_error: "DNS error", omitted: "Omitted", no_route: "Unavailable"
    }[state] || state;
    badges.appendChild(el("span", "badge " + cls, badgeText));
    head.appendChild(badges);
    card.appendChild(head);
    if (r.ip) card.appendChild(el("div", "ip-resolved", r.ip));

    var rows = el("div", "ip-rows");
    card.appendChild(rows);

    if (state === "online") {
        var info = r.info || {};
        rows.appendChild(row("Port", portList(r.ports_tried, r.port)));
        if (r.rtt_ms) rows.appendChild(row("Latency", r.rtt_ms + " ms" + (r.cached ? ", cached " + r.age_s + " s ago" : "")));
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
            var tl = el("span", "toggle-label");
            tl.appendChild(el("span", "toggle-more", "\u25BC More"));
            tl.appendChild(el("span", "toggle-less", "\u25B2 Less"));
            toggle.appendChild(tl);
            rows.appendChild(toggle);
        }
    } else if (state === "offline") {
        rows.appendChild(row("Status", "No response", true));
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
        if (r.cached) cacheExpiry = Math.max(cacheExpiry, queriedAt + provider.cacheTTL - (r.age_s || 0));
    });
    updateTimeAgo();

    var grid = $("ip-grid");
    grid.textContent = "";
    grid.appendChild(ipCard(data.ipv4, "IPv4"));
    grid.appendChild(ipCard(data.ipv6, "IPv6"));

    var showDebug = [data.ipv4.state, data.ipv6.state].some(function (st) {
        return st === "offline" || st === "no_route" || st === "dns_error";
    });
    var dbg = $("debug-log");
    dbg.textContent = "";
    data.log.forEach(function (line) {
        var cls = "";
        if (line.indexOf("ONLINE") === 0) cls = "d-ok";
        else if (line.indexOf("ERROR") === 0 || line.indexOf("OFFLINE") === 0) cls = "d-err";
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

// Say up front when results deserve a caveat: a third-party provider, or
// a backend without IPv6 that cannot judge IPv6 reachability.
function backendNotice(msg) {
    var n = $("notice-backend");
    n.textContent = "\u26A0 " + msg;
    n.hidden = false;
}

api("config").then(function (cfg) {
    provider = PROVIDERS[cfg.provider] || null;
    if (cfg.version) $("version").textContent = cfg.version;
}, function () {
    provider = null;
}).then(function () {
    loadFromURL();
    if (!provider) return;
    if (provider.notice) backendNotice(provider.notice);
    provider.health().then(function (h) {
        if (h.version) $("version").textContent += ", API " + h.version;
        if (h.ipv6 === false) backendNotice("The checker backend has no IPv6 connectivity. IPv6 results are not meaningful.");
    }).catch(function () {});
});
