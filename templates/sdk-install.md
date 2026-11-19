# Installing the RhymeHub SDK

This project can send bug reports and feature requests to RhymeHub from inside a managed app. This guide was
written by `rhymehub init`. It contains no keys.

## 1. Create a reporter key

Ask a project maintainer to create a key in the RhymeHub dashboard (Project → Keys). The key looks like
`rhk_...` and is shown only once.

## 2. Store the key outside the repository

Never commit the key. Put it in your deployment secret store or a local `.env` file that is git-ignored:

```
RHYMEHUB_KEY=rhk_...
RHYMEHUB_URL=https://rhymehub.example.ts.net:7780
```

## 3. Send a request

Every SDK call sends the key in the `X-Rhymehub-Key` header and targets `/api/v1/sdk/*` only:

```
POST {RHYMEHUB_URL}/api/v1/sdk/requests
X-Rhymehub-Key: rhk_...
Content-Type: application/json

{
  "type": "bug",
  "title": "Crash on start",
  "description": "What happened and what you expected",
  "installationId": "inst_<stable id for this install>",
  "appVersion": "1.2.3",
  "platform": "ios"
}
```

The response is an `SdkRequest` with `id`, `status` and `progress`. Poll `GET /api/v1/sdk/requests/{id}?installationId=...`
for updates. Only the installation that created a request can read it.

## Rules

- Keep the key on the server or in the app's secure storage. Do not ship it in public source.
- Use one `installationId` per app install and keep it stable.
- Limit is 30 requests per minute per key and installation. Body size is at most 6 MiB.
- Diagnostics are sent as `{kind, name, content}` objects. Remove personal data before sending.
