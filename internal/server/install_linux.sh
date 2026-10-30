# Linux service migration keeps the existing identity directory and retry budget.
linux_agent_load_state() {
  # Older systemd releases do not support --value.
  if state="$(systemctl show --property=LoadState "$1.service")"; then
    :
  else
    [ "$state" = LoadState=not-found ] || { echo 'Cannot inspect the existing agent service.' >&2; return 1; }
  fi
  case "$state" in
    LoadState=not-found|LoadState=loaded|LoadState=masked|LoadState=error|LoadState=bad-setting|LoadState=merged|LoadState=stub) printf '%s\n' "${state#LoadState=}" ;;
    *) echo 'Cannot inspect the existing agent service.' >&2; return 1 ;;
  esac
}
linux_agent_check_unit() {
  [ -f "$1" ] && [ ! -L "$1" ] &&
    awk -v prefix="ExecStart=$2 " '/^ExecStart=/ { count++; if (index($0, prefix) != 1) bad=1 } END { exit (count != 1 || bad) }' "$1" || {
      echo "Refusing to replace an unrecognized agent service: $1" >&2; return 1;
    }
}
linux_agent_check_retry_state() {
  for state_file in "$agent_target.update-state.json" "$legacy_agent_target.update-state.json"; do
    if [ -e "$state_file" ] || [ -L "$state_file" ]; then
      [ -f "$state_file" ] && [ ! -L "$state_file" ] || { echo 'Unrecognized agent retry state file.' >&2; return 1; }
    fi
  done
  if [ -f "$legacy_agent_target.update-state.json" ] && [ -f "$agent_target.update-state.json" ]; then
    cmp -s "$legacy_agent_target.update-state.json" "$agent_target.update-state.json" || { echo 'Agent retry state files conflict; installation aborted.' >&2; return 1; }
  fi
}
linux_agent_copy_retry_state() {
  linux_agent_check_retry_state
  if [ -f "$legacy_agent_target.update-state.json" ] && [ ! -f "$agent_target.update-state.json" ]; then
    cp -p "$legacy_agent_target.update-state.json" "$agent_target.update-state.json"
  fi
}
linux_agent_remove_legacy_files() {
  if [ -f "$legacy_agent_target" ] && [ ! -L "$legacy_agent_target" ]; then rm -f -- "$legacy_agent_target"; fi
  if [ -f "$legacy_agent_target.update-state.json" ] && [ ! -L "$legacy_agent_target.update-state.json" ]; then rm -f -- "$legacy_agent_target.update-state.json"; fi
}
install_linux_agent_service() {
  current_unit=/etc/systemd/system/linksphere-agent.service
  legacy_unit=/etc/systemd/system/gosshd-agent.service
  current_state="$(linux_agent_load_state linksphere-agent)"
  legacy_state="$(linux_agent_load_state gosshd-agent)"
  if [ "$current_state" != not-found ]; then linux_agent_check_unit "$current_unit" "$agent_target"; fi
  if [ "$legacy_state" != not-found ]; then linux_agent_check_unit "$legacy_unit" "$legacy_agent_target"; fi
  [ ! -L "$agent_target" ] && [ ! -L "$legacy_agent_target" ] || { echo 'Refusing to replace a symlinked agent executable.' >&2; return 1; }
  linux_agent_check_retry_state
  if [ "$current_state" != not-found ]; then systemctl stop linksphere-agent; fi
  if [ "$legacy_state" != not-found ]; then systemctl stop gosshd-agent; fi
  stop_agent_processes
  linux_agent_copy_retry_state
  mv -f "$tmp" "$agent_target"
  mkdir -p /etc/systemd/system
  mv -f "$tmp.service" "$current_unit"
  systemctl daemon-reload
  systemctl enable linksphere-agent
  systemctl restart linksphere-agent
  systemctl status linksphere-agent --no-pager
  # Retire the legacy startup only after the new service started successfully.
  if [ "$legacy_state" != not-found ]; then
    systemctl disable gosshd-agent
    rm -f -- "$legacy_unit"
    systemctl daemon-reload
  fi
  linux_agent_remove_legacy_files
}
