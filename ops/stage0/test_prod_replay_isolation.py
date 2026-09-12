"""Real Docker isolation regression; no production resources (US-056)."""
import json
import secrets
import shutil
import subprocess
import time
import unittest


class DockerIsolationTest(unittest.TestCase):
    def test_shared_none_namespace_has_no_host_port_or_external_route(self):
        if not shutil.which('docker'):
            self.skipTest('Docker unavailable')
        def docker(args):
            return subprocess.check_output(['docker', *args], stderr=subprocess.PIPE, timeout=120)
        try:
            docker(['version', '--format', '{{.Server.Version}}'])
        except subprocess.SubprocessError:
            self.skipTest('Docker daemon unavailable')
        try:
            docker(['image', 'inspect', 'python:3.13-slim'])
        except subprocess.CalledProcessError:
            docker(['pull', 'python:3.13-slim'])
        name = 'tk-replay-isolation-test-' + secrets.token_hex(4)
        created = []
        try:
            docker(['run', '-d', '--name', name, '--network', 'none', '--memory', '64m', '--cpus', '0.25',
                    'python:3.13-slim', 'python', '-m', 'http.server', '8080'])
            created.append(name)
            peer = name + '-peer'
            docker(['run', '-d', '--name', peer, '--network', 'container:' + name, '--memory', '64m', '--cpus', '0.25',
                    'python:3.13-slim', 'python', '-m', 'http.server', '8081'])
            created.append(peer)
            state = json.loads(docker(['inspect', name]))[0]
            self.assertFalse(any(state['NetworkSettings']['Ports'].values()))
            for _ in range(20):
                try:
                    docker(['exec', peer, 'python', '-c', 'import urllib.request; assert urllib.request.urlopen("http://127.0.0.1:8080").status==200'])
                    break
                except subprocess.CalledProcessError:
                    time.sleep(.2)
            else:
                self.fail('container loopback unavailable')
            routes = docker(['exec', name, 'python', '-c', 'from pathlib import Path; print(Path("/proc/net/route").read_text())']).decode()
            self.assertFalse(any(line.split()[1] == '00000000' for line in routes.splitlines()[1:] if line.strip()))
            docker(['exec', name, 'python', '-c', 'import socket\ntry:\n socket.create_connection(("1.1.1.1",443),1)\nexcept OSError: pass\nelse: raise SystemExit("unexpected egress")'])
            self.assertEqual(state['HostConfig']['NetworkMode'], 'none')
            peer_state = json.loads(docker(['inspect', peer]))[0]
            self.assertEqual(peer_state['HostConfig']['NetworkMode'], 'container:' + state['Id'])
            self.assertFalse(any(peer_state['NetworkSettings']['Ports'].values()))
        finally:
            for container in reversed(created):
                docker(['rm', '-f', container])



if __name__ == '__main__':
    unittest.main()
