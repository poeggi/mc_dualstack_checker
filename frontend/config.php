<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// Backend selection, read by api.php. The release workflow overwrites this
// file from the MC_PROVIDER and MC_BACKEND repository variables.
//   MC_PROVIDER  "own" (the Go backend at MC_BACKEND), "mcsrvstat", or "" (checks disabled)
//   MC_BACKEND   base URL of the own backend
//   MC_VERSION   release tag shown in the footer
define('MC_PROVIDER', 'own');
define('MC_BACKEND', 'http://localhost:8080');
define('MC_VERSION', 'dev');
