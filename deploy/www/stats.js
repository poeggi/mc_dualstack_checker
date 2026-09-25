// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// Draws stats.json, which the backend rewrites once a minute: per kind of
// period a bar chart of unique clients, IPv4 and IPv6 stacked, and a table
// with clients and requests. The running period comes first in the data.
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

// label names a period by its start, in UTC.
function label(kind, start) {
    var d = new Date(start);
    var ymd = d.getUTCFullYear() + "-" + pad(d.getUTCMonth() + 1) + "-" + pad(d.getUTCDate());
    var hm = pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
    if (kind === "minutes") return hm;
    if (kind === "hours") return ymd + " " + hm;
    if (kind === "days") return ymd;
    return ymd.slice(0, 7);
}

function chart(kind, rows, max) {
    var c = el("div", "chart");
    rows.slice().reverse().forEach(function (r) {
        var bar = el("div", "bar" + (r.current ? " current" : ""));
        bar.title = label(kind, r.start) + ": " + r.ipv4.clients + " IPv4, " + r.ipv6.clients + " IPv6 clients";
        [["v4", r.ipv4.clients], ["v6", r.ipv6.clients]].forEach(function (part) {
            var seg = el("div", part[0]);
            seg.style.height = (part[1] / max * 100) + "%";
            bar.appendChild(seg);
        });
        c.appendChild(bar);
    });
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
        [label(kind, r.start) + (r.current ? " *" : ""), r.ipv4.clients, r.ipv4.requests,
            r.ipv6.clients, r.ipv6.requests].forEach(function (v) { tr.appendChild(el("td", "", String(v))); });
        t.appendChild(tr);
    });
    return t;
}

function section(kind, title, rows) {
    var max = Math.max.apply(null, rows.map(function (r) { return r.ipv4.clients + r.ipv6.clients; }));
    var s = el("section");
    s.appendChild(el("h2", "", title));
    s.appendChild(chart(kind, rows, Math.max(max, 1)));
    var axis = el("div", "axis");
    axis.appendChild(el("span", "", label(kind, rows[rows.length - 1].start)));
    axis.appendChild(el("span", "", "max " + max + (max === 1 ? " client" : " clients")));
    axis.appendChild(el("span", "", label(kind, rows[0].start)));
    s.appendChild(axis);
    var details = el("details");
    details.appendChild(el("summary", "", "Numbers"));
    details.appendChild(table(kind, rows));
    s.appendChild(details);
    return s;
}

fetch("stats.json", { cache: "no-cache" }).then(function (res) {
    if (!res.ok) throw new Error(res.status);
    return res.json();
}).then(function (data) {
    var box = document.getElementById("stats");
    box.textContent = "";
    KINDS.forEach(function (k) {
        if (data[k[0]] && data[k[0]].length) box.appendChild(section(k[0], k[1], data[k[0]]));
    });
}, function () {
    document.getElementById("stats").textContent = "No usage numbers yet.";
});
