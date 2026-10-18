#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'usage: %s [--check] <RustDesk device ID or name>\n       %s [--check] --file <devices.txt>\n' "${0##*/}" "${0##*/}"
}

device=''
device_file=''
check_only=false
while (( $# )); do
  case $1 in
    --check) check_only=true; shift ;;
    --file)
      if [[ $# -lt 2 || -z $2 || -n $device_file ]]; then
        printf 'error: --file requires one devices.txt path\n' >&2
        exit 2
      fi
      device_file=$2
      shift 2
      ;;
    --help|-h) usage; exit 0 ;;
    --*) printf 'error: unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    *)
      if [[ -n $device || -z $1 ]]; then usage >&2; exit 2; fi
      device=$1
      shift
      ;;
  esac
done
if [[ -z $device && -z $device_file || -n $device && -n $device_file ]]; then
  usage >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
rdcli_bin=${RDCLI_BIN:-$script_dir/rdcli}

ids=()
passwords=()
if [[ -n $device_file ]]; then
  if [[ ! -f $device_file || ! -r $device_file ]]; then
    printf 'error: cannot read device file: %s\n' "$device_file" >&2
    exit 2
  fi
  declare -A seen=()
  line_number=0
  # Validate the entire file before connecting. Never evaluate or print its passwords.
  while IFS= read -r line || [[ -n $line ]]; do
    line_number=$((line_number + 1))
    if (( line_number == 1 )); then line=${line#$'\xef\xbb\xbf'}; fi
    line=${line%$'\r'}
    if [[ $line =~ ^[[:blank:]]*(#.*)?$ ]]; then continue; fi
    if [[ ! $line =~ ^[[:blank:]]*([0-9]+)[[:blank:]]+([^[:blank:]].*)$ ]]; then
      printf 'error: device file line %d: expected numeric ID followed by space/tab and a nonempty password\n' "$line_number" >&2
      exit 2
    fi
    id=${BASH_REMATCH[1]}
    password=${BASH_REMATCH[2]}
    if [[ -n ${seen[$id]:-} ]]; then
      printf 'error: device file line %d: duplicate device ID %s\n' "$line_number" "$id" >&2
      exit 2
    fi
    seen[$id]=1
    ids+=("$id")
    passwords+=("$password")
  done < "$device_file"
  if (( ${#ids[@]} == 0 )); then
    printf 'error: device file contains no devices\n' >&2
    exit 2
  fi
elif [[ -z ${RDC_PASSWORD:-} ]]; then
  if [[ ! -t 0 ]]; then
    printf 'error: device password required; set RDC_PASSWORD (API login does not supply the device password)\n' >&2
    exit 2
  fi
  if ! IFS= read -r -s -p 'RustDesk device password (input hidden): ' RDC_PASSWORD; then
    printf '\nerror: could not read device password\n' >&2
    exit 2
  fi
  printf '\n' >&2
  if [[ -z $RDC_PASSWORD ]]; then
    printf 'error: device password must not be empty\n' >&2
    exit 2
  fi
  export RDC_PASSWORD
fi

install_command=$(cat <<'POWERSHELL'
Write-Output '[rdcli] Connected. Downloading NetBird installation script...'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$client = [Net.WebClient]::new()
try {
    $script = [Text.Encoding]::UTF8.GetString($client.DownloadData('https://www.keviince.com/netbird/install.ps1')).TrimStart([char]0xFEFF)
} finally {
    $client.Dispose()
}
Invoke-Expression $script
POWERSHELL
)

if $check_only; then install_command='whoami'; fi

run_device() {
  printf '[rdcli] Connecting to device %s via relay (setup timeout %s)...\n' "$1" "${RDC_CONNECT_TIMEOUT:-30s}" >&2
  # Keep this network upgrade off a direct connection through NetBird itself.
  RDC_PASSWORD="$2" "$rdcli_bin" --relay --timeout "${RDC_CONNECT_TIMEOUT:-30s}" -c "$1" \
    sh --command-timeout "${RDC_COMMAND_TIMEOUT:-30m}" "$install_command" </dev/null
}

if [[ -z $device_file ]]; then
  run_device "$device" "$RDC_PASSWORD"
  exit $?
fi

trap 'printf "\n[rdcli] Interrupted; no further devices will be started.\n" >&2; exit 1' INT TERM
printf '[rdcli] Loaded %d device(s). Mode: %s.\n' "${#ids[@]}" "$($check_only && printf check || printf upgrade)"
failed=0
for i in "${!ids[@]}"; do
  id=${ids[$i]}
  printf '[rdcli] Device %d/%d: %s\n' "$((i + 1))" "${#ids[@]}" "$id"
  # One stream preserves log order. Print complete lines so even output without
  # a final newline cannot run into the following device's status.
  if run_device "$id" "${passwords[$i]}" 2>&1 |
    while IFS= read -r output_line || [[ -n $output_line ]]; do
      printf '%s\n' "$output_line"
    done; then
    result=OK
    code=0
  else
    code=$?
    result=FAILED
    failed=$((failed + 1))
  fi
  printf '[rdcli] %s: %s (exit %s)\n' "$id" "$result" "$code"
done
printf '[rdcli] Finished: %d succeeded, %d failed.\n' "$((${#ids[@]} - failed))" "$failed"
if (( failed )); then exit 1; fi
