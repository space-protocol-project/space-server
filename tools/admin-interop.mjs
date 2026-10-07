import assert from "node:assert/strict";
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
