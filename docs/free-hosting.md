# Free hosting for a small pilot

Recommended starting point: one Oracle Cloud Always Free VM running the existing
`compose.hosted.yaml` stack. Visitors need only a browser. The server runs the
website, PostgreSQL, scheduled workers, and each user's Codex process.

This is a deployment guide, not a deployed service. You still need to create the
hosting/DNS/provider accounts and supply their credentials privately on the server.

## Why this host

As checked on September 17, 2026:

| Option | Fit for this application |
| --- | --- |
| Oracle Always Free VM | Can run the full Docker stack with persistent storage. Current A1 free allowance is 2 OCPUs / 12 GB total; select only resources within your console's free allowance. Capacity is not guaranteed, and idle VMs may be reclaimed. |
| Vercel | Useful for a frontend, but its bounded request-based functions do not replace this app's persistent Go server, schedulers, and Codex subprocesses. Splitting the frontend would still require a backend host and additional authentication/origin configuration. |
| Render Free | Sleeps after 15 minutes without inbound traffic and cannot attach persistent disks. That interrupts scheduled monitoring and loses local Codex credentials across replacements. Its free PostgreSQL databases also expire after 30 days. |

Sources: [Oracle Always Free resources](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm),
[Vercel function limits](https://vercel.com/docs/functions/limitations),
[Render free services](https://render.com/docs/free).

Free hosting is suitable for this pilot, but is not a guarantee of uninterrupted
service. Keep database and credential backups outside the VM. Do not select paid
resources or rely on temporary trial credits when aiming for an ongoing $0 setup.

## 1. Create the server

In Oracle Cloud, create an Ubuntu VM in your home region using an Always Free
eligible shape. Prefer Arm A1 with 2 OCPUs and 12 GB RAM, if available within your
account's free allowance; the smaller 1 GB micro instance is tight for this stack.
Use a 50 GB boot disk within the free storage allowance and retain your SSH key.
The Dockerfile builds natively on either Arm64 or AMD64; no hardcoded x86 platform
is required. The Arm deployment still needs a real-machine smoke test before launch.

Assign a public IP. Allow TCP 80 and 443 in the cloud network rules and host
firewall; restrict SSH (22) to your own IP where possible. Do not expose PostgreSQL
5432 or the internal app port 8080. Install Docker Engine and the Compose plugin
using [Docker's Ubuntu instructions](https://docs.docker.com/engine/install/ubuntu/).
Only the site operator needs Docker; visitors do not need Docker accounts.

## 2. Choose a website address

For a $0 pilot, [DuckDNS](https://www.duckdns.org/about.jsp) provides a free subdomain
such as `your-project.duckdns.org`. Create one and point its A record to the VM's
public IP. Keep it updated if that IP changes. Avoid a stale AAAA record pointing
elsewhere. You can move to an owned domain later by updating DNS, the public URL,
and Google OAuth redirect configuration.

A free subdomain works for HTTPS hosting, but verify Google's consent-screen and
domain requirements for the audience you intend to invite. Start Google OAuth in
Testing mode with explicit test users; do not assume a shared free domain satisfies
all requirements for a fully verified public OAuth application.

## 3. Configure sign-in and account email

Create a Google Web OAuth client and register:

```
https://your-project.duckdns.org/auth/google/callback
```

Follow [the account setup guide](hosted-website.md) for the consent screen and
configuration. Google owns the provider login; never collect a visitor's Google or
ChatGPT password on your site.

Email/password signup also needs outgoing email for verification and recovery.
For a small pilot without an owned domain, a dedicated Gmail account with an app
password can provide authenticated SMTP on `smtp.gmail.com:587`. Use the full
Gmail address for both username and sender. Google requires 2-Step Verification
for app passwords, and some accounts are ineligible; see [Google's app-password
instructions](https://support.google.com/accounts/answer/185833).
Deliverability and sending limits still apply. Use a separate sender account and
store its app password only in server configuration.

An alternative after obtaining an owned domain is a transactional email provider
such as [Resend](https://resend.com/pricing), whose current free transactional tier
includes 3,000 emails/month with a 100/day limit. Configure its SMTP credentials and
verified sender domain. Do not assume a free website subdomain supplies the DNS
records required to verify an email sender.

## 4. Start the website

On the VM, check out the repository. Create a private `.env` in the repository root
with the following values, replacing every placeholder:

```dotenv
ORCH_DOMAIN=your-project.duckdns.org
ORCH_GOOGLE_CLIENT_ID=your-google-web-client-id
ORCH_GOOGLE_CLIENT_SECRET=your-google-web-client-secret
ORCH_HOSTED_DB_PASSWORD=generate-a-long-random-hex-password
ORCH_MAX_USERS=5
ORCH_AUTH_SMTP_HOST=smtp.gmail.com
ORCH_AUTH_SMTP_PORT=587
ORCH_AUTH_SMTP_USERNAME=your-dedicated-sender@gmail.com
ORCH_AUTH_SMTP_PASSWORD=your-app-password
ORCH_AUTH_SMTP_FROM=your-dedicated-sender@gmail.com
```

Generate a database password with `openssl rand -hex 32`. Restrict `.env` to its
owner with `chmod 600 .env`. Do not put credentials in Vite variables, Git commits,
screenshots, or chat. Then run:

```sh
docker compose -f compose.hosted.yaml config --quiet
docker compose -f compose.hosted.yaml up -d --build
docker compose -f compose.hosted.yaml ps
```

### Automatic deploys from GitHub

The repository includes a production deployment workflow at
`.github/workflows/deploy-hosted.yml`. It runs after pushes to `main` and
rebuilds the hosted Compose stack on the Oracle VM over SSH. The workflow does
not use the local/private Compose project.

Create these GitHub Actions secrets in the `production` environment:

| Secret | Value |
| --- | --- |
| `ORACLE_HOST` | Oracle VM public IP or hostname |
| `ORACLE_USER` | SSH user, such as `ubuntu` |
| `ORACLE_APP_DIR` | Absolute path of the repository on the VM |
| `ORACLE_SSH_PRIVATE_KEY` | Private key authorized in the VM user's `~/.ssh/authorized_keys` |
| `ORACLE_SSH_KNOWN_HOSTS` | Output of `ssh-keyscan -H <oracle-host>` |

On the Oracle VM, clone this repository into `ORACLE_APP_DIR`, create the
private hosted `.env` described above, and confirm the SSH user can run Docker
without `sudo`. The first deployment can still be started manually; later
pushes to `main` run the workflow automatically. The workflow uses
`git merge --ff-only`, so local changes on the VM stop the deployment instead
of being overwritten.

Caddy obtains an HTTPS certificate after DNS and ports are ready. The website
becomes available at `https://your-project.duckdns.org`. The Go server derives
its public origin from `ORCH_DOMAIN` in this Compose deployment.

## 5. Verify before invitations

- Create an email account, follow its verification link, and sign out/in.
- Use password recovery and confirm the old password/session no longer works.
- Sign in with Google using the same verified address and confirm the same workspace.
- Connect ChatGPT, approve the device code on OpenAI's site, and submit an AI plan.
- Use a second account and confirm neither account sees the other's saved jobs.
- Restart the stack and confirm saved jobs, application state, and schedules return.
- Back up PostgreSQL and the user-data volume; the latter contains sensitive tokens.

The automated tests cover account/session behavior, workspace isolation, and
restart persistence. They do not replace real Google consent, SMTP delivery,
ChatGPT approval, or a smoke test of the actual hosting VM.
