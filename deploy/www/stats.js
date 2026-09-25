// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// Draws the finished periods the backend publishes under stats/, one file
// per kind of period: a bar chart of requests or unique clients (a switch
// picks), IPv4 and IPv6 stacked, with a scale, and a table with both. The
// data lists the newest period first; the chart shows it rightmost.
var KINDS = [
    ["minutes", "Last 60 minutes"], ["hours", "Last 24 hours"],
    ["days", "Last 30 days"], ["months", "Last 12 months"]
];

function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
}

function pad(n) { return String(n).padStart(2, "0"); }

// label names a period by its start.
function label(kind, start) {
    var d = new Date(start);
    var ymd = d.getUTCFullYear() + "-" + pad(d.getUTCMonth() + 1) + "-" + pad(d.getUTCDate());
    var hm = pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
    if (kind === "minutes") return hm;
    if (kind === "hours") return ymd + " " + hm;
    if (kind === "days") return ymd;
    return ymd.slice(0, 7);
}

// The number the charts show, "requests" or "clients".
var metric = "requests";
function value(r) { return r.ipv4[metric] + r.ipv6[metric]; }

// One box for all bars: the period's clients per family, absolute and in
// percent. It follows the mouse, and a tap opens it on touch screens.
var tip = el("div", "tip");
tip.hidden = true;
document.body.appendChild(tip);

function showTip(kind, r, e) {
    var total = value(r);
    tip.textContent = "";
    tip.appendChild(el("div", "tip-head", label(kind, r.start)));
    [["v4", "IPv4", r.ipv4[metric]], ["v6", "IPv6", r.ipv6[metric]]].forEach(function (f) {
        var line = el("div");
        line.appendChild(el("span", "key " + f[0]));
        var pct = total ? " (" + Math.round(f[2] / total * 100) + " %)" : "";
        line.appendChild(document.createTextNode(" " + f[1] + " " + f[2] + pct));
        tip.appendChild(line);
    });
    tip.hidden = false;
    moveTip(e);
}

function moveTip(e) {
    var x = e.pageX + 12, y = e.pageY - tip.offsetHeight - 12;
    if (x + tip.offsetWidth > window.scrollX + document.documentElement.clientWidth - 8) x = e.pageX - tip.offsetWidth - 12;
    tip.style.left = Math.max(window.scrollX + 8, x) + "px";
    tip.style.top = Math.max(window.scrollY + 8, y) + "px";
}

function hideTip() { tip.hidden = true; }

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
    rows.slice().reverse().forEach(function (r) {
        var bar = el("div", "bar");
        bar.addEventListener("mouseenter", function (e) { showTip(kind, r, e); });
        bar.addEventListener("mousemove", moveTip);
        bar.addEventListener("mouseleave", hideTip);
        bar.addEventListener("click", function (e) { e.stopPropagation(); showTip(kind, r, e); });
        [["v4", r.ipv4[metric]], ["v6", r.ipv6[metric]]].forEach(function (part) {
            var seg = el("div", part[0]);
            seg.style.height = (part[1] / top * 100) + "%";
            bar.appendChild(seg);
        });
        bars.appendChild(bar);
    });
    c.appendChild(bars);
    return c;
}

function table(kind, rows) {
    var t = el("table"), head = el("tr");
    ["", "IPv4 clients", "IPv4 requests", "IPv6 clients", "IPv6 requests"].forEach(function (h) {
        head.appendChild(el("th", "", h));
    });
    t.appendChild(head);
    rows.forEach(function (r) {
        var tr = el("tr");
        [label(kind, r.start), r.ipv4.clients, r.ipv4.requests, r.ipv6.clients, r.ipv6.requests]
            .forEach(function (v) { tr.appendChild(el("td", "", String(v))); });
        t.appendChild(tr);
    });
    return t;
}

function fill(s, kind, rows) {
    if (!rows.length) {
        s.appendChild(el("p", "empty", "No finished period yet."));
        return;
    }
    s.appendChild(chart(kind, rows));
    var axis = el("div", "axis");
    axis.appendChild(el("span", "", label(kind, rows[rows.length - 1].start)));
    axis.appendChild(el("span", "", label(kind, rows[0].start)));
    s.appendChild(axis);
    var details = el("details");
    details.appendChild(el("summary", "", "Numbers"));
    details.appendChild(table(kind, rows));
    s.appendChild(details);
}

document.addEventListener("click", hideTip);

var box = document.getElementById("stats");
box.textContent = "";
var sections = {}, loaded = {};

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

var buttons = document.querySelectorAll(".switch button");
buttons.forEach(function (b) {
    b.addEventListener("click", function () {
        if (b.dataset.metric === metric) return;
        metric = b.dataset.metric;
        buttons.forEach(function (o) { o.classList.toggle("on", o === b); });
        Object.keys(loaded).forEach(draw);
    });
});
