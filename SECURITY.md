# Security policy

Rebound holds your email and Resend API keys, so security reports are taken seriously.

**Please don't open a public issue for vulnerabilities.** Report them privately through
[GitHub Security Advisories](https://github.com/vrlda/rebound/security/advisories/new).
You'll get a response within a few days.

## Security model in short

- Single user. Password (bcrypt) + **mandatory TOTP 2FA**, with replay protection.
- Login attempts are rate-limited per IP and globally.
- Session cookie: `HttpOnly`, `Secure`, `SameSite=Strict`; state-changing requests also require a custom header (CSRF).
- Resend API keys, the TOTP secret and the VAPID private key are encrypted at rest (AES-256-GCM) with a key generated on first start (`/data/master.key`).
- Email HTML is rendered in a sandboxed iframe with **no scripts**; remote images are blocked until you choose to load them; links open without a referrer.
- Strict Content-Security-Policy, `X-Frame-Options: DENY`, `noindex`.
- Rebound trusts `X-Real-IP` for rate limiting — always run it behind a reverse proxy that sets it (the bundled Caddy setup does). Don't expose port 8080 directly to the internet.
