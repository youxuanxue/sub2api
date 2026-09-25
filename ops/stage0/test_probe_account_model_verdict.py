#!/usr/bin/env python3
"""Behavioral tests for probe_account_model_verdict.classify_probe_verdict."""

from __future__ import annotations

import unittest
import json
import base64
import io
import importlib.util

from probe_account_model_verdict import classify_probe_verdict, embedding_response_valid, compat_image_response_summary


class ProbeAccountModelVerdictTest(unittest.TestCase):
    @unittest.skipUnless(importlib.util.find_spec('PIL'), 'image decoder installed by Gemini Web CI job')
    def test_gemini_images_require_matching_mime_and_full_decode(self):
        from PIL import Image
        output = io.BytesIO()
        Image.new('RGB', (4, 4), 'blue').save(output, 'PNG')
        valid = output.getvalue()
        for data, mime, expected in [
            (valid, 'image/png', 'servable'),
            (valid, 'image/jpeg', 'uncorrelated_success'),
            (b'\x89PNG\r\n\x1a\n' + b'garbage' * 10, 'image/png', 'uncorrelated_success'),
            (valid[:40], 'image/png', 'uncorrelated_success'),
        ]:
            body = json.dumps({'candidates': [{'finishReason': 'STOP', 'content': {'parts': [
                {'inlineData': {'mimeType': mime, 'data': base64.b64encode(data).decode()}}]}}]})
            self.assertEqual(classify_probe_verdict(endpoint='gemini_image', http_code='200',
                body_text=body, target_account_id=90, usage_row={'account_id': 90}, curl_err=''), expected)

    @unittest.skipUnless(importlib.util.find_spec('PIL'), 'image decoder installed by Gemini Web CI job')
    def test_compat_images_validate_legal_text_payloads_and_exact_account(self):
        from PIL import Image
        output = io.BytesIO()
        Image.new('RGB', (4, 4), 'blue').save(output, 'PNG')
        valid = output.getvalue()

        def envelope(endpoint, text):
            if endpoint == 'messages':
                return {'stop_reason': 'end_turn', 'content': [{'type': 'text', 'text': text}]}
            if endpoint == 'chat':
                return {'choices': [{'finish_reason': 'stop', 'message': {'role': 'assistant', 'content': text}}]}
            return {'status': 'completed', 'output': [{'type': 'message', 'content': [{'type': 'output_text', 'text': text}]}]}

        for endpoint in ('messages', 'chat', 'responses'):
            for data, mime, expected in [
                (valid, 'image/png', 'servable'),
                (valid, 'image/jpeg', 'uncorrelated_success'),
                (valid[:40], 'image/png', 'uncorrelated_success'),
                (b'garbage' * 10, 'image/png', 'uncorrelated_success'),
            ]:
                with self.subTest(endpoint=endpoint, mime=mime, size=len(data)):
                    markdown = '![generated](data:' + mime + ';base64,' + base64.b64encode(data).decode() + ')'
                    body = json.dumps(envelope(endpoint, markdown))
                    self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code='200',
                        body_text=body, target_account_id=200, usage_row={'account_id': 200},
                        curl_err='', expect_image_output=True), expected)
                    if expected == 'servable':
                        summary = compat_image_response_summary(endpoint, body)
                        self.assertEqual(summary['images'][0]['bytes'], len(valid))
                        self.assertEqual(summary['images'][0]['mime_type'], 'image/png')
                        self.assertNotIn(base64.b64encode(valid).decode(), json.dumps(summary))
                        self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code='200',
                            body_text=body, target_account_id=200, usage_row={'account_id': 201},
                            curl_err='', expect_image_output=True), 'wrong_account')
            for text in ('no image', '![bad](data:image/png;base64,***)'):
                body = envelope(endpoint, text)
                # A valid image in an unknown field must never rescue missing output.
                body['debug'] = '![hidden](data:image/png;base64,' + base64.b64encode(valid).decode() + ')'
                self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code='200',
                    body_text=json.dumps(body), target_account_id=200, usage_row={'account_id': 200},
                    curl_err='', expect_image_output=True), 'uncorrelated_success')

    @unittest.skipUnless(importlib.util.find_spec('PIL'), 'image decoder installed by Gemini Web CI job')
    def test_compat_sse_images_require_complete_valid_output(self):
        from PIL import Image
        output = io.BytesIO()
        Image.new('RGB', (4, 4), 'blue').save(output, 'PNG')
        encoded = base64.b64encode(output.getvalue()).decode()
        markdown = '![generated](data:image/png;base64,' + encoded + ')'

        def frame(event, name=''):
            # Reuse the framing owner's multiline-data and CRLF contract.
            raw = json.dumps(event).replace(',', ',\n', 1)
            return (('event: ' + name + '\r\n') if name else '') + ''.join(
                'data: ' + line + '\r\n' for line in raw.splitlines()) + '\r\n'

        def stream(endpoint, text):
            a, b = text[:len(text)//2], text[len(text)//2:]
            if endpoint == 'chat':
                events = [frame({'choices': [{'index': 0, 'delta': {'content': part}}]}) for part in (a, b)]
                events += [frame({'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}]}),
                           frame({'choices': [], 'usage': {'prompt_tokens': 7, 'completion_tokens': 5}}),
                           'data: [DONE]\r\n\r\n']
            elif endpoint == 'messages':
                events = [frame({'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': ''}})]
                events += [frame({'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'text_delta', 'text': part}}) for part in (a, b)]
                events += [frame({'type': 'message_delta', 'delta': {'stop_reason': 'end_turn'}}), frame({'type': 'message_stop'})]
            else:
                events = [frame({'type': 'response.output_text.delta', 'output_index': 0, 'content_index': 0, 'delta': part}) for part in (a, b)]
                events += [frame({'type': 'response.output_text.done', 'output_index': 0, 'content_index': 0, 'text': text})]
                events += [frame({'type': 'response.completed', 'response': {'status': 'completed', 'output': [
                    {'type': 'message', 'content': [{'type': 'output_text', 'text': text}]}]}}), 'data: [DONE]\r\n\r\n']
            return events

        error = frame({'type': 'error', 'error': {'message': 'upstream interrupted'}}, 'error')
        for endpoint in ('messages', 'chat', 'responses'):
            good = stream(endpoint, markdown)
            cases = {
                'complete': (': heartbeat\r\n\r\n' + ''.join(good), 'servable'),
                'missing terminal': (''.join(good[:-2] if endpoint == 'responses' else good[:-1]), 'uncorrelated_success'),
                'explicit error': (''.join(good[:-1]) + error + good[-1], 'uncorrelated_success'),
                'error after terminal': (''.join(good) + error, 'uncorrelated_success'),
                'malformed event': ('data: {broken\n\n' + ''.join(good), 'uncorrelated_success'),
                'corrupt image': (''.join(stream(endpoint, '![bad](data:image/png;base64,***)')), 'uncorrelated_success'),
                'MIME mismatch': (''.join(stream(endpoint, markdown.replace('image/png', 'image/jpeg'))), 'uncorrelated_success'),
                'zero images': (''.join(stream(endpoint, 'no image')), 'uncorrelated_success'),
            }
            for name, (body, expected) in cases.items():
                with self.subTest(endpoint=endpoint, case=name):
                    self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code='200', body_text=body,
                        target_account_id=200, usage_row={'account_id': 200}, curl_err='', expect_image_output=True), expected)
            summary = compat_image_response_summary(endpoint, ''.join(good))
            self.assertEqual(len(summary['images']), 1, 'deltas, done and completed snapshots must not multiply image count')
            self.assertEqual(summary['images'][0]['bytes'], len(output.getvalue()))
            self.assertNotIn(encoded, json.dumps(summary))
            self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code='200', body_text=''.join(good),
                target_account_id=200, usage_row={'account_id': 201}, curl_err='', expect_image_output=True), 'wrong_account')
        # A provider may buffer the full Responses result in its terminal event.
        self.assertTrue(compat_image_response_summary('responses', stream('responses', markdown)[-2])['valid'])
        self.assertTrue(compat_image_response_summary('responses', ''.join(stream('responses', markdown)[:-1]))['valid'])
        self.assertFalse(compat_image_response_summary('responses', ''.join(stream('responses', markdown)) + 'data: [DONE]\n\n')['valid'])

    def test_gemini_requires_complete_response_and_exact_account(self) -> None:
        body = json.dumps({"candidates": [{"finishReason": "STOP", "content": {"parts": [{"text": "ok"}]}}]})
        for endpoint, payload, usage, expected in [
            ("gemini", body, {"account_id": 90}, "servable"),
            ("gemini", body, {"account_id": 91}, "wrong_account"),
            ("gemini", body, None, "uncorrelated_success"),
            ("gemini_image", body, {"account_id": 90}, "uncorrelated_success"),
            ("gemini", '{}', {"account_id": 90}, "uncorrelated_success"),
            ("gemini", body.replace('STOP', 'MAX_TOKENS'), {"account_id": 90}, "uncorrelated_success"),
        ]:
            self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code="200",
                body_text=payload, target_account_id=90, usage_row=usage, curl_err=""), expected)

    def test_transcriptions_require_text_and_target_account_usage(self) -> None:
        cases = [
            ('{"text":"recognized"}', {"account_id": 90}, "servable"),
            ('{"text":""}', {"account_id": 90}, "servable"),
            ('{"text":"recognized"}', {"account_id": 39}, "wrong_account"),
            ('{"text":"recognized"}', None, "uncorrelated_success"),
            ('{"text":null}', {"account_id": 90}, "uncorrelated_success"),
            ('{"error":"upstream failed"}', {"account_id": 90}, "uncorrelated_success"),
            ('[]', {"account_id": 90}, "uncorrelated_success"),
            ('not JSON', {"account_id": 90}, "uncorrelated_success"),
        ]
        for body, usage, expected in cases:
            with self.subTest(body=body, usage=usage):
                self.assertEqual(classify_probe_verdict(
                    endpoint="transcriptions", http_code="200", body_text=body,
                    target_account_id=90, usage_row=usage, curl_err="",
                ), expected)

    def test_embedding_response_valid_requires_data_embedding(self) -> None:
        body = '{"object":"list","data":[{"object":"embedding","embedding":[0.1]}]}'
        self.assertTrue(embedding_response_valid(body))
        self.assertFalse(embedding_response_valid('{"data":[]}'))
        self.assertFalse(embedding_response_valid('{"data":[{"object":"embedding"}]}'))

    def test_embeddings_servable_without_usage_when_body_valid(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row=None,
            curl_err="",
        )
        self.assertEqual(verdict, "servable")

    def test_embeddings_wrong_account_when_usage_points_elsewhere(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row={"account_id": 39},
            curl_err="",
        )
        self.assertEqual(verdict, "wrong_account")

    def test_embeddings_servable_when_usage_matches_target(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row={"account_id": 90},
            curl_err="",
        )
        self.assertEqual(verdict, "servable")

    def test_chat_wrong_account_unchanged(self) -> None:
        verdict = classify_probe_verdict(
            endpoint="chat",
            http_code="200",
            body_text='{"choices":[{"message":{"content":"ok"}}]}',
            target_account_id=90,
            usage_row={"account_id": 39},
            curl_err="",
        )
        self.assertEqual(verdict, "wrong_account")


if __name__ == "__main__":
    unittest.main()
