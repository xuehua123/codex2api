$ErrorActionPreference = "Stop"

$ServerIP = "101.35.240.224"
$ServerUser = "root"
$RepoURL = "https://github.com/xuehua123/codex2api.git"
$RepoDir = "/opt/platform/codex2api-src"
$ImageName = "platform-codex2api:latest"
$RemoteComposeDir = "/opt/platform"
$RemoteEnvFile = "/opt/platform/codex2api.env"
$RemoteSourceTar = "/tmp/codex2api_src.tgz"
$LocalSourceTar = "codex2api_src.tgz"

Set-Location -Path $PSScriptRoot

function Invoke-RemotePython {
    param(
        [Parameter(Mandatory = $true)]
        [string]$PythonCode
    )

    @"
$PythonCode
"@ | python -
}

function Invoke-RemoteCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string]$RemoteCommand
    )

    if ($env:DEPLOY_PASSWORD) {
        $env:DEPLOY_HOST = $ServerIP
        $env:DEPLOY_USER = $ServerUser
        $env:DEPLOY_REMOTE_CMD = $RemoteCommand
        Invoke-RemotePython @'
import os
import sys
import time
import paramiko

host = os.environ["DEPLOY_HOST"]
user = os.environ["DEPLOY_USER"]
password = os.environ["DEPLOY_PASSWORD"]
remote_cmd = os.environ["DEPLOY_REMOTE_CMD"]

last_error = None
for attempt in range(1, 7):
    try:
        client = paramiko.SSHClient()
        client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
        client.connect(hostname=host, username=user, password=password, timeout=30, banner_timeout=60, auth_timeout=30)
        try:
            stdin, stdout, stderr = client.exec_command(remote_cmd, get_pty=True, timeout=3600)
            for line in iter(stdout.readline, ""):
                sys.stdout.write(line)
            err = stderr.read().decode("utf-8", errors="replace")
            if err:
                sys.stderr.write(err)
            code = stdout.channel.recv_exit_status()
            raise SystemExit(code)
        finally:
            client.close()
    except Exception as exc:
        last_error = exc
        print(f"[remote] attempt {attempt} failed: {exc}", file=sys.stderr)
        time.sleep(10)

raise SystemExit(f"remote command failed after retries: {last_error}")
'@
        return
    }

    $sshCmd = @"
set -e
$RemoteCommand
"@
    ssh -o StrictHostKeyChecking=no ${ServerUser}@${ServerIP} $sshCmd
}

function Send-SourceArchive {
    param(
        [Parameter(Mandatory = $true)]
        [string]$LocalPath,
        [Parameter(Mandatory = $true)]
        [string]$RemotePath
    )

    if ($env:DEPLOY_PASSWORD) {
        $env:DEPLOY_HOST = $ServerIP
        $env:DEPLOY_USER = $ServerUser
        $env:DEPLOY_SRC = (Join-Path $PSScriptRoot $LocalPath)
        $env:DEPLOY_DST = $RemotePath
        Invoke-RemotePython @'
import os
import sys
import time
import paramiko

host = os.environ["DEPLOY_HOST"]
user = os.environ["DEPLOY_USER"]
password = os.environ["DEPLOY_PASSWORD"]
src = os.environ["DEPLOY_SRC"]
dst = os.environ["DEPLOY_DST"]
total = os.path.getsize(src)
last_pct = -1

def progress(sent, size):
    global last_pct
    pct = int(sent * 100 / size) if size else 100
    if pct != last_pct:
        print(f"[upload] {pct}% ({sent}/{size})")
        last_pct = pct

last_error = None
for attempt in range(1, 7):
    try:
        client = paramiko.SSHClient()
        client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
        client.connect(hostname=host, username=user, password=password, timeout=30, banner_timeout=60, auth_timeout=30)
        try:
            sftp = client.open_sftp()
            try:
                sftp.put(src, dst, callback=progress)
            finally:
                sftp.close()
        finally:
            client.close()
        raise SystemExit(0)
    except Exception as exc:
        last_error = exc
        print(f"[upload] attempt {attempt} failed: {exc}", file=sys.stderr)
        time.sleep(10)

raise SystemExit(f"upload failed after retries: {last_error}")
'@
        return
    }

    scp -o StrictHostKeyChecking=no $LocalPath ${ServerUser}@${ServerIP}:${RemotePath}
}

$LocalHead = (git rev-parse HEAD).Trim()
$ShortHead = (git rev-parse --short HEAD).Trim()
$RemoteMain = ((git ls-remote origin refs/heads/main) -split "\s+")[0]
$DirtyFiles = git status --porcelain
$HasDirtyWorktree = [bool]($DirtyFiles -join "")

$DeployMode = "git"
if ($HasDirtyWorktree -or $LocalHead -ne $RemoteMain) {
    $DeployMode = "source-sync"
}

Write-Host "Deploy mode: $DeployMode" -ForegroundColor Cyan
Write-Host "Local HEAD: $LocalHead" -ForegroundColor DarkCyan
Write-Host "Remote main: $RemoteMain" -ForegroundColor DarkCyan

if ($DeployMode -eq "source-sync") {
    Write-Host "`n>>> [1/4] DOING: create source archive from current worktree ..." -ForegroundColor Cyan
    if (Test-Path $LocalSourceTar) {
        Remove-Item $LocalSourceTar -Force
    }
    tar -czf $LocalSourceTar `
        --exclude=.git `
        --exclude=.idea `
        --exclude=.vscode `
        --exclude=frontend/node_modules `
        --exclude=frontend/dist `
        --exclude=docs `
        --exclude=.env `
        --exclude=.env.local `
        --exclude=codex2api_prod.tar `
        --exclude=codex2api_src.tgz `
        --exclude=deploy.ps1 `
        .

    Write-Host "`n>>> [2/4] DOING: upload source archive to server ..." -ForegroundColor Cyan
    Send-SourceArchive -LocalPath $LocalSourceTar -RemotePath $RemoteSourceTar

    $RemoteBuildVersion = "local-$ShortHead"
    $RemoteCmd = @"
set -e
mkdir -p $RepoDir
if [ -d $RepoDir/.git ]; then
  git -C $RepoDir remote set-url origin $RepoURL || true
  git -C $RepoDir reset --hard || true
  git -C $RepoDir clean -fdx || true
else
  rm -rf $RepoDir
  mkdir -p $RepoDir
fi
tar -xzf $RemoteSourceTar -C $RepoDir
rm -f $RemoteSourceTar
docker build -t $ImageName --build-arg BUILD_VERSION=$RemoteBuildVersion $RepoDir
cd $RemoteComposeDir
echo '--- TRUSTED_PROXIES ---'
grep '^TRUSTED_PROXIES=' $RemoteEnvFile || true
docker compose up -d --force-recreate codex2api
echo '--- CONTAINER IMAGE ID ---'
docker inspect codex2api --format '{{.Image}}'
echo '--- LOCAL HEALTH ---'
curl -fsS http://127.0.0.1:8080/health
"@

    Write-Host "`n>>> [3/4] DOING: remote build and deploy ..." -ForegroundColor Cyan
    Invoke-RemoteCommand -RemoteCommand $RemoteCmd

    Write-Host "`n>>> [4/4] DOING: clean up local source archive ..." -ForegroundColor Cyan
    Remove-Item $LocalSourceTar -ErrorAction SilentlyContinue
}
else {
    $RemoteCmd = @"
set -e
if [ ! -d $RepoDir/.git ]; then
  rm -rf $RepoDir
  git clone $RepoURL $RepoDir
else
  git -C $RepoDir remote set-url origin $RepoURL
fi
git -C $RepoDir fetch --prune origin
git -C $RepoDir checkout --force main
git -C $RepoDir reset --hard $LocalHead
git -C $RepoDir clean -fdx
docker build -t $ImageName --build-arg BUILD_VERSION=$ShortHead $RepoDir
cd $RemoteComposeDir
echo '--- TRUSTED_PROXIES ---'
grep '^TRUSTED_PROXIES=' $RemoteEnvFile || true
docker compose up -d --force-recreate codex2api
echo '--- CONTAINER IMAGE ID ---'
docker inspect codex2api --format '{{.Image}}'
echo '--- LOCAL HEALTH ---'
curl -fsS http://127.0.0.1:8080/health
"@

    Write-Host "`n>>> [1/3] DOING: remote git sync and build ..." -ForegroundColor Cyan
    Invoke-RemoteCommand -RemoteCommand $RemoteCmd
}

Write-Host "`n>>> [post] DOING: public health check ..." -ForegroundColor Cyan
curl.exe -fsS https://codex.wenrugouai.cn/health

Write-Host "`n=== ALL DONE ===" -ForegroundColor Green
