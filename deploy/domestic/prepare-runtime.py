import argparse
import json
import os
import secrets
import shutil
import re
import subprocess
from pathlib import Path

from target import load_target

parser = argparse.ArgumentParser(
    description="Prepare a fresh domestic runtime in protected local state"
)
parser.add_argument(
    "state_dir", type=Path, help="Protected directory containing verified images.json"
)
parser.add_argument("--target", help="Explicit isolated SSH target; omitted preserves UCloud configuration")
args = parser.parse_args()
target = load_target(args.target) if args.target else None
if target and target['ingress'] != 'ssh-tunnel':
    parser.error('Production must adopt the reconciled candidate; fresh runtime generation is forbidden')
root = Path(__file__).resolve().parents[2]
state = args.state_dir.expanduser().resolve()
if state == root or root in state.parents:
    parser.error("Secret state must be outside the repository")
os.umask(0o077)
state.mkdir(mode=0o700, parents=True, exist_ok=True)
state.chmod(0o700)
out = state / "runtime"
out.mkdir(mode=0o700, exist_ok=True)
out.chmod(0o700)
p = state / "login-secrets.json"
if p.exists():
    secret = json.loads(p.read_text())
else:
    secret = {
        k: secrets.token_hex(32)
        for k in [
            "admin_password",
            "customer_password",
            "postgres_password",
            "redis_password",
            "jwt_secret",
            "totp_key",
        ]
    }
    p.write_text(json.dumps(secret))
p.chmod(0o600)
imgs = json.loads((state / "images.json").read_text())
env = {
    "TOKENKEY_IMAGE": imgs["tokenkey"]["tag"],
    "CADDY_IMAGE": imgs["caddy"]["tag"],
    "POSTGRES_IMAGE": imgs["postgres"]["tag"],
    "REDIS_IMAGE": imgs["redis"]["tag"],
    "ADMIN_EMAIL": "admin@cn.tokenkey.dev",
    "ADMIN_PASSWORD": secret["admin_password"],
    "POSTGRES_PASSWORD": secret["postgres_password"],
    "REDIS_PASSWORD": secret["redis_password"],
    "JWT_SECRET": secret["jwt_secret"],
    "TOTP_ENCRYPTION_KEY": secret["totp_key"],
    "SERVER_FRONTEND_URL": "https://api-cn.tokenkey.dev",
    "TZ": "Asia/Shanghai",
}
if target:
    env.update({
        "API_DOMAIN": target["domain"],
        "ADMIN_EMAIL": target["admin_email"],
        "SERVER_FRONTEND_URL": target["frontend_url"],
        "QA_CAPTURE_ENABLED": "false",
        "QA_ARCHIVE_ENABLED": "false",
        "TELEMETRY_ARCHIVE_ENABLED": "false",
    })
(out / ".env").write_text("".join(k + "=" + v + "\n" for k, v in env.items()))
shutil.copyfile(root / "deploy/aws/stage0/docker-compose.yml", out / "compose.yml")
shutil.copyfile(
    root / "deploy/domestic/compose.override.yml", out / "compose.override.yml"
)
shutil.copyfile(root / "deploy/domestic/compose.local.yml", out / "compose.local.yml")
if target:
    render_env = dict(os.environ)
    render_env.update(API_DOMAIN=target["domain"], ACME_EMAIL=target["admin_email"],
                      GLOBAL_SITE_PHASE="disabled", SITE_DOMAIN="", GLOBAL_SITE_DOMAIN="",
                      API_ALIAS_DOMAIN="")
    rendered = subprocess.run(
        ["bash", str(root / "deploy/aws/stage0/render-prod-caddyfile.sh"),
         str(root / "deploy/aws/stage0/Caddyfile"), "/dev/stdout"],
        check=True, env=render_env, capture_output=True, text=True)
    caddy = rendered.stdout
    caddy, count = re.subn(r"\ttls \{\n.*?^\t\}\n", "\ttls internal\n", caddy, flags=re.S | re.M)
    if count != 1:
        raise ValueError("Shared Caddy TLS block changed; refusing ambiguous override")
    (out / "compose.tunnel.yml").write_text("""services:
  caddy:
    ports: !override
      - "127.0.0.1:80:80"
      - "127.0.0.1:443:443"
  tokenkey:
    environment:
      QA_ARCHIVE_ENABLED: "false"
""")
    (out / "Caddyfile").write_text(caddy)
else:
    render_env = dict(os.environ)
    render_env.update(API_DOMAIN="api-cn.tokenkey.dev", ACME_EMAIL="admin@cn.tokenkey.dev",
                      GLOBAL_SITE_PHASE="disabled", SITE_DOMAIN="", GLOBAL_SITE_DOMAIN="", API_ALIAS_DOMAIN="")
    subprocess.run(["bash", str(root / "deploy/aws/stage0/render-prod-caddyfile.sh"),
                    str(root / "deploy/aws/stage0/Caddyfile"), str(out / "Caddyfile")],
                   check=True, env=render_env, capture_output=True, text=True)
shutil.copyfile(root / "deploy/domestic/bootstrap-host.sh", out / "bootstrap-host.sh")
shutil.copyfile(state / "images.json", out / "images.json")
for artifact in out.iterdir():
    if artifact.is_file():
        artifact.chmod(0o600)
print("Runtime bundle prepared; secrets omitted")
