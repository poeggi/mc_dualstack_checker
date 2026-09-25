<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// This host's view of the backend: one copy of the backend's /health for
// all visitors. The page asks it once on load, for the footer, and when a
// probe fails. The page sends probes to the backend itself.
//
// .htaccess maps api/<endpoint> here; the endpoint is the last path segment.
//   api/backend-health   -> the copy, with its age in the Age header
//
// Also the router for the built-in development server: other paths are
// served as files, scripts excepted.
ini_set('display_errors', '0');
$path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH) ?: '';
if (PHP_SAPI === 'cli-server' && !preg_match('#/api/[^/]+$#', $path) && !str_ends_with($path, '.php')) {
    return false;
}

header('Content-Type: application/json');
header('X-Content-Type-Options: nosniff');
header('Cache-Control: no-store');

// The frontend deploy writes the backend's base URL ("" when none is
// configured) and the release tag into these two lines.
define('MC_BACKEND', 'http://localhost:8080');
define('MC_VERSION', 'dev');

// Seconds one call to the backend may take. Two tries stay within
// HEALTH_TTL.
const BACKEND_TIMEOUT = 3;

// The backend refreshes its health every 7 s; asking more often gains
// nothing. .htaccess keeps the dot files off the web, and the deploy leaves
// them in place.
const HEALTH_TTL   = 7;
const HEALTH_CACHE = __DIR__ . '/.health.json';
const HEALTH_LOCK  = __DIR__ . '/.health.lock';

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

// kept_health is this host's copy of the backend's /health, with 'age' in
// seconds added, or null while there is none.
function kept_health(): ?array {
    $kept = json_decode((string)@file_get_contents(HEALTH_CACHE), true);
    if (!isset($kept['at'], $kept['status'])) {
        return null;
    }
    $kept['age'] = max(0, time() - (int)$kept['at']);
    return $kept;
}

// refresh_health refreshes a copy older than HEALTH_TTL, or a missing one.
// It asks the backend once more when the first try got no answer, and
// replaces the copy in one step. Only the request that gets the lock
// refreshes; a failure is kept like an answer. Call it after finish().
function refresh_health(?array $kept): void {
    if (MC_BACKEND === '' || ($kept !== null && $kept['age'] < HEALTH_TTL)) {
        return;
    }
    set_time_limit(4 * BACKEND_TIMEOUT);
    $lock = @fopen(HEALTH_LOCK, 'c');
    if ($lock === false || !flock($lock, LOCK_EX | LOCK_NB)) {
        return;
    }
    $kept = kept_health();
    if ($kept === null || $kept['age'] >= HEALTH_TTL) {
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

function fail(int $code, string $msg): never {
    http_response_code($code);
    echo json_encode(['error' => $msg]);
    exit;
}

if (basename($path) !== 'backend-health') {
    fail(404, 'unknown endpoint');
}
if (MC_BACKEND === '') {
    fail(503, 'no backend configured');
}

// The copy is answered at once, with its age in the Age header, or 503
// while there is none yet. Visitors' reloads never reach the backend.
$kept = kept_health();
if ($kept === null) {
    finish(503, json_encode(['error' => 'health not known yet']), ['Retry-After: ' . HEALTH_TTL]);
} elseif ($kept['body'] === null) {
    finish(502, json_encode(['error' => 'upstream unreachable']), ['Age: ' . $kept['age']]);
} else {
    $headers = ['Age: ' . $kept['age']];
    if ($kept['status'] === 429 || $kept['status'] === 503) {
        $headers[] = 'Retry-After: ' . (int)($kept['retry'] ?? 10);
    }
    finish((int)$kept['status'], $kept['body'], $headers);
}
refresh_health($kept);
