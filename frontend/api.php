<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// Same-origin relay for the page. The browser never talks to anything but
// this host; this script passes each call on to the backend. config.php
// sets:
//   MC_BACKEND   base URL of the backend, "" when none is configured
//   MC_VERSION   release tag of the deployed page
//
// .htaccess maps api/<endpoint> here; the endpoint is the last path segment.
//   api/config   -> {"backend": true|false, "version": ...}
//   api/ping     -> the backend's /ping, see docs/api.md
//   api/health   -> the backend's /health, one copy for all visitors
//
// Also the router for the built-in development server: other paths are
// served as static files, scripts excepted.
ini_set('display_errors', '0');
$path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH) ?: '';
if (PHP_SAPI === 'cli-server' && !preg_match('#/api/[^/]+$#', $path) && !str_ends_with($path, '.php')) {
    return false;
}

header('Content-Type: application/json');
header('X-Content-Type-Options: nosniff');
header('Cache-Control: no-store');

$config = __DIR__ . '/config.php';
if (is_file($config)) {
    require $config;
}
defined('MC_BACKEND') || define('MC_BACKEND', '');
defined('MC_VERSION') || define('MC_VERSION', 'dev');

function fail(int $code, string $msg): never {
    http_response_code($code);
    echo json_encode(['error' => $msg]);
    exit;
}

// backend asks the backend and returns [status, body, retry-after]; the
// body is null without a JSON answer. With forward, the backend gets the
// client address for its per-client limits.
function backend(string $endpoint, array $params, bool $forward): array {
    $url = rtrim(MC_BACKEND, '/') . '/' . $endpoint . ($params ? '?' . http_build_query($params) : '');
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT        => 15,
        CURLOPT_USERAGENT      => 'mc_dualstack_check/' . MC_VERSION . ' (https://www.poggensee.it/mc_dualstack_check/)',
        CURLOPT_HTTPHEADER     => $forward ? ['X-Forwarded-For: ' . ($_SERVER['REMOTE_ADDR'] ?? '')] : [],
    ]);
    $retry  = null;
    curl_setopt($ch, CURLOPT_HEADERFUNCTION, function ($ch, $line) use (&$retry) {
        if (stripos($line, 'Retry-After:') === 0) {
            $retry = (int)trim(substr($line, 12));
        }
        return strlen($line);
    });
    $raw    = curl_exec($ch);
    $status = curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    curl_close($ch);
    if ($raw === false || json_decode($raw) === null) {
        return [0, null, null];
    }
    return [$status, $raw, $retry];
}

// respond passes a backend answer on with its status code.
function respond(int $status, ?string $raw, ?int $retry): never {
    if ($raw === null) {
        fail(502, 'upstream unreachable');
    }
    http_response_code($status);
    if ($status === 429 || $status === 503) {
        header('Retry-After: ' . (int)($retry ?? 10));
    }
    echo $raw;
    exit;
}

// relay passes one call on to the backend on behalf of the client.
function relay(string $endpoint, array $params = []): never {
    if (MC_BACKEND === '') {
        fail(503, 'no backend configured');
    }
    respond(...backend($endpoint, $params, true));
}

// The backend refreshes its health every 7 s; asking more often gains
// nothing. .htaccess keeps the dot file off the web.
const HEALTH_TTL   = 7;
const HEALTH_CACHE = __DIR__ . '/.health.json';

// health answers all visitors from one copy of the backend's /health,
// fetched at most every HEALTH_TTL seconds and as this host, so no
// visitor's reloads count against the backend's health limit. Without a
// usable cache file it relays directly.
function health(): never {
    if (MC_BACKEND === '') {
        fail(503, 'no backend configured');
    }
    $fh = @fopen(HEALTH_CACHE, 'c+');
    if ($fh === false || !flock($fh, LOCK_EX)) {
        relay('health');
    }
    clearstatcache(true, HEALTH_CACHE);
    $body = stream_get_contents($fh);
    if ($body === false || $body === '' || time() - filemtime(HEALTH_CACHE) >= HEALTH_TTL) {
        [$status, $body, $retry] = backend('health', [], false);
        if ($status !== 200) {
            flock($fh, LOCK_UN);
            fclose($fh);
            respond($status, $body, $retry);
        }
        ftruncate($fh, 0);
        rewind($fh);
        fwrite($fh, $body);
        fflush($fh);
    }
    flock($fh, LOCK_UN);
    fclose($fh);
    echo $body;
    exit;
}

switch (basename($path)) {
case 'config':
    echo json_encode(['backend' => MC_BACKEND !== '', 'version' => MC_VERSION]);
    break;

case 'ping':
    // Only the backend's own parameters, as plain strings; it validates them.
    $params = [];
    foreach (['ip', 'host', 'family', 'port', 'edition'] as $name) {
        $v = $_GET[$name] ?? '';
        if (is_string($v) && $v !== '') {
            $params[$name] = $v;
        }
    }
    relay('ping', $params);

case 'health':
    health();

default:
    fail(404, 'unknown endpoint');
}
