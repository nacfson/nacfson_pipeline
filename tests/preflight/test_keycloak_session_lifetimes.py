#!/usr/bin/env python3
"""
Config-as-code checks for platform session lifetimes (GitHub issue #93).

The realm import used to set no lifespans, so the live realm ran on Keycloak 24
defaults (SSO Session Idle 30 min). The gateway refreshes only when a protected
API call arrives after the 5-minute access token expires, so a visitor who made
no such call for 30 minutes was signed out and sent back to Google.

This test locks the realm JSON to the values in
contracts/CONTRACT_AUTH_DOOR.md §5. Startup import skips an existing realm, so
the live realm is updated separately (docs/operations-runbook.md §7).
"""

from __future__ import annotations

import json
import os
import re
import unittest

import yaml

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
REALM_CONFIG = os.path.join(
    BASE_DIR, "deploy", "platform", "identity", "keycloak-realm-config.yaml"
)
AUTH_CONTRACT = os.path.join(BASE_DIR, "contracts", "CONTRACT_AUTH_DOOR.md")

EXPECTED = {
    "accessTokenLifespan": 300,
    "ssoSessionIdleTimeout": 3600,
    "ssoSessionMaxLifespan": 36000,
    "revokeRefreshToken": False,
}


def load_realm() -> dict:
    with open(REALM_CONFIG, "r", encoding="utf-8") as f:
        configmap = yaml.safe_load(f)
    return json.loads(configmap["data"]["platform-realm.json"])


class TestKeycloakSessionLifetimes(unittest.TestCase):
    def test_realm_declares_contracted_lifetimes(self):
        realm = load_realm()
        for key, value in EXPECTED.items():
            with self.subTest(setting=key):
                self.assertIn(key, realm, f"realm import must declare {key}")
                self.assertEqual(realm[key], value)

    def test_idle_outlives_access_token(self):
        realm = load_realm()
        self.assertGreater(realm["ssoSessionIdleTimeout"], realm["accessTokenLifespan"])
        self.assertGreaterEqual(realm["ssoSessionMaxLifespan"], realm["ssoSessionIdleTimeout"])

    def test_no_client_shortens_the_session(self):
        realm = load_realm()
        for client in realm.get("clients", []):
            attrs = client.get("attributes", {})
            for key in ("client.session.idle.timeout", "client.session.max.lifespan"):
                with self.subTest(client=client["clientId"], attribute=key):
                    self.assertNotIn(key, attrs)

    def test_contract_names_the_same_values(self):
        with open(AUTH_CONTRACT, "r", encoding="utf-8") as f:
            contract = f.read()
        section = contract.split("## 5. Session Lifetimes", 1)
        self.assertEqual(len(section), 2, "CONTRACT_AUTH_DOOR.md must have §5 Session Lifetimes")
        body = section[1]
        for key, value in EXPECTED.items():
            with self.subTest(setting=key):
                expected = str(value).lower()
                self.assertRegex(body, rf"`{key}`\s*\|\s*`{re.escape(expected)}`")


if __name__ == "__main__":
    unittest.main()
