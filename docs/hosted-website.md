# Public website with saved accounts

Visitors use a browser. They do not clone this repository, install Docker, supply
an API key, or sign in to Docker. Docker and Codex run on the hosting server.

## Visitor flow

1. Continue with Google. The first sign-in creates a website account.
2. Open the ChatGPT connection panel and choose **Connect ChatGPT**.
3. Follow the OpenAI link, enter the displayed code, and approve the connection.
   Device-code login may need to be enabled in the user's ChatGPT security settings.
4. Return to the dashboard. Natural-language requests use that account's Codex
   connection to create plans. The account's ChatGPT/Codex limits apply.
5. Jobs, job postings, application status, workflows, and results are saved in
   the account's PostgreSQL workspace. Scheduled monitors run while the browser
   is closed and are restored when the hosting server restarts.

Website sign-out revokes the browser session. Disconnecting ChatGPT clears the
Codex connection but preserves saved jobs and existing scheduled monitors. Users
can disable those monitors in the dashboard. Reconnecting does not create a new
website account or discard saved data.

## Hosting setup

This implementation uses Google sign-in by default. You need a Google **Web
application** OAuth client. Register this exact authorized redirect URI:

```
https://YOUR_DOMAIN/auth/google/callback
```

Configure the Google consent screen for your intended audience. During Google's
testing mode, add your testers to its test-user list. Request only the basic
`openid email profile` scopes. The website gets identity from Google's
authenticated userinfo endpoint and uses the stable Google subject as account
identity, rather than trusting browser-provided email fields.

On a Linux server with Docker Compose, point your domain's DNS at the server and
allow inbound ports 80 and 443. Put these values in a private `.env` file:

```dotenv
ORCH_DOMAIN=jobs.example.com
ORCH_GOOGLE_CLIENT_ID=your-web-client-id
ORCH_GOOGLE_CLIENT_SECRET=your-web-client-secret
ORCH_HOSTED_DB_PASSWORD=a-long-random-hex-password
ORCH_MAX_USERS=5
```

Then run:

```sh
docker compose -f compose.hosted.yaml config --quiet
docker compose -f compose.hosted.yaml up -d --build
docker compose -f compose.hosted.yaml ps
```

Caddy obtains and renews HTTPS certificates. The database and application ports
are private to the Compose network. The public deployment uses its own Compose
project, database, and volumes; existing private-instance data is not assigned
to new users. Neither the private instance's API key nor its ChatGPT credentials
are used by the hosted service.

Deploy one website instance. A PostgreSQL advisory lock rejects a second hosted
instance for the same database. The website contains the workers and scheduler
for this initial deployment; do not attach the legacy worker container to it.

Back up **both** the database volume and `user-data` volume. The latter contains
sensitive Codex OAuth credentials, stored in separate directories with owner-only
permissions. Protect the hosting disk and backups; these files are not encrypted
by this application. Never publish them, mount them into another user's process,
or include them in source control. `docker compose down` preserves volumes;
`down -v` deletes saved accounts, jobs, and connections.

## Local development

Run PostgreSQL, install the pinned Codex CLI on the development server, create a
Google Web OAuth client with `http://localhost:5173/auth/google/callback`, and set:

```dotenv
ORCH_PUBLIC_URL=http://localhost:5173
ORCH_DATABASE_URL=postgres://orchestrator:orchestrator@localhost:5432/orchestrator?sslmode=disable
ORCH_GOOGLE_CLIENT_ID=your-web-client-id
ORCH_GOOGLE_CLIENT_SECRET=your-web-client-secret
ORCH_USER_DATA_DIR=/tmp/orchestrator-local-users
```

Use a durable user-data directory for actual use; `/tmp` is only for development.
Export those environment variables, then run `go run ./cmd/hosted` from `backend`
and `npm run dev` from `frontend`. The Vite proxy forwards `/auth` and `/v1` to the
backend. Use the exact port configured in `ORCH_PUBLIC_URL`; origin checks reject
mutations from other ports. Alternatively build the frontend and use port 8080
for both the public URL and registered callback.

The existing `make website` and `compose.yaml` still run the private token-based
application. Hosted mode is the separate `cmd/hosted` binary / `hosted` Docker
build target. The frontend detects which server is running.

## Boundaries of this first hosted version

- This is a small, bounded hosted deployment, not an unbounded SaaS service.
  Signup capacity defaults to five accounts. `ORCH_MAX_USERS` may be set from 1
  to 25, but database connections and server memory must be provisioned first.
  Each account has its own database schema, worker, scheduler, and Codex process.
  A larger service should share bounded connection pools and provision isolated
  workers on demand before lifting the cap. Existing accounts are never removed
  by lowering the signup limit.
- ChatGPT connects through the official Codex app-server device-code protocol,
  over private stdio. It is not a general “Sign in with ChatGPT” identity provider
  or a way to use ChatGPT subscriptions as OpenAI API keys. The integration is
  pinned to Codex 0.154.0; retest protocol and sandbox behavior before upgrades.
  OpenAI documents app-server/remote transports as evolving; validate support
  expectations before a broad commercial launch.
- AI produces structured plans for the app's existing job types. This does not
  add arbitrary coding-agent execution or automatic job applications. Some
  existing demonstration job types still simulate work. Hosted SMTP delivery is
  not enabled; saved monitor results are available in the dashboard.
- The planner uses a clean credential directory, disables shell, plugins, apps,
  multi-agent and web-search tools, restricts filesystem reads to an empty
  workspace, and declines server-initiated tool/approval requests. No raw Codex
  protocol or credentials are exposed through the website API.
- Sessions are opaque, hashed in the database, expire after seven days, and use
  HttpOnly/SameSite cookies (Secure on HTTPS). Mutations require an exact Origin.
  Legacy bearer tokens cannot access hosted workspaces. Job HTTP requests reject
  private/reserved IPs at dial time, including redirect targets and DNS rebinding.
- Per-account mutation rate limits and a signup cap are included. This version
  does not include billing, user-managed account deletion, account merging,
  administrator UI, storage retention policies, or a shared public jobs feed.
  Plan those before opening unlimited registration.

## Verification

```sh
cd backend
go test ./...
go test -race ./internal/hosted ./internal/codex ./internal/llm
ORCH_TEST_CODEX=1 go test ./internal/codex -run TestRealCodexHandshake -v
# Use a disposable database; the hosted integration tests create their own schemas.
ORCH_TEST_DATABASE_URL=postgres://... go test ./internal/hosted -run TestPostgresHosted -v
cd ../frontend
npm run build
```

Before inviting users, exercise Google sign-in and ChatGPT device-code approval
with two real accounts, create a monitor in each, restart the deployment, and
verify both accounts retain only their own data. These interactive provider
approvals require the account owners.

References: [OpenAI app-server authentication](https://developers.openai.com/codex/app-server#auth-endpoints),
[Google web OAuth](https://developers.google.com/identity/protocols/oauth2/web-server),
[Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect).
