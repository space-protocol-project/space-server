import assert from "node:assert/strict";
import { recoveryInterop } from "./recovery-interop.mjs";
import {
  request,
  newKeys,
  proof,
} from "../internal/spaceweb/assets/identity.mjs";
const origin = process.env.SPACE_ADMIN_ORIGIN || "http://127.0.0.1:18080";
const code = process.env.SPACE_SETUP_CODE;
if (!code) throw new Error("Нужен тестовый setup code через environment");
const discovery = await (
  await fetch(origin + "/.well-known/space-protocol")
).json();
const keys = await newKeys();
assert.equal(keys.root.privateKey.extractable, false);
const grant = await proof(origin, discovery, keys, "device.register", "");
keys.grantId = grant.grantId;
const session = await proof(
  origin,
  discovery,
  keys,
  "auth.login",
  keys.grantId,
);
const result = await request(
  origin,
  "/api/v1/space/setup/claim",
  { setupCode: code },
  session.accessToken,
);
assert.equal(result.settings.revision, "2");
const updated = await request(
  origin,
  "/api/v1/space/settings",
  {
    title: "Проверка браузерного владельца",
    chatTitle: "Чат мастерской",
    chatEnabled: true,
    registrationPolicy: "open",
    expectedRevision: result.settings.revision,
  },
  session.accessToken,
  "PATCH",
);
assert.equal(updated.settings.revision, "3");
const manifest = await request(origin, "/api/v1/manifest");
assert.equal(manifest.title, "Проверка браузерного владельца");
const html = await fetch(origin + "/space");
assert.equal(html.status, 200);
assert.ok(
  html.headers
    .get("content-security-policy")
    .includes("frame-ancestors 'none'"),
);
const denied = await fetch(origin + "/api/v1/space/settings");
assert.equal(denied.status, 401);
const invitation = await request(
  origin,
  "/api/v1/space/invites",
  { role: "reader", ttlSeconds: 3600, maxUses: 1 },
  session.accessToken,
);
await request(
  origin,
  "/api/v1/space/settings",
  {
    title: updated.settings.title,
    chatTitle: updated.settings.chatTitle,
    chatEnabled: true,
    registrationPolicy: "closed",
    expectedRevision: updated.settings.revision,
  },
  session.accessToken,
  "PATCH",
);
const readerKeys = await newKeys();
const readerGrant = await proof(
  origin,
  discovery,
  readerKeys,
  "device.register",
  "",
  invitation.token,
);
const readerSession = await proof(
  origin,
  discovery,
  readerKeys,
  "auth.login",
  readerGrant.grantId,
);
const member = await request(
  origin,
  "/api/v1/membership",
  undefined,
  readerSession.accessToken,
);
assert.equal(member.member.role, "reader");
await request(
  origin,
  "/api/v1/channels/general/content",
  undefined,
  readerSession.accessToken,
);
await assert.rejects(
  request(
    origin,
    "/api/v1/channels/general/content",
    { text: "Читатель не пишет", idempotencyKey: "reader-denied" },
    readerSession.accessToken,
  ),
  (error) => error.status === 403,
);
await assert.rejects(
  request(
    origin,
    "/api/v1/space/invites",
    { role: "member", ttlSeconds: 3600, maxUses: 1 },
    readerSession.accessToken,
  ),
  (error) => error.status === 403,
);
const latest = await request(
  origin,
  "/api/v1/space/settings",
  undefined,
  session.accessToken,
);
await request(
  origin,
  "/api/v1/space/settings",
  {
    title: latest.settings.title,
    chatTitle: latest.settings.chatTitle,
    chatEnabled: true,
    registrationPolicy: "open",
    expectedRevision: latest.settings.revision,
  },
  session.accessToken,
  "PATCH",
);
await recoveryInterop(origin, discovery, keys, session);
await request(origin, "/api/v1/auth/logout", {}, session.accessToken);
let refused = false;
try {
  await request(
    origin,
    "/api/v1/space/settings",
    undefined,
    session.accessToken,
  );
} catch (error) {
  refused = error.status === 401;
}
assert.ok(refused);
console.log(
  "WebCrypto/Go: administrative grant, login, bootstrap, settings, manifest и отказ гостю — успешно.",
);
