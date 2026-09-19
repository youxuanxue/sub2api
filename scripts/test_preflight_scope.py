#!/usr/bin/env python3
"""Behavior checks for preflight selection, Git scopes and deduplicated suites."""
from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts/preflight' / f'{name}.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


plan = load('plan')
discover = load('discover')
BILL = 'ops/observability/probe-user-billing-watch.sh'


class SelectionTest(unittest.TestCase):
    def setUp(self):
        self.data = plan.load_manifest(ROOT)

    def choose(self, *paths, scope='staged'):
        return plan.select(self.data, set(paths), scope)

    def test_billing_script_selects_its_test_and_sql_guards_without_unrelated_jobs(self):
        selected = self.choose(BILL)
        self.assertFalse(selected['full'])
        for gate in ('user billing watch probe', 'ops/deploy SQL soft-delete filter', 'script ref existence', 'ops tool orphan check'):
            self.assertIn(gate, selected['gates'])
        for gate in ('backend lint', 'observability test suite', 'stage0 test suite', 'scripts test suite', 'QA Bundle service/worker contract', 'Caddyfile syntax gate', 'ops/anthropic orchestrators unittest', 'generated model-surface bundle drift'):
            self.assertNotIn(gate, selected['gates'])

    def test_shared_observability_helper_keeps_directory_and_consumers(self):
        selected = self.choose('ops/observability/run-probe.sh')
        for gate in ('observability test suite', 'user billing watch probe', 'SSM host command script parse', 'run-probe.sh --env loop regression guard', 'determinism-baseline helper unittests'):
            self.assertIn(gate, selected['gates'])

    def test_mixed_changes_do_not_get_billing_only_exemption(self):
        selected = self.choose(BILL, 'ops/observability/edge_health_verdict.py')
        self.assertIn('observability test suite', selected['gates'])
        self.assertIn('edge-health verdict selftest', selected['gates'])

    def test_frontend_change_skips_ops_and_go_artifact_jobs(self):
        selected = self.choose('frontend/src/views/HomeView.vue')
        self.assertFalse(selected['full'])
        self.assertIn('frontend release asset contract', selected['gates'])
        for gate in ('backend lint', 'QA Bundle service/worker contract', 'Caddyfile syntax gate', 'generated model-surface bundle drift'):
            self.assertNotIn(gate, selected['gates'])

    def test_unknown_and_gate_machinery_changes_fall_back_to_full(self):
        for path in ('unknown/new.file', 'scripts/preflight.sh', '.preflight/gates.json', 'scripts/preflight/plan.py', 'dev-rules', 'backend/go.mod'):
            with self.subTest(path=path):
                selected = self.choose(path)
                self.assertTrue(selected['full'])
                self.assertEqual(set(selected['gates']), set(self.data['gates']))

    def test_empty_and_explicit_full_never_silently_pass_without_checks(self):
        for selected in (self.choose(), self.choose(BILL, scope='full')):
            self.assertTrue(selected['full'])
            self.assertEqual(set(selected['gates']), set(self.data['gates']))

    def test_new_unclassified_gate_defaults_to_running(self):
        self.data['gates']['new gate'] = []
        self.assertIn('new gate', self.choose(BILL)['gates'])

    def test_archive_launch_dependency_is_selected(self):
        selected = self.choose('ops/archive/data_layer_archive_cleanup_hold.py')
        self.assertIn('nonprod archive/restore rehearsal', selected['gates'])
        self.assertIn('QA Phase 1 edge baseline probe', selected['gates'])

    def test_sentinel_paths_come_from_the_registry(self):
        gate = 'gateway TK sentinel registry'
        path = next(p for p in self.data['gates'][gate] if p.startswith('ops/'))
        self.assertIn(gate, self.choose(path)['gates'])
        self.assertNotIn(gate, self.choose(BILL)['gates'])

    def test_every_guard_has_manifest_entry_and_background_owner(self):
        import re
        script = (ROOT / 'scripts/preflight.sh').read_text()
        guarded = re.findall(r"^if _preflight_selected '(.+)'; then$", script, re.M)
        self.assertFalse(set(guarded) - set(self.data['gates']))
        for key, gate in self.data['backgrounds'].items():
            self.assertIn(gate, self.data['gates'], key)


class GitScopeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.clean_env = patch.dict(os.environ, {k: v for k,v in os.environ.items() if not k.startswith('GIT_') and k not in ('CI','GITHUB_ACTIONS','GITHUB_EVENT_NAME')}, clear=True)
        self.clean_env.start()
        self.addCleanup(self.clean_env.stop)
        self.git('init', '-q')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.git('config', 'user.name', 'Fixture')
        self.write('old.txt', 'base\n')
        self.git('add', '.')
        self.git('commit', '-qm', 'base')
        self.git('branch', 'base')

    def git(self, *args):
        return plan.git(self.root, *args)

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def test_staged_does_not_include_prior_branch_commits(self):
        self.write('previous.py', 'old change\n')
        self.git('add', '.')
        self.git('commit', '-qm', 'previous')
        self.write(BILL, '# probe\n')
        self.git('add', BILL)
        self.assertEqual(plan.changed_paths(self.root, 'auto', 'base'), ('staged', {BILL}))
        self.assertEqual(plan.changed_paths(self.root, 'branch', 'base')[1], {BILL, 'previous.py'})

    def test_worktree_includes_staged_unstaged_and_untracked(self):
        self.write('old.txt', 'edit\n')
        self.write('staged.py', 'staged\n')
        self.git('add', 'staged.py')
        self.write('new.py', 'new\n')
        self.assertEqual(plan.changed_paths(self.root, 'worktree', 'base')[1], {'old.txt','staged.py','new.py'})

    def test_partial_staging_fails_instead_of_testing_other_contents(self):
        self.write('old.txt', 'staged\n')
        self.git('add', 'old.txt')
        self.write('old.txt', 'different\n')
        with self.assertRaisesRegex(ValueError, 'differ from the tested'):
            plan.changed_paths(self.root, 'staged', 'base')

    def test_rename_selects_both_owners(self):
        self.git('mv', 'old.txt', 'new.txt')
        self.assertEqual(plan.changed_paths(self.root, 'staged', 'base')[1], {'old.txt','new.txt'})

    def test_missing_base_is_an_error_not_an_empty_diff(self):
        with self.assertRaises(subprocess.CalledProcessError):
            plan.changed_paths(self.root, 'branch', 'missing-base')

    def test_alternate_commit_index_is_respected(self):
        alternate = self.root / '.git/alternate-index'
        self.write('real-index.txt', 'real\n')
        self.git('add', 'real-index.txt')
        with patch.dict(os.environ, {'GIT_INDEX_FILE':str(alternate)}):
            self.git('read-tree', 'HEAD')
            self.write('alternate.txt', 'alternate\n')
            self.git('add', 'alternate.txt')
            self.assertEqual(plan.changed_paths(self.root, 'auto', 'base'), ('staged', {'alternate.txt'}))

    def test_ci_pr_uses_branch_and_main_uses_full(self):
        with patch.dict(os.environ, {'CI':'true','GITHUB_EVENT_NAME':'pull_request'}):
            self.assertEqual(plan.changed_paths(self.root,'auto','base')[0], 'branch')
        with patch.dict(os.environ, {'CI':'true','GITHUB_EVENT_NAME':'push'}):
            self.assertEqual(plan.changed_paths(self.root,'auto','base')[0], 'full')


class DiscoverTest(unittest.TestCase):
    def test_focused_module_is_excluded_but_other_failures_still_propagate(self):
        events = []
        class Focused(unittest.TestCase):
            __module__ = 'test_probe_user_billing_watch'
            def runTest(self): events.append('duplicate')
        class Other(unittest.TestCase):
            def runTest(self):
                events.append('other')
                self.fail('intentional fixture failure')
        suite = unittest.TestSuite([unittest.TestSuite([Focused(),Other()])])
        result = unittest.TestResult()
        discover.without_focused(suite, {'test_probe_user_billing_watch'}).run(result)
        self.assertEqual(events, ['other'])
        self.assertFalse(result.wasSuccessful())
        self.assertEqual(result.testsRun, 1)


class ShellDispatchTest(unittest.TestCase):
    def test_unselected_background_never_starts_and_unknown_job_runs(self):
        from scripts.test_preflight_background_output import _shell_function
        import shlex
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'selected').write_text('user billing watch probe\n')
            (root / 'registered').write_text('bg:qa_bundle\nuser billing watch probe\n')
            shell = '\n'.join([
                'set -u', f'_preflight_plan_dir={shlex.quote(tmp)}',
                '_preflight_bg_dir="$_preflight_plan_dir"',
                'PREFLIGHT_SELECTION_FILE="$_preflight_plan_dir/selected"',
                _shell_function('_preflight_selected'), _shell_function('_bg_spawn'),
                '_bg_spawn qa_bundle touch "$_preflight_plan_dir/should-not-run"',
                '_bg_spawn future_job touch "$_preflight_plan_dir/unknown-ran"',
                'wait',
            ])
            result = subprocess.run(['bash','-c',shell],capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertFalse((root/'should-not-run').exists())
            self.assertFalse((root/'qa_bundle.pid').exists())
            self.assertTrue((root/'unknown-ran').exists())


class ReporterTest(unittest.TestCase):
    def test_reporter_preserves_nonzero_exit_and_hides_success_noise(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            entry = root/'scripts/preflight/report.py'
            entry.parent.mkdir(parents=True)
            entry.write_text((ROOT/'scripts/preflight/report.py').read_text())
            (root/'scripts/preflight.sh').write_text('''#!/bin/bash
printf '=== sub2api: first ===\\nverbose success detail\\n=== sub2api: second ===\\nFAIL: fixture failure\\n'
exit 7
''')
            result = subprocess.run(['python3',str(entry)],capture_output=True,text=True)
            self.assertEqual(result.returncode,7,result.stderr)
            self.assertIn('PASS first',result.stdout)
            self.assertNotIn('verbose success detail',result.stdout)
            self.assertIn('FAIL: second',result.stdout)
            self.assertIn('fixture failure',result.stdout)


class SyntaxTest(unittest.TestCase):
    def test_syntax_errors_fail_without_executing_scripts(self):
        syntax = load('syntax')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'bad.sh').write_text('if then\n')
            (root/'bad.py').write_text('def broken(:\n')
            (root/'good.sh').write_text('touch should-not-exist\n')
            self.assertEqual(syntax.check(root,['good.sh','deleted.sh']),0)
            self.assertFalse((root/'should-not-exist').exists())
            import contextlib, io
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(syntax.check(root,['bad.sh','bad.py']),1)


if __name__ == '__main__':
    unittest.main()
