# pelosi-mirror

Mirrors Nancy Pelosi's disclosed stock trades into your Fidelity account, the way
Autopilot's "Pelosi Tracker+" portfolio does. You approve every set of orders before
anything is placed.

- Checks the House Clerk for new Periodic Transaction Reports every 10 minutes.
- Turns each disclosed trade into an updated estimate of her portfolio.
- Works out the buys and sells that bring your strategy money to the same weights, and
  sends them to you as a Discord DM with **Approve** / **Reject** buttons. They also
  show on a web page.
- When you approve, it signs in to Fidelity in its own Chrome and places the orders. It
  checks Fidelity's preview screen against the approved order before each click.

> **Read this first**
> - **Not financial advice.** Disclosures arrive up to 45 days after the trade, and the
>   dollar amounts are ranges, so the portfolio is an estimate.
> - **Pelosi leaves Congress on Jan 3, 2027.** Reports for her last trades should arrive by
>   mid-February 2027. After that there's nothing new to mirror.
> - **Automating Fidelity's website may break their terms of use**, and it breaks
>   whenever Fidelity changes the site. Every order needs your approval and is checked
>   against Fidelity's preview, so the usual failure is an order that doesn't go
>   through, not a wrong one. Still, check your account.
> - **Fidelity only**, and the account needs fractional shares (standard on Fidelity
>   brokerage accounts on the web). Buys are dollar amounts and sells are shares
>   (3 decimals). All orders are market orders, good for the day.

## How it works

| Piece | What it does |
|---|---|
| `ptr.go` | Polls the Clerk's member search, downloads new PTR PDFs, parses the trades |
| `model.go` | Keeps the portfolio estimate in dollars: a buy adds the middle of its range, a full stock sale zeroes the position, a partial sale or options sale subtracts the midpoint. **Call options count as the underlying stock.** Option exercises are skipped because the option was already counted. LLCs and partnership units (`[AB]`) are skipped. Then it plans the trades. |
| `engine.go` | Runs the loop: new filing → read the account → proposal → your approval → orders |
| `fidelity.go` | Drives Chrome over the DevTools protocol: reads positions, fills the trade ticket, checks the preview, places the order |
| `discord.go`, `web.go` | Approval by Discord DM and on a password-protected web page |

**Your money vs. the strategy's.** The strategy only trades the tickers in its model,
plus the **strategy cash** you give it. Anything else in the account is never touched,
whether that's other stocks, funds or extra cash. Shares you already own in one of
the model's tickers count as part of the strategy.

**Starting point.** `seed.json` holds the estimated portfolio, current through the
filings it lists (Nov 6, 2026). A new install starts from it and applies newer filings
by itself, so you don't need Autopilot.

## What you need

- A computer that stays on and runs Docker: a NAS, a home server, a Raspberry Pi 4/5
  with 4 GB or more, or an always-on PC. It needs about 1.5 GB of RAM for Chrome.
- A Fidelity brokerage account with phone or text 2-step verification.
- Optionally, a Discord account, for approve buttons on your phone.

## Setup

### 1. Get the code

```sh
git clone https://github.com/clarkent86/pelosi-mirror.git
cd pelosi-mirror
cp .env.example .env
```

### 2. Make a Discord bot (optional, about 5 minutes)

1. Go to <https://discord.com/developers/applications> → **New Application**. Name it,
   for example "Pelosi mirror".
2. **Bot** tab → **Reset Token** → copy the token into `.env` as `DISCORD_TOKEN`.
   No privileged intents are needed.
3. **OAuth2 → URL Generator**: tick `bot`, open the URL it gives you, and add the bot
   to any server you're in. Your own empty server is fine. The bot can only DM you if
   you share a server with it.
4. In Discord: **Settings → Advanced → Developer Mode** on. Then right-click your own
   name → **Copy User ID** → paste it into `.env` as `DISCORD_USER_ID`. Only this user
   can approve trades.

You can skip Discord: leave both blank and approve on the web page instead.

### 3. Fill in `.env`

The minimum:

| Setting | What to put |
|---|---|
| `FIDELITY_ACCOUNT` | The account to trade in, e.g. `Z12345678`. It's on Fidelity's positions page. Ideally use a dedicated account. |
| `INITIAL_CASH` | How much of that account's cash the strategy may invest, e.g. `1000` |
| `WEB_PASSWORD` | Any password, for the web page |
| `BROWSER_USER`, `BROWSER_PASSWORD` | Any username and password, for the browser desktop |
| `PUBLIC_URL`, `BROWSER_URL` | Your server's LAN address: `http://<ip>:3013` and `https://<ip>:3001` |

Recommended: `FIDELITY_USERNAME` and `FIDELITY_PASSWORD`. These let the app sign back
in by itself when Fidelity's session times out. That only works once the browser is a
trusted device (step 5).

### 4. Start it

```sh
docker compose up -d --build
docker compose logs -f mirror     # Ctrl-C to stop watching
```

You should see `pelosi-mirror listening on :3013`. If Discord is set up, you'll get a
DM soon after with what it found.

### 5. Sign in to Fidelity once

1. Open **https://\<server-ip\>:3001**. Your browser will warn about the certificate,
   because it's self-signed. Choose **Advanced → Proceed**. Sign in with
   `BROWSER_USER` / `BROWSER_PASSWORD`.
2. A Chrome window opens on Fidelity's login page. Sign in with your Fidelity username,
   password and the text-message code.
3. On the code screen, **tick "Don't ask me again on this device."** This is what lets
   the app sign back in later without a new code.
4. Leave that window alone. You can close the browser tab on your computer; Chrome keeps
   running on the server.

### 6. Check the web page

Open **http://\<server-ip\>:3013**, with any username and your `WEB_PASSWORD`. Press
**Refresh positions**. The "Strategy portfolio" table should show your holdings in the
model's tickers and the strategy cash. Your other holdings stay out of it.

### 7. Do a dry run

New installs start in **dry run**: approved orders are filled in on Fidelity and checked
against the preview, but never placed.

1. Press **Propose rebalance**. On a new install, the first proposal invests your
   strategy cash across the portfolio.
2. Approve it in Discord or on the web page. Each order takes about 30 seconds.
3. Every order should end up `previewed`. If one fails, the error says why. The **Log**
   section at the bottom of the web page shows each step.

### 8. Go live

On the web page: **Turn on live trading** → confirm → **Propose rebalance** → approve.
Approved orders are now placed for real. If you approve outside market hours, Fidelity
queues market orders for the next open. Check **Activity & Orders** on Fidelity to see
the fills.

## Day to day

- **New filing:** you get a DM listing her trades and the orders that would mirror them.
  Approve or reject it. If another filing arrives before you decide, a fresh proposal
  replaces the old one.
- **Adding money:** deposit it in Fidelity, raise **Strategy cash** on the web page, then
  press **Propose rebalance**.
- **Signed out:** if the app can't sign itself back in, you get a DM. Sign in again at
  https://\<server-ip\>:3001.
- **One proposal at a time:** if an order fails, the rest of that batch is skipped, and a
  confirmation it doesn't recognize also stops the batch. Check Fidelity, then propose
  again.

## Updating

```sh
git pull
docker compose up -d --build mirror
```

This rebuilds only the app. Restarting the `chromium` container can sign you out of
Fidelity. Chrome is started with `--restore-last-session` to make that less likely.

## Settings

| Variable | Default | |
|---|---|---|
| `FIDELITY_ACCOUNT` | (required) | Account number to trade in |
| `FIDELITY_USERNAME`, `FIDELITY_PASSWORD` | | For signing back in automatically. It never retries a failed sign-in on its own, so it can't lock your account. |
| `INITIAL_CASH` | `0` | Strategy cash on the first run (then set it on the web page) |
| `WEB_PASSWORD` | (required) | Web page password |
| `DISCORD_TOKEN`, `DISCORD_USER_ID` | | Discord approvals |
| `PUBLIC_URL`, `BROWSER_URL` | | Links in messages |
| `POLL_MINUTES` | `10` | How often to check for filings (hourly 10 PM–6 AM) |
| `MIN_TRADE` | `5` | Skip changes smaller than this ($) |
| `CASH_RESERVE` | `5` | Leave this much strategy cash uninvested ($) |
| `MEMBER_LAST`, `MEMBER_STATE` | `Pelosi`, `CA` | Whose filings to follow. Another member also needs a seed of their holdings (`SEED_FILE`, same shape as `seed.json`). |
| `SEED_FILE` | built-in `seed.json` | Starting portfolio for a new install |
| `STATE_FILE` | `/data/state.json` | Everything the app remembers. Back up `./data`. |
| `DEBUG` | off | Developer endpoints that control the signed-in browser. Leave off. |

## Security

- **Keep it on your home network.** Don't forward ports 3001 or 3013 to the internet.
  The browser desktop is a signed-in Fidelity session.
- **Private files:** `.env`, `./browser` (the Chrome profile with your Fidelity cookies)
  and `./data`. They're in `.gitignore`; keep it that way.
- **Approvals:** only `DISCORD_USER_ID` can press the buttons, and the web page needs
  `WEB_PASSWORD`.

## Development

```sh
go test ./...
go run .        # needs WEB_PASSWORD, and a Chrome with --remote-debugging-port=9222 for Fidelity
```

`testdata/` holds real PTR PDFs (public records) for the parser tests. `internal/pdf` is
a patched PDF library; see its README.
