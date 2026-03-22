# Vulnerability Setup Script

`setup-vulns.sh` turns a fresh Ubuntu 24.04 box into a comprehensive vulnerable target for testing jagr. It is run automatically by Vagrant on provision.

## Planted Vulnerabilities (~50+)

| Category | Examples |
|---|---|
| Users & access | UID-0 backdoor, empty passwords, weak passwords, permissive sudoers, bad group memberships |
| Persistence | Cron reverse shells, systemd timers, rc.local, init.d, shell profile backdoors, PAM bypass, MOTD beacon, at jobs |
| SUID/SGID/caps | SUID python/find/perl/bash, cap_setuid, cap_dac_read_search, cap_net_raw |
| File permissions | World-writable /etc/passwd, /etc/shadow, /etc/crontab, writable systemd units, writable /usr/local/bin |
| Hidden artifacts | Reverse shells in /tmp/.cache, /dev/shm, /var/tmp, /usr/share, /var/lib |
| Network | IP forwarding, NAT redirect, DNS hijacking of Ubuntu/GitHub/PyPI repos, disabled SYN cookies |
| SSH | Root login, empty passwords, weak 1024-bit host key, rogue authorized_keys, leaked private key |
| Credentials | AWS/GCP/K8s keys, .env files, .my.cnf, .pgpass, git-credentials, bash_history leaks |
| Web servers | Apache server-status, phpinfo, PHP webshell, Nginx alias traversal, directory listing |
| Databases | MySQL bound 0.0.0.0 no password, Redis no auth |
| NFS/FTP/SNMP | no_root_squash exports, anonymous FTP upload, public/private community strings |
| Docker | World-readable socket, TCP API without TLS, privileged container script |
| Telnet/xinetd | Telnet service enabled, socat backdoor unit |
| Kernel params | ASLR disabled, ptrace_scope=0, suid_dumpable=2, kptr_restrict=0, rp_filter=0 |
| Log tampering | Truncated auth.log/wtmp/btmp, rsyslog filter suppressing sudo/ssh |
| PATH hijacking | World-writable /opt/custom-bin prepended to PATH, trojanized sudo/su |
| Wildcard injection | Tar cron job with `--checkpoint-action` payload files |
| LD_PRELOAD | libprocesshider.so in /etc/ld.so.preload, LD_LIBRARY_PATH injection |
| Misc | No firewall, weak umask 000, core dumps enabled, dev tools on "production", missing sticky bit |
