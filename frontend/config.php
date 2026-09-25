<?php
// SPDX-License-Identifier: AGPL-3.0-or-later
// Relay settings, read by api.php. The frontend deploy workflow overwrites
// this file from the MC_BACKEND repository variable and the release tag.
//   MC_BACKEND   base URL of the backend, "" disables checks
//   MC_VERSION   release tag shown in the footer
define('MC_BACKEND', 'http://localhost:8080');
define('MC_VERSION', 'dev');
