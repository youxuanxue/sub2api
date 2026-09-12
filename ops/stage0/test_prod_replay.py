#!/usr/bin/env python3
"""Behavioral replay gates, real loopback HTTP, and orchestration regressions."""
import contextlib
import http.server
import importlib.util
import json
import os
import sqlite3
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch, MagicMock

import prod_replay as replay
import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('replay_cli', ROOT / 'scripts/stage0/replay-prod-release.py')
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


def row(user=1, model='m1', endpoint='/v1/messages'):
    return {'request_id': 'real-request-' + str(user), 'user_id': user, 'api_key_id': user, 'key_replayable': True,
            'requested_model': model, 'inbound_endpoint': endpoint, 'stream': False,
            'tool_calls_present': False, 'multimodal_present': False}


def sample(user=1, model='m1', endpoint='/v1/messages'):
    return {'row': row(user, model, endpoint), 'path': endpoint,
            'source': 'legacy', 'body': json.dumps({'model': model, 'messages': [{'role': 'user', 'content': 'private prompt'}]}).encode()}


@contextlib.contextmanager
def server(status=200, body=b'{"content":[{"text":"ok"}]}', content_type='application/json'):
    requests = []
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            raw = self.rfile.read(int(self.headers['Content-Length']))
            requests.append((self.path, raw, dict(self.headers)))
            self.send_response(status)
            self.send_header('Content-Type', content_type)
            self.send_header('Content-Length', str(len(body)))
            self.send_header('Location', 'http://example.invalid/credential-sink')
            self.end_headers()
            self.wfile.write(body)
        def log_message(self, *_):
            pass
    httpd = http.server.HTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        yield httpd.server_port, requests
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join()


class CaptureTest(unittest.TestCase):
    def test_replays_retained_body_and_rejects_fabricated_paths(self):
        payload = {'request_id': 'real-request-1', 'request': {'path': '/v1/messages', 'body': {'model': 'm1', 'messages': ['real']}}}
        result = replay.sample_from_capture(row(), payload)
        self.assertEqual(json.loads(result['body']), payload['request']['body'])
        for path in ('https://evil.invalid', '//evil.invalid/path', '/v1beta/models', '/api/v1/admin/accounts'):
            payload['request']['path'] = path
            with self.subTest(path=path), self.assertRaises(replay.ReplayError):
                replay.sample_from_capture(row(), payload)

    def test_redacted_and_truncated_bodies_fail(self):
        for body in ({'prompt': '***'}, {'_truncated': True}, {}, None, 'not JSON'):
            with self.subTest(body=body), self.assertRaises(replay.ReplayError):
                replay.sample_from_capture(row(), {'request_id': 'real-request-1', 'request': {'path': '/v1/messages', 'body': body}})

    def test_selection_tracks_missing_combinations_and_uses_next_complete_capture(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'app').mkdir()
            blob = root / 'app/a.zst'
            blob.write_bytes(b'fixture')
            rows = [dict(row(), request_id='missing-request', blob_uri='file:///app/data/missing.zst'),
                    dict(row(), blob_uri='file:///app/data/a.zst'),
                    dict(row(2), blob_uri='s3://archive/missing')]
            proc = MagicMock()
            proc.stdout.read.return_value = json.dumps({'request_id': 'real-request-1', 'request': {'path': '/v1/messages', 'body': {'model': 'm1'}}}).encode()
            proc.wait.return_value = 0
            with patch.object(replay, 'sql', return_value='\n'.join(map(json.dumps, rows))), patch.object(replay.subprocess, 'Popen', return_value=proc):
                selected, gaps, total = replay.collect(root)
            self.assertEqual(total, 2)
            self.assertEqual(len(selected), 1)
            self.assertEqual(gaps, {'capture_not_local': 1})

    def test_http200_error_capture_is_not_a_successful_baseline(self):
        payload = {'request_id': 'real-request-1', 'request': {
            'path': '/v1/messages', 'body': {'model': 'm1', 'messages': ['retained']}},
            'response': {'status_code': 200, 'body': 'data: {"error":{"message":"sensitive content"}}\n\n'}}
        with self.assertRaisesRegex(replay.ReplayError, 'baseline_upstream_error'):
            replay.sample_from_capture(row(), payload)
        payload['response']['body'] = 'data: {"type":"response.failed","response":{"error":{"code":"server_error"}}}\n\n'
        with self.assertRaisesRegex(replay.ReplayError, 'baseline_upstream_error'):
            replay.sample_from_capture(row(), payload)
        payload['response']['body'] = 'data: {"type":"message_stop"}\n\n'
        self.assertEqual(json.loads(replay.sample_from_capture(row(), payload)['body']), payload['request']['body'])

    def test_alias_is_preserved_and_missing_gemini_action_is_a_gap(self):
        payload = {'request_id': 'real-request-1', 'request': {
            'path': '/responses', 'body': {'model': 'm1', 'input': 'retained'}}}
        self.assertEqual(replay.sample_from_capture(row(endpoint='/responses'), payload)['path'], '/responses')
        payload['request']['path'] = '/v1beta/models'
        with self.assertRaisesRegex(replay.ReplayError, 'unsupported_path'):
            replay.sample_from_capture(row(), payload)
        payload['request']['path'] = '/v1/messages'
        payload['request']['body'] = '{"model":"m1","messages":['
        with self.assertRaisesRegex(replay.ReplayError, 'body_missing_or_truncated'):
            replay.sample_from_capture(row(), payload)

    def test_revoked_key_uses_same_stratum_alternative_without_reactivation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'app').mkdir()
            (root / 'app/a.zst').write_bytes(b'fixture')
            revoked = dict(row(), request_id='revoked-request', key_replayable=False, blob_uri='file:///app/data/a.zst')
            usable = dict(revoked, request_id='real-request-1', api_key_id=77, key_replayable=True)
            rows = [revoked, usable, dict(row(2), key_replayable=False)]
            proc = MagicMock()
            proc.stdout.read.return_value = json.dumps({'request_id': 'real-request-1', 'request': {
                'path': '/v1/messages', 'body': {'model': 'm1'}}}).encode()
            proc.wait.return_value = 0
            details = []
            with patch.object(replay, 'sql', return_value='\n'.join(map(json.dumps, rows))), patch.object(replay.subprocess, 'Popen', return_value=proc) as decompress:
                selected, gaps, total = replay.collect(root, details)
            self.assertEqual(total, 2)
            self.assertEqual(selected[0]['row']['api_key_id'], 77)
            self.assertEqual(gaps, {'original_key_unavailable': 1})
            self.assertEqual(details[0]['user_id'], 2)
            self.assertFalse(details[0]['passed'])
            self.assertEqual(decompress.call_count, 1)



class HTTPTest(unittest.TestCase):
    def test_real_loopback_request_preserves_body_identity_and_hides_payload(self):
        request = sample()
        with server() as (port, received):
            result = replay.execute(request, 'private-key', port, 'test-replay-1')
        self.assertTrue(result['passed'])
        self.assertEqual(received[0][1], request['body'])
        self.assertEqual(received[0][2]['Authorization'], 'Bearer private-key')
        self.assertNotIn('private', json.dumps(result))

    def test_redirect_and_upstream_errors_are_failures(self):
        for status, body in ((302, b'redirect'), (500, b'{}'), (200, b'{"error":"quota"}')):
            with self.subTest(status=status), server(status, body) as (port, received):
                self.assertFalse(replay.execute(sample(), 'secret', port, 'id')['passed'])
                self.assertEqual(len(received), 1)

    def test_stream_requires_terminal_and_rejects_error_even_after_terminal(self):
        for body, expected in ((b'data: {"type":"message_stop"}\n\n', True),
                               (b'data: {"type":"content_block_delta"}\n\n', False),
                               (b'data: [DONE]\n\ndata: {"error":"bad"}\n\n', False),
                               (b'data: not-json\n\ndata: [DONE]\n\n', False)):
            with self.subTest(body=body), server(200, body, 'text/event-stream') as (port, _):
                request = sample()
                request['row']['stream'] = True
                self.assertEqual(replay.execute(request, 'secret', port, 'id')['passed'], expected)

    def test_timeout_reports_phase_without_exception_or_credential_text(self):
        with patch.object(replay.http.client, 'HTTPConnection') as conn:
            conn.return_value.getresponse.side_effect = TimeoutError('private-key private prompt')
            result = replay.execute(sample(), 'private-key', 1234, 'replay-id')
        self.assertEqual(result['reason'], 'socket_timeout')
        self.assertEqual(result['phase'], 'response_headers')
        self.assertEqual(result['http_status'], 0)
        self.assertNotIn('private', json.dumps(result))
        self.assertGreaterEqual(conn.call_args.kwargs['timeout'], 120)

    def test_partial_http_framing_and_error_events_never_pass(self):
        # A complete JSON object is insufficient if HTTP advertises missing bytes.
        class ShortResponse(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers['Content-Length']))
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', '100')
                self.end_headers()
                self.wfile.write(b'{"content":[{"text":"ok"}]}')
            def log_message(self, *_):
                pass
        httpd = http.server.HTTPServer(('127.0.0.1', 0), ShortResponse)
        thread = threading.Thread(target=httpd.handle_request)
        thread.start()
        try:
            result = replay.execute(sample(), 'secret', httpd.server_port, 'id')
            self.assertFalse(result['passed'])
            self.assertEqual(result['reason'], 'response_truncated')
        finally:
            thread.join()
            httpd.server_close()
        self.assertEqual(replay.response_reason(200, 'text/event-stream',
            b'event: error\ndata: {}\n\ndata: [DONE]\n\n', True), 'upstream_error')
        self.assertEqual(replay.response_reason(200, 'application/json',
            b'{"status":"incomplete"}', False), 'upstream_error')

    def test_provider_error_classification_never_exposes_message_or_credentials(self):
        raw = b'data: {"error":{"code":1301,"message":"sensitive content: private prompt private-key"}}\n\n'
        details = replay.response_error_details(raw)
        self.assertEqual(details['error_message_categories'], ['content_filter'])
        self.assertEqual(details['error_numeric_code'], 1301)
        self.assertNotIn('private', json.dumps(details))
        incomplete = replay.response_error_details(
            b'data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}\n\n')
        self.assertEqual(incomplete['error_event_type'], 'response.incomplete')
        self.assertEqual(incomplete['incomplete_reason'], 'max_output_tokens')
        self.assertNotIn('private', json.dumps(replay.response_error_details(
            b'{"error":{"code":"private-key","message":"private prompt"}}')))

    def test_stream_failure_diagnostics_distinguish_missing_terminal_and_invalid_json(self):
        for body, reason in ((b'data: {"type":"ping"}\n\n', 'stream_terminal_missing'),
                             (b'data: broken\n\n', 'response_invalid_json')):
            with self.subTest(reason=reason), server(200, body, 'text/event-stream') as (port, _):
                request = sample()
                request['row']['stream'] = True
                result = replay.execute(request, 'secret', port, 'id')
                self.assertFalse(result['passed'])
                self.assertEqual(result['reason'], reason)
                self.assertEqual(result['response_bytes'], len(body))
                self.assertEqual(result['response_sha256'], replay.digest(body))


class ProductionAuditTest(unittest.TestCase):
    def test_usage_audit_matches_storage_namespaces_and_timeout_markers(self):
        query = replay.production_usage_query('tk-replay-test-', [{'response_request_id': 'server-uuid'}])
        # Exercise the generated predicate against persisted identities.
        query = query.replace("now()-interval '3 hours'", '0')
        with sqlite3.connect(':memory:') as db:
            db.execute('CREATE TABLE usage_logs (request_id TEXT, created_at INTEGER)')
            for rid in ('server-uuid', 'local:server-uuid', 'client:server-uuid',
                        'tk-replay-test-1', 'client:tk-replay-test-2', 'local:tk-replay-test-3'):
                db.execute('INSERT INTO usage_logs VALUES (?, 1)', (rid,))
            db.execute("INSERT INTO usage_logs VALUES ('local:unrelated', 1)")
            self.assertEqual(db.execute(query).fetchone()[0], 6)
        with self.assertRaises(replay.ReplayError):
            replay.production_usage_query("prefix' OR TRUE", [])
        with self.assertRaises(replay.ReplayError):
            replay.production_usage_query('prefix', [{'response_request_id': "id'"}])



class ReceiptTest(unittest.TestCase):
    def receipt(self, **changes):
        return replay.seal(dict({'schema': 2, 'corpus_manifest_sha256': 'f'*64, 'tag': '1.2.3', 'prepared_receipt': 'b' * 64,
            'verdict': 'green', 'cutover': False, 'approval_pending': True, 'finished_at': time.time()}, **changes))

    def test_gate_binds_green_evidence_to_reviewed_candidate_and_age(self):
        receipt = self.receipt()
        replay.validate_receipt(receipt, receipt['receipt_sha256'], 'b' * 64, '1.2.3')
        for change in ({'verdict': 'red'}, {'cutover': True}, {'finished_at': 1}, {'prepared_receipt': 'c' * 64}, {'tag': '1.2.4'}):
            invalid = self.receipt(**change)
            with self.subTest(change=change), self.assertRaises(replay.ReplayError):
                replay.validate_receipt(invalid, invalid['receipt_sha256'], 'b' * 64, '1.2.3')
        receipt['verdict'] = 'red'
        with self.assertRaises(replay.ReplayError):
            replay.validate_receipt(receipt, receipt['receipt_sha256'], 'b' * 64, '1.2.3')

    def test_missing_approval_cannot_be_replaced_by_a_green_verdict(self):
        receipt = self.receipt()
        with self.assertRaises(replay.ReplayError):
            replay.validate_receipt(receipt, '', 'b' * 64, '1.2.3')


class ExecutionTest(unittest.TestCase):
    def perform(self, *, gaps=None, failure=False, drift=False, cleanup_failure=False):
        samples = [sample(), sample(2, 'm2', '/v1/chat/completions')]
        keys = '\n'.join(json.dumps({'id': i, 'user_id': i, 'key': 'secret', 'status': 'active', 'deleted_at': None, 'expired': False, 'key_state': 'active'}) for i in (1, 2))
        with tempfile.TemporaryDirectory() as tmp, server(500 if failure else 200) as (port, _):
            box = MagicMock()
            box.start.return_value = port
            box.name = 'tk-replay-test'
            if cleanup_failure:
                box.close.side_effect = replay.ReplayError('cleanup_failed')
            before = {'target': 'green', 'caddy_sha': 'original'}
            with patch.object(replay, 'prepared', return_value=('b'*64, before)), \
                 patch.object(replay, 'inspect', return_value={}), \
                 patch.object(replay, 'snapshot', return_value={'target': 'blue'} if drift else before), \
                 patch.object(replay, 'Sandbox', return_value=box), \
                 patch.object(replay, 'frozen_samples', return_value=(samples, gaps or {}, 2, 'f'*64)), \
                 patch.object(replay, 'sql', side_effect=[keys, 'balance-hash', '0']):
                receipt = replay.replay('1.2.3', Path(tmp))
            self.assertEqual(json.loads((Path(tmp) / 'bluegreen-replay.json').read_bytes()), receipt)
            self.assertGreaterEqual(box.close.call_count, 1)
            return receipt

    def test_full_execution_green_requires_real_successes(self):
        receipt = self.perform()
        self.assertEqual(receipt['verdict'], 'green')
        self.assertEqual(receipt['passed'], 2)
        self.assertEqual(receipt['production_usage_rows'], 0)
        self.assertFalse(receipt['cutover'])
        self.assertTrue(receipt['approval_pending'])
        self.assertNotIn('private prompt', json.dumps(receipt))

    def test_gaps_failures_drift_and_cleanup_never_green(self):
        for args in ({'gaps': {'missing': 1}}, {'failure': True}, {'drift': True}, {'cleanup_failure': True}):
            with self.subTest(args=args):
                self.assertEqual(self.perform(**args)['verdict'], 'red')

    def test_isolation_overrides_live_credentials_and_rejects_public_port(self):
        env = replay.isolated_environment({'DATABASE_HOST': 'prod', 'DATABASE_PASSWORD': 'prod-secret', 'AWS_SECRET_ACCESS_KEY': 'cloud-secret'}, 'sandbox', 'random')
        self.assertEqual(env['DATABASE_HOST'], 'sandbox-pg')
        self.assertEqual(env['DATABASE_PASSWORD'], 'random')
        self.assertNotIn('AWS_SECRET_ACCESS_KEY', env)
        box = replay.Sandbox({'Image': 'sha256:test'})
        state = {'Image': 'sha256:test', 'Config': {'Env': [k+'='+v for k, v in env.items()]},
            'NetworkSettings': {'Networks': {box.name: {}}, 'Ports': {'8080/tcp': [{'HostIp': '0.0.0.0'}]}}, 'Mounts': []}
        with patch.object(replay, 'run', return_value=b'[{"Internal":false}]'), self.assertRaisesRegex(replay.ReplayError, 'loopback'):
            box.verify(state, env)
        state['NetworkSettings']['Ports']['8080/tcp'][0]['HostIp'] = '127.0.0.1'
        with patch.object(replay, 'run', return_value=b'[{"Internal":false}]'):
            box.verify(state, env)


class OrchestrationTest(unittest.TestCase):
    def test_prepare_cannot_inherit_cutover_and_execution_must_produce_receipt(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(cli, 'remote', side_effect=[{'needs_prepare': True}, {'needs_prepare': False}, {'verdict': 'green', 'cutover': False, 'receipt_sha256': 'a'*64}]) as remote, patch.object(cli.subprocess, 'run') as process, patch.dict(os.environ, {'STAGE0_BLUEGREEN_STAGE': 'deploy', 'STAGE0_BLUEGREEN_WAIT_PHASE': 'cutover'}):
            cli.run_replay('1.2.3', 'i-prod', Path(tmp))
            env = process.call_args.kwargs['env']
            self.assertEqual(env['STAGE0_BLUEGREEN_STAGE'], 'prepare')
            self.assertEqual(env['STAGE0_BLUEGREEN_WAIT_PHASE'], 'complete')
            self.assertEqual([c.args[1] for c in remote.call_args_list], ['status', 'status', 'run'])

    def test_ssm_delivery_executes_the_shipped_source(self):
        delivered = {}
        real_run = subprocess.run
        def send(args, **_kwargs):
            parameters = json.loads(args[args.index('--parameters') + 1])
            self.assertEqual(parameters['executionTimeout'], ['60'])
            result = real_run(['bash', '-c', '\n'.join(parameters['commands'])], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            delivered['stdout'] = result.stdout
            return 'command-id'
        def invocation(*_args, **_kwargs):
            return subprocess.CompletedProcess([], 0, json.dumps({'Status': 'Success', 'ResponseCode': 0,
                'StandardOutputContent': delivered['stdout']}), '')
        source = b'import json,sys; print(json.dumps({"operation":sys.argv[1], "tag":sys.argv[3]}))'
        with patch.object(Path, 'read_bytes', return_value=source), patch.object(cli.subprocess, 'check_output', side_effect=send), patch.object(cli.subprocess, 'run', side_effect=invocation):
            result = cli.remote('i-prod', 'status', '1.2.3', timeout=60)
        self.assertEqual(result, {'operation': 'status', 'tag': '1.2.3'})

    def test_prepared_retry_does_not_replace_candidate_and_red_fails(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(cli, 'remote', side_effect=[{'needs_prepare': False}, {'needs_prepare': False}, {'verdict': 'red', 'cutover': False}]), patch.object(cli.subprocess, 'run') as process:
            with self.assertRaisesRegex(RuntimeError, 'replay failed'):
                cli.run_replay('1.2.3', 'i-prod', Path(tmp))
            process.assert_not_called()
            self.assertEqual(json.loads((Path(tmp)/'replay-receipt.json').read_text())['verdict'], 'red')

    def test_cli_rejects_non_prod_before_aws(self):
        proc = subprocess.run([sys.executable, str(ROOT/'scripts/stage0/replay-prod-release.py'), '--tag', '1.2.3', '--target', 'edge-us3'], capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn('invalid choice', proc.stderr)

    def test_workflow_runs_executor_and_resume_keeps_existing_checks(self):
        workflow = yaml.safe_load((ROOT/'.github/workflows/deploy-stage0.yml').read_text())
        job = workflow['jobs']['replay']
        self.assertEqual(job['if'], "inputs.operation == 'replay'")
        self.assertEqual(job['environment'], 'prod')
        runs = '\n'.join(step.get('run', '') for step in job['steps'])
        self.assertIn('replay-prod-release.py --target prod --tag', runs)
        self.assertNotIn('--help', runs)
        self.assertNotIn('rollout-edges', runs)
        self.assertNotIn('promote', runs)
        steps = workflow['jobs']['deploy']['steps']
        deploy = next(s for s in steps if s.get('name') == 'Deploy via SSM Run-Command')
        self.assertIn('replay_gate.outputs.prepared_receipt', deploy['env']['STAGE0_BLUEGREEN_APPROVED_RECEIPT'])
        self.assertEqual(workflow['jobs']['deploy']['if'], "inputs.operation == 'deploy'")
        names = [s.get('name') for s in steps]
        self.assertLess(names.index('Validate human-approved replay receipt'), names.index('Deploy via SSM Run-Command'))
        self.assertIn('Check traffic and 5xx after 5 minutes', names)


if __name__ == '__main__':
    unittest.main()
