"use strict";

var API_BASE = (window.MC_API_BASE || "").replace(/\/+$/, "");
var DEFAULT_PORTS = { bedrock: "19132", java: "25565" };
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
function updateTimeAgo() {
    if (queriedAt) $("time-ago").textContent = formatQueried(queriedAt);
}
setInterval(updateTimeAgo, 1000);

// -- Form helpers ------------------------------------------------
function updatePort4Placeholder() {
    if (!port4El.value.trim()) port4El.placeholder = DEFAULT_PORTS[editionEl.value] || DEFAULT_PORTS.bedrock;
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
    if (q.port4 && q.port4 !== DEFAULT_PORTS[q.edition]) p.set("port4", q.port4);
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

// -- Check -------------------------------------------------------
var retryTimer = null;

function runCheck(q) {
    if (retryTimer) { clearInterval(retryTimer); retryTimer = null; }
    showNotice("");
    $("results").hidden = true;
    document.title = q.host + " - Minecraft Server Dualstack Checker";
    if (!API_BASE) {
        showNotice("No checker backend is configured yet. The check cannot run.");
        return;
    }
    setBusy(true);

    fetch(API_BASE + "/check?" + toParams(q).toString(), { cache: "no-store" })
        .then(function (res) {
            return res.json().then(function (body) { return { res: res, body: body }; });
        })
        .then(function (r) {
            if (r.res.status === 429) {
                startRetryCountdown(parseInt(r.res.headers.get("Retry-After"), 10) || 10);
                return;
            }
            if (!r.res.ok) {
                setBusy(false);
                showNotice(r.body && r.body.error ? r.body.error : "Request failed (" + r.res.status + ")");
                return;
            }
            setBusy(false);
            render(r.body);
        })
        .catch(function () {
            setBusy(false);
            showNotice("The checker backend (" + API_BASE + ") could not be reached. Please try again in a moment.");
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
    if (state === "online" && r.ports_tried.length > 1) badges.appendChild(el("span", "badge fallback", "Fallback"));
    var badgeText = { online: "Online", offline: "Offline", no_dns: "No DNS record", omitted: "Omitted", no_route: "Unavailable" }[state] || state;
    badges.appendChild(el("span", "badge " + cls, badgeText));
    head.appendChild(badges);
    card.appendChild(head);
    if (r.ip) card.appendChild(el("div", "ip-resolved", r.ip));

    var rows = el("div", "ip-rows");
    card.appendChild(rows);

    if (state === "online") {
        var info = r.info || {};
        rows.appendChild(row("Port", portList(r.ports_tried, r.port)));
        rows.appendChild(row("Players", info.players_online + " / " + info.players_max));
        if (info.motd) rows.appendChild(row("MOTD", info.motd));
        if (info.map) rows.appendChild(row("Map", info.map));

        var extra = [
            ["Version", info.version], ["Gamemode", info.gamemode], ["Protocol", info.protocol],
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
    updateTimeAgo();

    var grid = $("ip-grid");
    grid.textContent = "";
    grid.appendChild(ipCard(data.ipv4, "IPv4"));
    grid.appendChild(ipCard(data.ipv6, "IPv6"));

    var anyOffline = data.ipv4.state === "offline" || data.ipv6.state === "offline";
    var anyError = (data.log || []).some(function (l) { return l.indexOf("ERROR") === 0; });
    var dbg = $("debug-log");
    dbg.textContent = "";
    (data.log || []).forEach(function (line) {
        var cls = "";
        if (line.indexOf("ONLINE") === 0) cls = "d-ok";
        else if (line.indexOf("ERROR") === 0 || line.indexOf("OFFLINE") === 0) cls = "d-err";
        else if (line.indexOf("failed") >= 0 || line.indexOf("retrying") >= 0) cls = "d-warn";
        dbg.appendChild(el("div", cls, line));
    });
    $("debug-card").hidden = !(anyOffline || anyError);
    $("results").hidden = false;
}

// -- Submit / deep links -----------------------------------------
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
