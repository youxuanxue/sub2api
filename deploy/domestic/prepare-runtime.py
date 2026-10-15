import argparse
import json
import os
import secrets
import shutil
from pathlib import Path

parser = argparse.ArgumentParser(
    description="Prepare a fresh domestic runtime in protected local state"
)
parser.add_argument(
    "state_dir", type=Path, help="Protected directory containing verified images.json"
)
args = parser.parse_args()
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
(out / ".env").write_text("".join(k + "=" + v + "\n" for k, v in env.items()))
shutil.copyfile(root / "deploy/aws/stage0/docker-compose.yml", out / "compose.yml")
shutil.copyfile(
    root / "deploy/domestic/compose.override.yml", out / "compose.override.yml"
)
shutil.copyfile(root / "deploy/domestic/compose.local.yml", out / "compose.local.yml")
caddy = (root / "deploy/aws/stage0/Caddyfile.edge").read_text()
caddy = (
    caddy.replace("${API_DOMAIN}", "api-cn.tokenkey.dev")
    .replace("${ACME_EMAIL}", "admin@cn.tokenkey.dev")
    .replace("${MAIN_GATEWAY_ALLOWED_CIDR}", "0.0.0.0/0")
)
(out / "Caddyfile").write_text(caddy)
shutil.copyfile(root / "deploy/domestic/bootstrap-host.sh", out / "bootstrap-host.sh")
shutil.copyfile(state / "images.json", out / "images.json")
for artifact in out.iterdir():
    if artifact.is_file():
        artifact.chmod(0o600)
print("Runtime bundle prepared; secrets omitted")
