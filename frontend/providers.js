// SPDX-License-Identifier: AGPL-3.0-or-later
"use strict";

// Backend providers. Each one offers the three calls the orchestration in
// mc_dualstack_check.js needs, with the result shapes documented in README
// under "API":
//
//   resolve(host)                    -> { a: [...], aaaa: [...], errors?: {a, aaaa} }
//   ping(ip, port, edition, host)    -> { state, info?, error?, rtt_ms?, cached?, age_s? }
//   health()                         -> { ipv6?: bool }
//
// Every call goes to api.php on this host, which resolves names itself and
// relays probes upstream. Providers only differ in how they read the
// upstream's answer. Errors are thrown as
// { rateLimited, retry } | { unreachable } | { message }.
var PROVIDERS = {};

function api(op, params) {
    var q = new URLSearchParams(params || {});
    q.set("op", op);
    return fetch("api.php?" + q.toString(), { cache: "no-store" }).then(
        function (res) {
            return res.json().catch(function () { return {}; }).then(function (body) {
                if (res.status === 429) throw { rateLimited: true, retry: parseInt(res.headers.get("Retry-After"), 10) || 10 };
                if (!res.ok) throw { message: body.error || ("Request failed (" + res.status + ")") };
                return body;
            });
        },
        function () { throw { unreachable: true }; }
    );
}

// -- own: the Go service in backend/ ------------------------------
PROVIDERS.own = {
    label: "the checker backend",
    notice: "",
    cacheTTL: 60,
    offlineTTL: 10,
    resolve: function (host) { return api("resolve", { host: host }); },
    ping: function (ip, port, edition, host) {
        var params = { ip: ip, port: port, edition: edition };
        if (host) params.host = host;
        return api("ping", params);
    },
    health: function () { return api("health"); }
};

// -- mcsrvstat: api.mcsrvstat.us, third-party ----------------------
// Cached on their side for about 5 minutes.
PROVIDERS.mcsrvstat = {
    label: "api.mcsrvstat.us",
    notice: "Third-party results via mcsrvstat.us, cached up to 5 min.",
    cacheTTL: 300,
    offlineTTL: 300,
    resolve: PROVIDERS.own.resolve,
    ping: function (ip, port, edition) {
        return api("ping", { ip: ip, port: port, edition: edition }).then(function (r) {
            var dbg = r.debug || {};
            var cached = !!dbg.cachehit;
            var age = dbg.cachetime ? Math.max(0, Math.floor(Date.now() / 1000) - dbg.cachetime) : 0;
            if (!r.online) {
                var msgs = (dbg.errors || []).map(function (e) { return e.type + ": " + e.message; });
                return { state: "offline", error: msgs.join("; ") || "no response", cached: cached, age_s: age };
            }
            var players = r.players || {};
            return {
                state: "online",
                cached: cached,
                age_s: age,
                info: {
                    edition: r.edition || (edition === "java" ? "Java" : ""),
                    motd: ((r.motd || {}).clean || []).join("\n").trim(),
                    version: typeof r.version === "string" ? r.version : "",
                    protocol: r.protocol && r.protocol.version != null ? String(r.protocol.version) : "",
                    players_online: players.online || 0,
                    players_max: players.max || 0,
                    gamemode: r.gamemode || "",
                    map: (r.map || {}).clean || "",
                    server_id: r.serverid || ""
                }
            };
        });
    },
    health: function () { return Promise.resolve({}); }
};
