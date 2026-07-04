#!/usr/bin/env python3
"""Tests for scripts/checks/platform-registry-drift.py."""
from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import textwrap
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "platform-registry-drift.py"
_spec = importlib.util.spec_from_file_location("platform_registry_drift", _MOD_PATH)
prd = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(prd)


PLATFORMS = ["anthropic", "gemini", "openai", "antigravity", "newapi", "kiro", "grok"]
QUOTA_PLATFORMS = ["anthropic", "openai", "gemini", "antigravity", "grok"]
ACCOUNT_TYPES = ["oauth", "setup-token", "apikey", "upstream", "bedrock", "service_account"]
ROLES = ["admin", "user"]
REDEEM_TYPES = ["balance", "concurrency", "subscription", "invitation"]
SUBSCRIPTION_TYPES = ["standard", "subscription"]
SUBSCRIPTION_STATUSES = ["active", "expired", "suspended", "revoked"]


def _write(root: pathlib.Path, rel: str, text: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(text).lstrip(), encoding="utf-8")


def _ts_array(values: list[str]) -> str:
    return "[" + ", ".join(repr(v) for v in values) + "]"


def _ts_record(values: list[str]) -> str:
    lines = [f"  {value}: 'class-{value}'," for value in values]
    return "{\n" + "\n".join(lines) + "\n}"


def _ent_platform_field(values: list[str] | None = None) -> str:
    if values is None:
        return """
            field.String("platform").
                MaxLen(50).
                NotEmpty(),
        """
    quoted = ", ".join(f'"{v}"' for v in values)
    return f"""
        field.String("platform").
            Values({quoted}).
            NotEmpty(),
    """


def _fixture(
    root: pathlib.Path,
    *,
    ent_values: list[str] | None = None,
    soft_badge: list[str] | None = None,
    label_text: list[str] | None = None,
) -> None:
    _write(
        root,
        "backend/internal/domain/constants.go",
        """
        package domain

        const (
            PlatformAnthropic = "anthropic"
            PlatformGemini = "gemini"
            PlatformOpenAI = "openai"
            PlatformAntigravity = "antigravity"
            PlatformNewAPI = "newapi"
            PlatformKiro = "kiro"
            PlatformGrok = "grok"
        )

        const (
            AccountTypeOAuth          = "oauth"
            AccountTypeSetupToken     = "setup-token"
            AccountTypeAPIKey         = "apikey"
            AccountTypeUpstream       = "upstream"
            AccountTypeBedrock        = "bedrock"
            AccountTypeServiceAccount = "service_account"
        )

        const (
            RoleAdmin = "admin"
            RoleUser  = "user"
        )

        const (
            RedeemTypeBalance      = "balance"
            RedeemTypeConcurrency  = "concurrency"
            RedeemTypeSubscription = "subscription"
            RedeemTypeInvitation   = "invitation"
        )

        const (
            SubscriptionTypeStandard     = "standard"
            SubscriptionTypeSubscription = "subscription"
        )

        const (
            SubscriptionStatusActive    = "active"
            SubscriptionStatusExpired   = "expired"
            SubscriptionStatusSuspended = "suspended"
            SubscriptionStatusRevoked   = "revoked"
        )
        """,
    )
    _write(
        root,
        "backend/internal/engine/provider.go",
        """
        package engine

        import "github.com/Wei-Shaw/sub2api/internal/domain"

        func OpenAICompatPlatforms() []string {
            return []string{domain.PlatformOpenAI, domain.PlatformNewAPI, domain.PlatformGrok}
        }

        func AllSchedulingPlatforms() []string {
            return []string{
                domain.PlatformAnthropic,
                domain.PlatformGemini,
                domain.PlatformOpenAI,
                domain.PlatformAntigravity,
                domain.PlatformNewAPI,
                domain.PlatformKiro,
                domain.PlatformGrok,
            }
        }
        """,
    )
    _write(
        root,
        "backend/internal/service/openai_messages_dispatch_tk_newapi.go",
        """
        package service

        import (
            "github.com/Wei-Shaw/sub2api/internal/domain"
            "github.com/Wei-Shaw/sub2api/internal/engine"
        )

        type Group struct{ Platform string }

        func tkGroupKeepsDispatchConfig(g Group) bool {
            if engine.IsOpenAICompatPlatform(g.Platform) {
                return true
            }
            return g.Platform == domain.PlatformGemini
        }
        """,
    )
    _write(
        root,
        "backend/ent/schema/account.go",
        f"""
        package schema

        import "entgo.io/ent/schema/field"

        func fields() {{
            {_ent_platform_field(ent_values)}
            field.String("type").NotEmpty()
        }}
        """,
    )
    _write(
        root,
        "frontend/src/types/index.ts",
        """
        export interface User {
          id: number
          role: 'admin' | 'user'
          status: 'active' | 'disabled'
        }

        export type GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'newapi' | 'kiro' | 'grok'

        export type SubscriptionType = 'standard' | 'subscription'

        export type AccountPlatform =
          | 'anthropic'
          | 'gemini'
          | 'openai'
          | 'antigravity'
          | 'newapi'
          | 'kiro'
          | 'grok'

        export type AccountType = 'oauth' | 'setup-token' | 'apikey' | 'upstream' | 'bedrock' | 'service_account'

        export type RedeemCodeType = 'balance' | 'concurrency' | 'subscription' | 'invitation'

        export interface UserSubscription {
          id: number
          status: 'active' | 'expired' | 'revoked' | 'suspended'
        }
        """,
    )
    _write(
        root,
        "backend/internal/service/domain_constants.go",
        """
        package service

        var AllowedQuotaPlatforms = []string{
            PlatformAnthropic,
            PlatformOpenAI,
            PlatformGemini,
            PlatformAntigravity,
            PlatformGrok,
        }
        """,
    )
    _write(
        root,
        "frontend/src/constants/gatewayPlatforms.ts",
        f"""
        export const OPENAI_COMPAT_PLATFORMS = {_ts_array(["openai", "newapi", "grok"])} as const
        export const GROUP_DISPATCH_CONFIG_PLATFORMS = {_ts_array(["openai", "newapi", "gemini", "grok"])} as const
        export const ALLOWED_QUOTA_PLATFORMS = {_ts_array(QUOTA_PLATFORMS)} as const

        const SOFT_BADGE: Record<string, string> = {_ts_record(soft_badge or PLATFORMS)}

        const LABEL_TEXT: Record<string, string> = {_ts_record(label_text or PLATFORMS)}
        """,
    )


class PlatformRegistryDriftTest(unittest.TestCase):
    def test_free_ent_string_and_complete_style_maps_pass(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)

            failures, ok_lines = prd.run(root)

        self.assertEqual(failures, [])
        self.assertTrue(any("free string" in line for line in ok_lines))
        self.assertTrue(any("SOFT_BADGE style map covers" in line for line in ok_lines))
        self.assertTrue(any("LABEL_TEXT style map covers" in line for line in ok_lines))

    def test_ent_enum_missing_scheduling_platform_fails(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root, ent_values=[p for p in PLATFORMS if p != "grok"])

            failures, _ = prd.run(root)

        self.assertEqual(len(failures), 1)
        self.assertIn("ent schema platform enum", failures[0][0])
        self.assertIn("missing: grok", "\n".join(failures[0]))

    def test_style_maps_missing_scheduling_platforms_fail(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(
                root,
                soft_badge=[p for p in PLATFORMS if p != "grok"],
                label_text=[p for p in PLATFORMS if p != "kiro"],
            )

            failures, _ = prd.run(root)

        rendered = "\n\n".join("\n".join(fail) for fail in failures)
        self.assertEqual(len(failures), 2)
        self.assertIn("frontend SOFT_BADGE style map", rendered)
        self.assertIn("missing: grok", rendered)
        self.assertIn("frontend LABEL_TEXT style map", rendered)
        self.assertIn("missing: kiro", rendered)

    def test_all_new_checks_pass(self) -> None:
        """All 11 checks pass with a complete, aligned fixture."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)

            failures, ok_lines = prd.run(root)

        self.assertEqual(failures, [])
        # Verify new checks produced ok lines
        ok_text = "\n".join(ok_lines)
        self.assertIn("Role constant universe in lockstep", ok_text)
        self.assertIn("RedeemType constant universe in lockstep", ok_text)
        self.assertIn("SubscriptionType constant universe in lockstep", ok_text)
        self.assertIn("SubscriptionStatus constant universe in lockstep", ok_text)
        self.assertIn("GroupPlatform constant universe in lockstep", ok_text)

    def test_role_drift_detected(self) -> None:
        """CHECK 7: missing role on TS side is caught."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)
            # Overwrite TS to drop 'user' from User.role
            _write(
                root,
                "frontend/src/types/index.ts",
                """
                export interface User {
                  id: number
                  role: 'admin'
                  status: 'active' | 'disabled'
                }
                export type GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type SubscriptionType = 'standard' | 'subscription'
                export type AccountPlatform = 'anthropic' | 'gemini' | 'openai' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type AccountType = 'oauth' | 'setup-token' | 'apikey' | 'upstream' | 'bedrock' | 'service_account'
                export type RedeemCodeType = 'balance' | 'concurrency' | 'subscription' | 'invitation'
                export interface UserSubscription {
                  id: number
                  status: 'active' | 'expired' | 'revoked' | 'suspended'
                }
                """,
            )

            failures, _ = prd.run(root)

        role_failures = [f for f in failures if "Role constant" in f[0]]
        self.assertEqual(len(role_failures), 1)
        self.assertIn("only in backend", "\n".join(role_failures[0]))
        self.assertIn("user", "\n".join(role_failures[0]))

    def test_redeem_type_drift_detected(self) -> None:
        """CHECK 8: extra value on TS side is caught."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)
            # Overwrite TS to add an extra redeem type
            _write(
                root,
                "frontend/src/types/index.ts",
                """
                export interface User {
                  id: number
                  role: 'admin' | 'user'
                  status: 'active' | 'disabled'
                }
                export type GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type SubscriptionType = 'standard' | 'subscription'
                export type AccountPlatform = 'anthropic' | 'gemini' | 'openai' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type AccountType = 'oauth' | 'setup-token' | 'apikey' | 'upstream' | 'bedrock' | 'service_account'
                export type RedeemCodeType = 'balance' | 'concurrency' | 'subscription' | 'invitation' | 'bogus'
                export interface UserSubscription {
                  id: number
                  status: 'active' | 'expired' | 'revoked' | 'suspended'
                }
                """,
            )

            failures, _ = prd.run(root)

        redeem_failures = [f for f in failures if "RedeemType constant" in f[0]]
        self.assertEqual(len(redeem_failures), 1)
        self.assertIn("only in frontend", "\n".join(redeem_failures[0]))
        self.assertIn("bogus", "\n".join(redeem_failures[0]))

    def test_subscription_status_drift_detected(self) -> None:
        """CHECK 10: missing subscription status on TS side is caught."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)
            # Overwrite TS to drop 'suspended' from UserSubscription.status
            _write(
                root,
                "frontend/src/types/index.ts",
                """
                export interface User {
                  id: number
                  role: 'admin' | 'user'
                  status: 'active' | 'disabled'
                }
                export type GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type SubscriptionType = 'standard' | 'subscription'
                export type AccountPlatform = 'anthropic' | 'gemini' | 'openai' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type AccountType = 'oauth' | 'setup-token' | 'apikey' | 'upstream' | 'bedrock' | 'service_account'
                export type RedeemCodeType = 'balance' | 'concurrency' | 'subscription' | 'invitation'
                export interface UserSubscription {
                  id: number
                  status: 'active' | 'expired' | 'revoked'
                }
                """,
            )

            failures, _ = prd.run(root)

        sub_failures = [f for f in failures if "SubscriptionStatus constant" in f[0]]
        self.assertEqual(len(sub_failures), 1)
        self.assertIn("only in backend", "\n".join(sub_failures[0]))
        self.assertIn("suspended", "\n".join(sub_failures[0]))

    def test_group_platform_drift_detected(self) -> None:
        """CHECK 11: GroupPlatform missing a platform is caught."""
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            _fixture(root)
            # Overwrite TS to make GroupPlatform a stale subset (missing 'grok')
            _write(
                root,
                "frontend/src/types/index.ts",
                """
                export interface User {
                  id: number
                  role: 'admin' | 'user'
                  status: 'active' | 'disabled'
                }
                export type GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'newapi' | 'kiro'
                export type SubscriptionType = 'standard' | 'subscription'
                export type AccountPlatform = 'anthropic' | 'gemini' | 'openai' | 'antigravity' | 'newapi' | 'kiro' | 'grok'
                export type AccountType = 'oauth' | 'setup-token' | 'apikey' | 'upstream' | 'bedrock' | 'service_account'
                export type RedeemCodeType = 'balance' | 'concurrency' | 'subscription' | 'invitation'
                export interface UserSubscription {
                  id: number
                  status: 'active' | 'expired' | 'revoked' | 'suspended'
                }
                """,
            )

            failures, _ = prd.run(root)

        gp_failures = [f for f in failures if "GroupPlatform constant" in f[0]]
        self.assertEqual(len(gp_failures), 1)
        self.assertIn("only in backend", "\n".join(gp_failures[0]))
        self.assertIn("grok", "\n".join(gp_failures[0]))


if __name__ == "__main__":
    unittest.main()
