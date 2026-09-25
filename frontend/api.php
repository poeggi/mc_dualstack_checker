<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// This host's view of the backend, for the page when a probe fails and for
// anyone who wants to check. The page sends probes to the backend itself.
//
// .htaccess maps api/<endpoint> here; the endpoint is the last path segment.
//   api/backend-health   -> the backend's /health, one copy for all visitors
//
// Also the router for the built-in development server: other paths are
// served as files, scripts excepted.
$path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH) ?: '';
if (PHP_SAPI === 'cli-server' && !preg_match('#/api/[^/]+$#', $path) && !str_ends_with($path, '.php')) {
    return false;
}

require __DIR__ . '/common.php';

header('Content-Type: application/json');
header('X-Content-Type-Options: nosniff');
header('Cache-Control: no-store');

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
