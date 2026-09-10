#!/usr/bin/env bash
# Sourced by the installer while the QA lifecycle lock is held.
qa_runtime_backup() {
  QA_RUNTIME_BACKUP="$(mktemp -d /var/lib/tokenkey/qa-runtime-backup.XXXXXXXX)"
  QA_RUNTIME_OLD_ID="$(sudo docker ps -aq --filter 'name=^/tokenkey-qa-runtime$')"
  QA_RUNTIME_FILES=(
    /usr/local/bin/tokenkey-qa-maintenance.sh
    /usr/local/lib/tokenkey/qa-runtime.sh
    /usr/local/lib/tokenkey/qa-runtime-compose.yml
    /usr/local/lib/tokenkey/qa-runtime-install.py
    /etc/systemd/system/tokenkey-qa-maintenance.service
    /etc/systemd/system/tokenkey-qa-maintenance.timer
  )
  local path
  for path in "${QA_RUNTIME_FILES[@]}"; do
    if sudo test -f "$path"; then
      sudo cp -p "$path" "$QA_RUNTIME_BACKUP/$(basename "$path")"
    fi
  done
  QA_RUNTIME_BACKUP_READY=1
}

qa_runtime_rollback() {
  [[ "${QA_RUNTIME_BACKUP_READY:-0}" = 1 ]] || return 0
  local current path
  current="$(sudo docker ps -aq --filter 'name=^/tokenkey-qa-runtime$')" || return 1
  if [[ "$current" != "$QA_RUNTIME_OLD_ID" ]]; then
    if [[ -n "$current" ]]; then sudo docker rm "$current" >/dev/null || return 1; fi
    if [[ -n "$QA_RUNTIME_OLD_ID" ]]; then
      sudo docker rename "$QA_RUNTIME_OLD_ID" tokenkey-qa-runtime || return 1
    fi
  fi
  for path in "${QA_RUNTIME_FILES[@]}"; do
    if sudo test -f "$QA_RUNTIME_BACKUP/$(basename "$path")"; then
      sudo cp -p "$QA_RUNTIME_BACKUP/$(basename "$path")" "$path" || return 1
    else
      sudo rm -f "$path" || return 1
    fi
  done
  sudo systemctl daemon-reload
}

qa_runtime_commit() {
  if [[ -n "$QA_RUNTIME_OLD_ID" && "$QA_RUNTIME_OLD_ID" != "$(sudo docker ps -aq --filter 'name=^/tokenkey-qa-runtime$')" ]]; then
    sudo docker rm "$QA_RUNTIME_OLD_ID" >/dev/null
  fi
}

qa_runtime_cleanup() {
  if [[ -n "${QA_RUNTIME_BACKUP:-}" ]]; then
    sudo find "$QA_RUNTIME_BACKUP" -maxdepth 1 -type f -delete
    sudo rmdir "$QA_RUNTIME_BACKUP"
  fi
}
