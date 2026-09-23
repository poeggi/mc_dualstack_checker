<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// Same-origin API for the page. The browser never talks to anything but
// this host; this script resolves names itself and relays probes to the
// configured backend. config.php selects the provider:
//   MC_PROVIDER  "own" (the backend at MC_BACKEND) or "mcsrvstat"
//   MC_BACKEND   base URL of the own backend
//   MC_VERSION   release tag of the deployed page
//
// .htaccess maps api/<endpoint> here; the endpoint is the last path segment.
//   api/config                                  -> {"provider": ..., "version": ...}
//   api/resolve?host=<name>                     -> {"a":[..],"aaaa":[..],"errors"?:{..}}
//   api/ping?ip=<addr>&port=<n>&edition=<e>     -> the upstream's JSON, unchanged
//   api/health                                  -> the own backend's /health, or {}
//
// Also the router for the built-in development server: other paths are
// served as static files, scripts excepted.
$path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH) ?: '';
if (PHP_SAPI === 'cli-server' && !preg_match('#/api/[^/]+$#', $path) && !str_ends_with($path, '.php')) {
    return false;
}

header('Content-Type: application/json');
header('Cache-Control: no-store');

$config = __DIR__ . '/config.php';
if (is_file($config)) {
    require $config;
}
defined('MC_PROVIDER') || define('MC_PROVIDER', '');
defined('MC_BACKEND') || define('MC_BACKEND', '');
defined('MC_VERSION') || define('MC_VERSION', 'dev');

// param reads a query parameter; anything but a plain string counts as absent.
function param(string $name): string {
    $v = $_GET[$name] ?? '';
    return is_string($v) ? $v : '';
}

function fail(int $code, string $msg): never {
    http_response_code($code);
    echo json_encode(['error' => $msg]);
    exit;
}

// relay fetches a JSON document and passes it through with its status code.
// The own backend gets the client address for its per-client limits.
function relay(string $url, bool $own = false): never {
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT        => 15,
        CURLOPT_USERAGENT      => 'mc_dualstack_check/' . MC_VERSION . ' (https://www.poggensee.it/mc_dualstack_check/)',
        CURLOPT_HTTPHEADER     => $own ? ['X-Forwarded-For: ' . ($_SERVER['REMOTE_ADDR'] ?? '')] : [],
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
        fail(502, 'upstream unreachable');
    }
    http_response_code($status);
    if ($status === 429 || $status === 503) {
        header('Retry-After: ' . (int)($retry ?? 10));
    }
    echo $raw;
    exit;
}

switch (basename($path)) {
case 'config':
    echo json_encode(['provider' => MC_PROVIDER, 'version' => MC_VERSION]);
    break;

case 'resolve':
    $host = trim(param('host'));
    if ($host === '' || strlen($host) > 253 || preg_match('/[^A-Za-z0-9._-]/', $host)) {
        fail(400, 'host is missing or invalid');
    }
    $a    = @dns_get_record($host, DNS_A);
    $aaaa = @dns_get_record($host, DNS_AAAA);
    // dns_get_record() answers false both for a failed query and, depending
    // on the resolver, for a nonexistent name. One successful query proves
    // the resolver works, so a lone false means "no record".
    if ($a === false && $aaaa === false) {
        echo json_encode(['a' => [], 'aaaa' => [], 'errors' => ['a' => 'lookup failed', 'aaaa' => 'lookup failed']]);
        break;
    }
    echo json_encode([
        'a'    => array_column($a ?: [], 'ip'),
        'aaaa' => array_column($aaaa ?: [], 'ipv6'),
    ]);
    break;

case 'ping':
    $ip      = trim(param('ip'), '[]');
    $port    = (int)param('port');
    $edition = param('edition') ?: 'bedrock';
    if (filter_var($ip, FILTER_VALIDATE_IP) === false) {
        fail(400, 'ip must be a literal IPv4 or IPv6 address');
    }
    if ($port < 1 || $port > 65535) {
        fail(400, 'port must be between 1 and 65535');
    }
    if (!in_array($edition, ['bedrock', 'java'], true)) {
        fail(400, 'edition must be bedrock or java');
    }
    if (MC_PROVIDER === 'own' && MC_BACKEND !== '') {
        $q = http_build_query(['ip' => $ip, 'port' => $port, 'edition' => $edition, 'host' => param('host')]);
        relay(rtrim(MC_BACKEND, '/') . '/ping?' . $q, true);
    }
    if (MC_PROVIDER === 'mcsrvstat') {
        $target = (str_contains($ip, ':') ? "[$ip]" : $ip) . ":$port";
        relay('https://api.mcsrvstat.us/' . ($edition === 'bedrock' ? 'bedrock/3/' : '3/') . $target);
    }
    fail(503, 'no backend configured');

case 'health':
    if (MC_PROVIDER === 'own' && MC_BACKEND !== '') {
        relay(rtrim(MC_BACKEND, '/') . '/health', true);
    }
    echo '{}';
    break;

default:
    fail(404, 'unknown endpoint');
}
