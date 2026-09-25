<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// The page. Its settings and this host's last copy of the backend's health
// go into the page itself, so opening it needs no further request. An older
// copy is refreshed after the page has gone out.
require __DIR__ . '/common.php';

$kept = kept_health();
$health = $kept !== null && $kept['body'] !== null ? json_decode($kept['body'], true) : null;
$up = is_array($health) && !empty($health['ok']);
$footer = MC_VERSION;
if (MC_BACKEND !== '' && $kept !== null) {
    $footer .= $up ? ', API ' . ($health['version'] ?? '') : ', API unavailable';
}
$noIPv6 = $up && ($health['ipv6'] ?? true) === false;

function h(string $s): string { return htmlspecialchars($s, ENT_QUOTES); }

ob_start();
?>
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Minecraft Server Dualstack Checker - IPv4 &amp; IPv6 Status</title>
    <meta name="description" content="Check the online status of any Minecraft server over both IPv4 and IPv6. Supports Bedrock and Java editions with automatic port fallback.">
    <meta name="keywords" content="Minecraft server status, Minecraft dualstack server checker, IPv4 IPv6 dual stack, Bedrock server status, Java server status, MC server ping, Minecraft online checker">
    <link rel="stylesheet" href="mc_dualstack_check.css">
    <link rel="icon" type="image/svg+xml" href="favicon.svg">
    <link rel="apple-touch-icon" href="apple-touch-icon.png">
    <script src="mc_dualstack_check.js" defer></script>
</head>
<body data-api="<?= h(rtrim(MC_BACKEND, '/')) ?>" data-version="<?= h(MC_VERSION) ?>"><main>
<div class="page">

    <header class="header">
        <img src="favicon.svg" width="16" height="16" alt="" aria-hidden="true">
        <h1 class="text-title">MC Server Dualstack Check</h1>
    </header>

    <div class="card">
        <div class="card-body">
            <form class="form" method="get" id="mcform" autocomplete="on">
                <div class="form-row">
                    <div class="field field-wide">
                        <label class="text-label text-strong" for="f-host">HOSTNAME | IP ADDRESS</label>
                        <input name="host" id="f-host" placeholder="e.g. play.example.com" autocapitalize="none" autocorrect="off" spellcheck="false">
                    </div>
                    <div class="field">
                        <label class="text-label text-strong" for="f-port4">IPv4 PORT</label>
                        <input name="port4" id="f-port4" placeholder="19132" inputmode="numeric">
                    </div>
                    <div class="field">
                        <label class="text-label text-strong" for="f-port6">IPv6 PORT</label>
                        <input name="port6" id="f-port6" placeholder="as IPv4" inputmode="numeric">
                    </div>
                    <div class="field">
                        <label class="text-label text-strong" for="f-edition">EDITION</label>
                        <select name="edition" id="f-edition">
                            <option value="bedrock" selected>Bedrock</option>
                            <option value="java">Java</option>
                        </select>
                    </div>
                </div>
                <div class="form-actions">
                    <label class="check-label" title="If enabled, skips automatic retry on fallback ports.">
                        <input type="checkbox" id="f-nofallback" name="nofallback" value="1">
                        Disable port fallback
                    </label>
                    <button class="text-strong" type="submit" id="f-submit">Check</button>
                </div>
            </form>
        </div>
    </div>

<?php if ($noIPv6): ?>
    <div class="notice-warn" id="notice-backend" role="status">&#9888; The checker backend has no IPv6 connectivity. IPv6 results are not meaningful.</div>
<?php else: ?>
    <div class="notice-warn" id="notice-backend" role="status" hidden></div>
<?php endif ?>
    <div class="notice-error" id="notice" role="alert" hidden></div>

    <div class="checking-indicator text-muted" id="checking-indicator" hidden>
        <span class="checking-spinner"></span>
        <span class="checking-label">Checking&hellip;</span>
    </div>

</div><!-- /.page -->

<div class="page" id="results" hidden>
    <div class="query-time text-muted" id="query-time">
        <span id="time-ago"></span>
    </div>
    <div class="ip-grid" id="ip-grid"></div>
    <div class="card debug-card" id="debug-card" hidden>
        <div class="card-head text-label text-caps text-strong">Failure log</div>
        <div class="debug-log text-muted" id="debug-log"></div>
    </div>
</div><!-- /results -->

<footer class="site-footer text-tiny text-muted">
    No data logged/stored beyond short-term caching or standard server access logs.<br>
    Version <span id="version"><?= h($footer) ?></span>. <a href="https://github.com/poeggi/mc_dualstack_checker">AGPL-3.0</a>.
</footer>
</main>
</body>
</html>
<?php
finish(200, ob_get_clean(), [
    'Content-Type: text/html; charset=utf-8',
    'X-Content-Type-Options: nosniff',
    'Cache-Control: no-cache',
]);
refresh_health($kept);
