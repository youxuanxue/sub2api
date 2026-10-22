#!/usr/bin/env bash
# run-probe companions: watch + probe/delivery owners + current fleet manifests.
set -euo pipefail
export PYTHONDONTWRITEBYTECODE=1
python3 - <<'PY'
import datetime,hashlib,json,os,pathlib,shutil,socket,subprocess,sys
root=pathlib.Path(os.environ.get('WATCH_BUNDLE_DIR','/tmp')).resolve()
sys.path.insert(0,str(root))
from host_https_watch import build_config
prod_matrix=json.loads((root/'candidate-usw-20261022.json').read_text())
domestic=json.loads((root/'prod-target.json').read_text())
role=os.environ.get('WATCH_ROLE','')
config=build_config(role,socket.gethostname(),prod_matrix,domestic)
files=['host_https_watch.py','https_health.py','feishu_delivery.py']
for file in files:
    if not (root/file).is_file():raise SystemExit('missing companion: '+file)
print(json.dumps({'role':role,**config,'apply':os.environ.get('APPLY')=='yes-host-https-watch'}))
if os.environ.get('APPLY')!='yes-host-https-watch':raise SystemExit(0)
os.umask(0o077)
# Immutable bundles keep an existing timer on its previous complete code until
# the validated unit is reloaded; never overwrite modules under a running job.
bundle_hash=hashlib.sha256(json.dumps(config,sort_keys=True).encode())
for file in files:bundle_hash.update((root/file).read_bytes())
base=pathlib.Path('/usr/local/lib/tokenkey/https-watch')/bundle_hash.hexdigest()[:16]
unit=pathlib.Path('/etc/systemd/system/tokenkey-https-watch.service')
timer=unit.with_suffix('.timer')
backup=pathlib.Path('/var/backups/tokenkey-https-watch')/datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
backup.mkdir(parents=True,mode=0o700)
for p in (base,unit,timer):
    if p.exists():
        if p.is_dir():shutil.copytree(p,backup/p.name)
        else:shutil.copy2(p,backup/p.name)
state=subprocess.run(['systemctl','show',timer.name,'-p','ActiveState,UnitFileState'],capture_output=True,text=True)
(backup/'before-state.txt').write_text(state.stdout)
# Install only while a previous watcher is idle; never interrupt a delivery.
if subprocess.run(['systemctl','is-active','--quiet',unit.name]).returncode==0:
    raise SystemExit('watcher busy; retry when idle')
base.mkdir(parents=True,exist_ok=True,mode=0o700)
for file in files:shutil.copy2(root/file,base/file)
(base/'config.json').write_text(json.dumps(config)+'\n')
command=['/usr/bin/python3',str(base/'host_https_watch.py'),'--config',str(base/'config.json'),'--state','/var/lib/tokenkey/https-watch/state.json']
if role=='cn-prod':command+=['--env-file','/opt/tokenkey/host-alerts.env']
# Validate credentials and probe every target without sending a test message.
check=subprocess.run(command+['--dry-run'],capture_output=True,text=True)
if check.returncode:raise SystemExit('dry-run failed; timer not enabled')
result=json.loads(check.stdout)
if any(item['failures'] for key,item in result['targets'].items() if not key.startswith('job:')):raise SystemExit('HTTPS target unhealthy; timer not enabled')
unit.write_text('''[Unit]
Description=TokenKey HTTPS and local job health watch
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart='''+' '.join(command)+'''
Environment=PYTHONDONTWRITEBYTECODE=1
UMask=0077
TimeoutStartSec=110
Nice=10
CPUQuota=10%
MemoryMax=128M
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/tokenkey/https-watch
''')
timer.write_text('''[Unit]
Description=TokenKey HTTPS and job health check every two minutes

[Timer]
OnBootSec=90
OnUnitActiveSec=2min
AccuracySec=10s
Unit=tokenkey-https-watch.service

[Install]
WantedBy=timers.target
''')
unit.chmod(0o644);timer.chmod(0o644)
subprocess.run(['systemd-analyze','verify',str(unit),str(timer)],check=True)
subprocess.run(['systemctl','daemon-reload'],check=True)
subprocess.run(['systemctl','enable','--now',timer.name],check=True)
subprocess.run(['systemctl','start',unit.name],check=True)
print(json.dumps({'backup':str(backup),'healthy_dry_run':result,'installed':True}))
PY
