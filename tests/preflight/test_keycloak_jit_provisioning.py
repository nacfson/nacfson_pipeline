#!/usr/bin/env python3
"""
Config-as-code checks for AUTH-01 Google SSO just-in-time (JIT) user provisioning.

Keycloak 24.0.5 ignores IdentityProviderRepresentation.updateProfileFirstLoginMode
and runs the IdP's firstBrokerLoginFlowAlias. The built-in "first broker login"
flow is interactive (Review Profile = MISSING, Handle Existing Account /
Confirm Link / email-or-password re-auth). Combined with VERIFY_PROFILE (on by
default, firstName/lastName required) and registrationAllowed=false, a first
Google login does not create a usable account until an operator links it.

This test locks the GitOps realm JSON to a non-interactive JIT flow.

Before (the bug):
  firstBrokerLoginFlowAlias = "first broker login"
  no authenticationFlows / authenticatorConfig in the import
  no syncMode, no linkOnly, VERIFY_PROFILE left at Keycloak 24 default (enabled)

After (this change):
  firstBrokerLoginFlowAlias = "google jit first broker login"
  Review Profile authenticator config = off
  Create User If Unique = REQUIRED (no email auto-link)
  VERIFY_PROFILE disabled; Google emails trusted via trustEmail
"""

from __future__ import annotations

import json
import os
import unittest

import yaml

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
REALM_CONFIG = os.path.join(
    BASE_DIR, "deploy", "platform", "identity", "keycloak-realm-config.yaml"
)

JIT_FLOW_ALIAS = "google jit first broker login"
FORBIDDEN_LINK_AUTHENTICATORS = {
    "idp-auto-link",
    "idp-confirm-link",
    "idp-email-verification",
    "idp-username-password-form",
    "idp-detect-existing-broker-user",
}


def load_platform_realm() -> dict:
    with open(REALM_CONFIG, encoding="utf-8") as fh:
        docs = list(yaml.safe_load_all(fh))
    configmap = next(
        d
        for d in docs
        if d
        and d.get("kind") == "ConfigMap"
        and d.get("metadata", {}).get("name") == "keycloak-realm-config"
    )
    raw = configmap["data"]["platform-realm.json"]
    return json.loads(raw)


class TestKeycloakGoogleJitProvisioning(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.realm = load_platform_realm()

    def test_realm_json_is_object(self) -> None:
        self.assertEqual(self.realm.get("realm"), "platform")
        self.assertTrue(self.realm.get("enabled"))

    def test_self_registration_stays_closed(self) -> None:
        self.assertFalse(self.realm.get("registrationAllowed"))
        self.assertFalse(self.realm.get("resetPasswordAllowed"))

    def test_google_idp_trusts_verified_google_email(self) -> None:
        google = self._google_idp()
        self.assertTrue(google.get("trustEmail"))
        self.assertTrue(google.get("enabled"))
        self.assertFalse(google.get("linkOnly"))
        config = google.get("config") or {}
        self.assertEqual(config.get("syncMode"), "IMPORT")
        self.assertEqual(config.get("defaultScope"), "openid profile email")
        self.assertEqual(config.get("clientId"), "${GOOGLE_CLIENT_ID}")
        self.assertEqual(config.get("clientSecret"), "${GOOGLE_CLIENT_SECRET}")

    def test_google_idp_uses_custom_jit_first_broker_login_flow(self) -> None:
        google = self._google_idp()
        self.assertEqual(google.get("firstBrokerLoginFlowAlias"), JIT_FLOW_ALIAS)
        self.assertEqual(self.realm.get("firstBrokerLoginFlow"), JIT_FLOW_ALIAS)
        self.assertNotEqual(
            google.get("firstBrokerLoginFlowAlias"),
            "first broker login",
            "built-in first broker login is interactive and must not be the Google IdP flow",
        )

    def test_jit_flow_creates_user_if_unique_as_required(self) -> None:
        flow = self._jit_flow()
        self.assertTrue(flow.get("topLevel"))
        self.assertFalse(flow.get("builtIn"))
        executions = flow.get("authenticationExecutions") or []
        create = self._execution(executions, "idp-create-user-if-unique")
        self.assertEqual(create.get("requirement"), "REQUIRED")
        self.assertEqual(
            create.get("authenticatorConfig"), "google-jit-create-unique-user"
        )

        configs = {c["alias"]: c for c in self.realm.get("authenticatorConfig") or []}
        create_cfg = configs["google-jit-create-unique-user"]["config"]
        self.assertEqual(
            create_cfg.get("require.password.update.after.registration"), "false"
        )

    def test_jit_flow_review_profile_is_off(self) -> None:
        flow = self._jit_flow()
        executions = flow.get("authenticationExecutions") or []
        review = self._execution(executions, "idp-review-profile")
        self.assertEqual(review.get("requirement"), "REQUIRED")
        self.assertEqual(
            review.get("authenticatorConfig"), "google-jit-review-profile"
        )

        configs = {c["alias"]: c for c in self.realm.get("authenticatorConfig") or []}
        review_cfg = configs["google-jit-review-profile"]["config"]
        self.assertEqual(review_cfg.get("update.profile.on.first.login"), "off")

    def test_jit_flow_does_not_auto_link_by_email(self) -> None:
        flow = self._jit_flow()
        authenticators = {
            (exe.get("authenticator") or "")
            for exe in flow.get("authenticationExecutions") or []
        }
        overlap = authenticators & FORBIDDEN_LINK_AUTHENTICATORS
        self.assertEqual(
            overlap,
            set(),
            "JIT flow must not auto-link or prompt to link existing accounts by email",
        )

    def test_verify_profile_required_action_is_disabled(self) -> None:
        actions = {
            a["alias"]: a for a in self.realm.get("requiredActions") or []
        }
        self.assertIn("VERIFY_PROFILE", actions)
        self.assertFalse(
            actions["VERIFY_PROFILE"].get("enabled"),
            "Keycloak 24 VERIFY_PROFILE blocks token grant when firstName/lastName are empty",
        )
        self.assertFalse(actions["VERIFY_PROFILE"].get("defaultAction"))

    def test_google_claim_mappers_are_declared(self) -> None:
        mappers = self.realm.get("identityProviderMappers") or []
        by_name = {m["name"]: m for m in mappers}
        self.assertIn("google-username-from-sub", by_name)
        self.assertIn("google-email", by_name)
        self.assertIn("google-given-name", by_name)
        self.assertIn("google-family-name", by_name)

        username = by_name["google-username-from-sub"]
        self.assertEqual(username.get("identityProviderAlias"), "google")
        self.assertEqual(
            username.get("identityProviderMapper"), "oidc-username-idp-mapper"
        )
        self.assertEqual(
            (username.get("config") or {}).get("template"),
            "${ALIAS}.${CLAIM.sub}",
        )

        email = by_name["google-email"]
        self.assertEqual((email.get("config") or {}).get("claim"), "email")
        self.assertEqual((email.get("config") or {}).get("user.attribute"), "email")

    def test_client_secrets_remain_placeholders(self) -> None:
        clients = {c["clientId"]: c for c in self.realm.get("clients") or []}
        self.assertEqual(
            clients["gateway-client"]["secret"], "${GATEWAY_CLIENT_SECRET}"
        )
        self.assertEqual(
            clients["project-pn"]["secret"], "${PROJECT_PN_CLIENT_SECRET}"
        )
        self.assertEqual(
            clients["session-revocation-client"]["secret"],
            "${SESSION_REVOCATION_SECRET}",
        )

    def _google_idp(self) -> dict:
        providers = self.realm.get("identityProviders") or []
        google = next((p for p in providers if p.get("alias") == "google"), None)
        self.assertIsNotNone(google, "Google identity provider missing from realm JSON")
        return google

    def _jit_flow(self) -> dict:
        flows = self.realm.get("authenticationFlows") or []
        flow = next((f for f in flows if f.get("alias") == JIT_FLOW_ALIAS), None)
        self.assertIsNotNone(flow, f"authenticationFlows missing alias {JIT_FLOW_ALIAS}")
        return flow

    def _execution(self, executions: list, authenticator: str) -> dict:
        exe = next(
            (e for e in executions if e.get("authenticator") == authenticator), None
        )
        self.assertIsNotNone(exe, f"JIT flow missing authenticator {authenticator}")
        return exe


if __name__ == "__main__":
    unittest.main()
