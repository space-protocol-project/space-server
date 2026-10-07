import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { promisify } from "node:util";
import { execFile } from "node:child_process";
import {
  newKeys,
  proof,
  request,
} from "../internal/spaceweb/assets/identity.mjs";
import {
  createRecoveryCard,
  openCard,
  keysFromCard,
} from "../internal/spaceweb/assets/recovery.mjs";

export async function recoveryInterop(origin, discovery, owner, ownerSession) {
  const pass = "CI recovery password 2026";
  const packet = await createRecoveryCard(
    owner,
    origin,
    discovery,
    pass,
    (keys) =>
      proof(origin, discovery, keys, "device.register", "", "", {
        recovery: true,
      }),
  );
  await assert.rejects(
    openCard(packet, "different CI password"),
    /Неверный пароль/,
  );
  const payload = await openCard(packet, pass);
  const recovered = await keysFromCard(payload, origin, discovery);
  const grant = await proof(
    origin,
    discovery,
    recovered,
    "device.register",
    "",
  );
  recovered.grantId = grant.grantId;
  assert.equal(grant.principalId, ownerSession.principalId);
  const session = await proof(
    origin,
    discovery,
    recovered,
    "auth.login",
    grant.grantId,
  );
  await request(
    origin,
    "/api/v1/space/settings",
    undefined,
    session.accessToken,
  );
  const directory = await mkdtemp(join(tmpdir(), "space-recovery-test-"));
  try {
    const cardPath = join(directory, "card.json");
    await writeFile(cardPath, packet);
    const result = await promisify(execFile)(
      "dart",
      [
        "run",
        "tool/recovery_interoperability.dart",
        cardPath,
        pass,
        ownerSession.principalId,
      ],
      { cwd: resolve("../client"), timeout: 90000, maxBuffer: 16384 },
    );
    process.stdout.write(result.stdout);
    assert.deepEqual(
      await openCard(await readFile(cardPath + ".dart.json", "utf8"), pass),
      payload,
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
  const otherKeys = { ...owner, device: (await newKeys()).device };
  const other = await proof(
    origin,
    discovery,
    otherKeys,
    "device.register",
    "",
  );
  await proof(
    origin,
    discovery,
    { ...recovered, device: otherKeys.device },
    "device.revoke",
    other.grantId,
  );
  const all = await request(
    origin,
    "/api/v1/auth/devices",
    undefined,
    ownerSession.accessToken,
  );
  assert.ok(all.devices.some((g) => g.id === other.grantId && g.revoked));
  const parent = all.devices.find((g) => g.id === payload.recovery_grant_id);
  assert.ok(parent.recovery);
  const publicKey = await crypto.subtle.importKey(
    "raw",
    Uint8Array.from(atob(parent.publicKey), (c) => c.charCodeAt(0)),
    "Ed25519",
    true,
    ["verify"],
  );
  await proof(
    origin,
    discovery,
    { ...owner, device: { publicKey } },
    "device.revoke",
    parent.id,
    "",
    { expectedScopes: parent.scopes },
  );
  await assert.rejects(
    request(origin, "/api/v1/space/settings", undefined, session.accessToken),
    (error) => error.status === 401,
  );
  await assert.rejects(
    proof(
      origin,
      discovery,
      await keysFromCard(payload, origin, discovery),
      "device.register",
      "",
    ),
    (error) => error.status === 401,
  );
  console.log(
    "WebCrypto/Go: зашифрованная карточка, прежний owner, новые ключи, отзыв устройства и карточки — успешно.",
  );
}
