// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// Draws the finished periods the backend publishes under stats/, one file
// per kind of period: a bar chart of requests or unique clients, split by
// IP version or by cache use (two switches pick; clients by IP version
// only), with a scale, and a table. The data lists the newest period
// first; the chart shows it rightmost. Each chart spans as many periods as the backend keeps.
var KINDS = [
    ["minutes", "Last 60 minutes", 60], ["hours", "Last 48 hours", 48],
    ["days", "Last 30 days", 30], ["months", "Last 12 months", 12]
];

// The splits: CSS class, name, field of a period.
var SPLITS = {
    family: [["v4", "IPv4", "ipv4"], ["v6", "IPv6", "ipv6"]],
    cache: [["fresh", "Fresh", "fresh"], ["cached", "Cached", "cached"]]
};

function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
}

function pad(n) { return String(n).padStart(2, "0"); }

// ago names the UTC day of d relative to today.
function ago(d) {
    var now = new Date();
    var n = Math.round((Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()) -
        Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())) / 86400000);
    return n === 0 ? "Today" : n === 1 ? "Yesterday" : n + " days ago";
}

// label names a period by its start.
function label(kind, start) {
    var d = new Date(start);
    var ymd = d.getUTCFullYear() + "-" + pad(d.getUTCMonth() + 1) + "-" + pad(d.getUTCDate());
    var hm = pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
    if (kind === "minutes") return hm;
    if (kind === "hours") return ago(d) + " " + hm;
    if (kind === "days") return ymd;
    return ymd.slice(0, 7);
}

// The number the charts show, "requests" or "clients", and the split,
// "family" or "cache".
var metric = "requests", split = "family";
function value(r) { return r.ipv4[metric] + r.ipv6[metric]; }

// shown is the split the charts show. Clients are split by IP version only:
// a client counts as cached only when it got no fresh answer in a period,
// and a visitor's first check of a server is nearly always fresh.
function shown() { return metric === "clients" ? "family" : split; }

// recorded tells whether a period is split by cache use; periods from
// before the split was counted are not.
function recorded(r) { return !!(r.fresh && r.cached); }

// parts is a period's segments in split s.
function parts(r, s) {
    if (s === "cache" && !recorded(r)) return [["unrecorded", "Not recorded", value(r)]];
    return SPLITS[s].map(function (p) {
        var name = p[0] === "cached" && metric === "clients" ? "Cached only" : p[1];
        return [p[0], name, r[p[2]][metric]];
    });
}

// One box for all charts: the picked period's number in both splits, the
// chart's split first, absolute and in percent.
var tip = el("div", "tip");
tip.hidden = true;
document.body.appendChild(tip);

function showTip(kind, r, bar, touch) {
    var total = value(r);
    tip.textContent = "";
    tip.appendChild(el("div", "tip-head", label(kind, r.start)));
    var groups = metric === "clients" ? ["family"] : [split, split === "family" ? "cache" : "family"];
    groups.forEach(function (s) {
        var group = el("div", "tip-group");
        parts(r, s).forEach(function (p) {
            var line = el("div");
            line.appendChild(el("span", "key " + p[0]));
            var pct = total ? " (" + Math.round(p[2] / total * 100) + " %)" : "";
            line.appendChild(document.createTextNode(" " + p[1] + " " + p[2] + pct));
            group.appendChild(line);
        });
        tip.appendChild(group);
    });
    tip.hidden = false;
    // Beside the picked bar, level with the chart's top; on touch screens
    // above the chart, where the finger does not cover it.
    var b = bar.getBoundingClientRect(), w = tip.offsetWidth;
    var right = window.scrollX + document.documentElement.clientWidth - 8;
    var x = window.scrollX + b.right + 10, y = window.scrollY + b.top;
    if (touch) {
        x = window.scrollX + b.left + b.width / 2 - w / 2;
        y -= tip.offsetHeight + 8;
    }
    if (x + w > right) x = touch ? right - w : window.scrollX + b.left - w - 10;
    tip.style.left = Math.max(window.scrollX + 8, x) + "px";
    tip.style.top = Math.max(window.scrollY + 8, y) + "px";
}

var picked = null;
function unpick() {
    tip.hidden = true;
    if (!picked) return;
    picked.parentNode.classList.remove("picking");
    picked.classList.remove("picked");
    picked = null;
}

// nearest is the non-empty bar nearest to x, or -1; empty periods are
// skipped.
function nearest(bars, list, x) {
    var best = -1, dist = Infinity;
    Array.prototype.forEach.call(bars.children, function (bar, i) {
        if (!value(list[i])) return;
        var b = bar.getBoundingClientRect();
        var d = Math.abs(x - (b.left + b.width / 2));
        if (d < dist) { dist = d; best = i; }
    });
    return best;
}

function pick(kind, bars, list, i, touch) {
    if (i < 0) { unpick(); return; }
    var bar = bars.children[i];
    if (bar !== picked) {
        unpick();
        picked = bar;
        bars.classList.add("picking");
        bar.classList.add("picked");
    }
    showTip(kind, list[i], bar, touch);
}

// A mouse picks by hovering. A finger picks by a tap and follows while it
// slides sideways; a tap on the picked bar closes it.
var sliding = false;
function listen(kind, bars, list) {
    bars.addEventListener("pointerdown", function (e) {
        var touch = e.pointerType !== "mouse", i = nearest(bars, list, e.clientX);
        if (touch && i >= 0 && bars.children[i] === picked) { unpick(); return; }
        sliding = touch;
        pick(kind, bars, list, i, touch);
    });
    bars.addEventListener("pointermove", function (e) {
        if (e.pointerType === "mouse" || sliding) pick(kind, bars, list, nearest(bars, list, e.clientX), e.pointerType !== "mouse");
    });
    bars.addEventListener("pointerleave", function (e) { if (e.pointerType === "mouse") unpick(); });
}

// scaleTop is the smallest of 1, 2 or 5 times a power of ten that is at
// least max.
function scaleTop(max) {
    var p = Math.pow(10, Math.floor(Math.log10(Math.max(max, 1))));
    return [1, 2, 5, 10].map(function (m) { return m * p; }).filter(function (v) { return v >= max; })[0];
}

function chart(kind, rows) {
    var top = scaleTop(Math.max.apply(null, rows.map(value)));
    var c = el("div", "chart");
    var scale = el("div", "scale");
    [top, top / 2, 0].forEach(function (v) { scale.appendChild(el("span", "", v % 1 ? "" : String(v))); });
    c.appendChild(scale);
    var bars = el("div", "bars");
    var list = rows.slice().reverse();
    list.forEach(function (r) {
        var bar = el("div", "bar");
        parts(r, shown()).forEach(function (p) {
            var seg = el("div", p[0]);
            seg.style.height = (p[2] / top * 100) + "%";
            bar.appendChild(seg);
        });
        bars.appendChild(bar);
    });
    listen(kind, bars, list);
    c.appendChild(bars);
    return c;
}

// table shows both numbers of the chart's split.
function table(kind, rows) {
    var t = el("table"), head = el("tr");
    head.appendChild(el("th", "", ""));
    SPLITS[shown()].forEach(function (p) {
        head.appendChild(el("th", "", p[1] + " clients"));
        head.appendChild(el("th", "", p[1] + " requests"));
    });
    t.appendChild(head);
    rows.forEach(function (r) {
        var tr = el("tr");
        tr.appendChild(el("td", "", label(kind, r.start)));
        SPLITS[shown()].forEach(function (p) {
            var c = r[p[2]];
            tr.appendChild(el("td", "", c ? String(c.clients) : "-"));
            tr.appendChild(el("td", "", c ? String(c.requests) : "-"));
        });
        t.appendChild(tr);
    });
    return t;
}

// before is the start of the period before the one starting at start.
function before(kind, start) {
    var d = new Date(start);
    if (kind === "minutes") return new Date(d - 60000);
    if (kind === "hours") return new Date(d - 3600000);
    if (kind === "days") return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() - 1));
    return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - 1, 1));
}

// padded is rows followed by empty older periods, n in all.
function padded(kind, rows, n) {
    var all = rows.slice();
    while (all.length < n) {
        all.push({
            start: before(kind, all[all.length - 1].start).toISOString(),
            ipv4: { requests: 0, clients: 0 }, ipv6: { requests: 0, clients: 0 }
        });
    }
    return all;
}

function fill(s, kind, rows) {
    if (!rows.length) {
        s.appendChild(el("p", "empty", "No finished period yet."));
        return;
    }
    var all = padded(kind, rows, spans[kind]);
    s.appendChild(chart(kind, all));
    var axis = el("div", "axis");
    axis.appendChild(el("span", "", label(kind, all[all.length - 1].start)));
    axis.appendChild(el("span", "", label(kind, all[0].start)));
    s.appendChild(axis);
    var details = el("details");
    details.appendChild(el("summary", "", "Numbers"));
    details.appendChild(table(kind, rows));
    s.appendChild(details);
}

// A tap or click outside the charts and scrolling close the box. Taps on
// plain page areas do not reach a click handler in iOS Safari.
document.addEventListener("pointerdown", function (e) { if (!e.target.closest(".bars")) unpick(); });
document.addEventListener("pointerup", function () { sliding = false; });
document.addEventListener("pointercancel", function () { sliding = false; });
window.addEventListener("scroll", function () { if (!sliding) unpick(); });

var box = document.getElementById("stats");
box.textContent = "";
var sections = {}, loaded = {}, spans = {};

// legend shows the keys of the chart's split.
function legend() {
    document.querySelectorAll(".legend-key").forEach(function (e) {
        e.hidden = e.dataset.view !== shown();
    });
}

// draw fills a section from the loaded data; the numbers table stays open
// when it was.
function draw(kind) {
    var s = sections[kind];
    var details = s.querySelector("details");
    var open = !!(details && details.open);
    while (s.children.length > 1) s.removeChild(s.lastChild);
    fill(s, kind, loaded[kind]);
    details = s.querySelector("details");
    if (details) details.open = open;
}

KINDS.forEach(function (k) {
    var s = el("section");
    s.appendChild(el("h2", "", k[1]));
    box.appendChild(s);
    sections[k[0]] = s;
    spans[k[0]] = k[2];
    fetch("stats/" + k[0] + ".json", { cache: "no-cache" }).then(function (res) {
        if (!res.ok) throw new Error(res.status);
        return res.json();
    }).then(function (data) {
        loaded[k[0]] = data.periods || [];
        draw(k[0]);
    }, function () {
        s.appendChild(el("p", "empty", "Not available."));
    });
});

// switches marks the picked buttons. The split switch is off while clients
// are shown; its pick comes back with requests.
function switches() {
    document.querySelectorAll(".switch button").forEach(function (b) {
        b.classList.toggle("on", b.dataset.metric ? b.dataset.metric === metric : b.dataset.view === shown());
        b.disabled = !!b.dataset.view && metric === "clients";
    });
}

function redraw() {
    unpick();
    switches();
    legend();
    Object.keys(loaded).forEach(draw);
}

document.querySelectorAll(".switch").forEach(function (sw) {
    var buttons = sw.querySelectorAll("button");
    buttons.forEach(function (b) {
        b.addEventListener("click", function () {
            if (b.classList.contains("on")) return;
            buttons.forEach(function (o) { o.classList.toggle("on", o === b); });
            if (b.dataset.metric) metric = b.dataset.metric;
            else split = b.dataset.view;
            redraw();
        });
    });
});
legend();
