<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// Same-origin helper for the page: it tells the page where the API is and
// keeps one copy of the API's health. The page sends probes to the API
// itself. config.php sets:
//   MC_BACKEND   base URL of the API, "" when none is configured
//   MC_VERSION   release tag of the deployed page
//
// .htaccess maps api/<endpoint> here; the endpoint is the last path segment.
//   api/config   -> {"backend": "<API URL>" | "", "version": ...}
//   api/health   -> the API's /health, one copy for all visitors, see health()
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

// Seconds one call to the backend may take. Two tries stay within
// HEALTH_TTL.
const BACKEND_TIMEOUT = 3;

// backend asks the backend and returns [status, body, retry-after]; status
// is 0 and body null without a JSON answer.
function backend(string $endpoint): array {
    $ch = curl_init(rtrim(MC_BACKEND, '/') . '/' . $endpoint);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT        => BACKEND_TIMEOUT,
        CURLOPT_USERAGENT      => 'mc_dualstack_check/' . MC_VERSION . ' (https://www.poggensee.it/mc_dualstack_check/)',
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

// finish sends a complete answer and ends the response, so work done after
// it keeps no client waiting.
function finish(int $status, string $body, array $headers): void {
    ignore_user_abort(true);
    @ini_set('zlib.output_compression', '0');
    http_response_code($status);
    foreach ($headers as $h) {
        header($h);
    }
    header('Content-Length: ' . strlen($body));
    header('Connection: close');
    echo $body;
    while (ob_get_level() > 0) {
        ob_end_flush();
    }
    flush();
    if (function_exists('fastcgi_finish_request')) {
        fastcgi_finish_request();
    }
}

// The backend refreshes its health every 7 s; asking more often gains
// nothing. .htaccess keeps the dot files off the web, and the deploy leaves
// them in place.
const HEALTH_TTL   = 7;
const HEALTH_CACHE = __DIR__ . '/.health.json';
const HEALTH_LOCK  = __DIR__ . '/.health.lock';

// health answers at once from this host's copy of the backend's /health,
// with the copy's age in the Age header, or with 503 while there is no copy
// yet. Visitors' reloads never reach the backend. A copy older than
// HEALTH_TTL is refreshed after the answer has gone out, so no visitor waits
// for the backend.
function health(): never {
    if (MC_BACKEND === '') {
        fail(503, 'no backend configured');
    }
    $kept = json_decode((string)@file_get_contents(HEALTH_CACHE), true);
    if (!isset($kept['at'], $kept['status'])) {
        finish(503, json_encode(['error' => 'health not known yet']), ['Retry-After: ' . HEALTH_TTL]);
        refresh_health();
        exit;
    }
    $age = max(0, time() - (int)$kept['at']);
    $headers = ['Age: ' . $age];
    if ($kept['body'] === null) {
        $status = 502;
        $body = json_encode(['error' => 'upstream unreachable']);
    } else {
        $status = (int)$kept['status'];
        $body = $kept['body'];
        if ($status === 429 || $status === 503) {
            $headers[] = 'Retry-After: ' . (int)($kept['retry'] ?? 10);
        }
    }
    finish($status, $body, $headers);
    if ($age >= HEALTH_TTL) {
        refresh_health();
    }
    exit;
}

// refresh_health asks the backend once more when the first try got no
// answer, and replaces the copy in one step. Only the request that gets the
// lock refreshes; a failure is kept like an answer.
function refresh_health(): void {
    set_time_limit(4 * BACKEND_TIMEOUT);
    $lock = @fopen(HEALTH_LOCK, 'c');
    if ($lock === false || !flock($lock, LOCK_EX | LOCK_NB)) {
        return;
    }
    $kept = json_decode((string)@file_get_contents(HEALTH_CACHE), true);
    if (!isset($kept['at']) || time() - (int)$kept['at'] >= HEALTH_TTL) {
        [$status, $body, $retry] = backend('health');
        if ($status === 0) {
            [$status, $body, $retry] = backend('health');
        }
        $tmp = HEALTH_CACHE . '.' . getmypid();
        $copy = json_encode(['at' => time(), 'status' => $status, 'body' => $body, 'retry' => $retry]);
        if (@file_put_contents($tmp, $copy) !== false) {
            @rename($tmp, HEALTH_CACHE);
        }
    }
    flock($lock, LOCK_UN);
    fclose($lock);
}

switch (basename($path)) {
case 'config':
    echo json_encode(['backend' => rtrim(MC_BACKEND, '/'), 'version' => MC_VERSION], JSON_UNESCAPED_SLASHES);
    break;

case 'health':
    health();

default:
    fail(404, 'unknown endpoint');
}
