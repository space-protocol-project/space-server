import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { request, proof } from "../internal/spaceweb/assets/identity.mjs";
import {
  startPairing,
  observeProposal,
  verifyPairing,
  verificationCode,
} from "../internal/spaceweb/assets/pairing.mjs";

const raw = (value) => Uint8Array.from(atob(value), (c) => c.charCodeAt(0));
async function fileJSON(path) {
  for (let i = 0; i < 200; i++) {
    try {
      return JSON.parse(await readFile(path, "utf8"));
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  throw new Error("Тестовый клиент не подготовил сопряжение");
}
export async function pairingInterop(origin, discovery, owner, session) {
  const root = new Uint8Array(
    await crypto.subtle.exportKey("raw", owner.root.publicKey),
  );
  const directory = await mkdtemp(join(tmpdir(), "space-pair-test-"));
  const child = spawn(
    "dart",
    [
      "run",
      "tool/pairing_interoperability.dart",
      directory,
      origin,
      session.principalId,
    ],
    { cwd: resolve("../client"), stdio: ["ignore", "pipe", "pipe"] },
  );
  let output = "",
    errors = "";
  child.stdout.on("data", (chunk) => (output += chunk));
  child.stderr.on("data", (chunk) => (errors += chunk));
  const finished = new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0
        ? resolve()
        : reject(new Error("Dart pairing test failed: " + errors)),
    );
  });
  finished.catch(() => {});
  try {
    const started = await fileJSON(join(directory, "request.json"));
    const inspected = (
      await request(origin, "/api/v1/auth/pairings/inspect", {
        code: started.code,
      })
    ).pairing;
    await request(
      origin,
      "/api/v1/auth/pairings/propose",
      { pairingId: inspected.id },
      session.accessToken,
    );
    const verification = await verificationCode(
      discovery.server_id,
      inspected.id,
      root,
      raw(inspected.publicKey),
      !!inspected.administrative,
    );
    const targetCheck = await fileJSON(join(directory, "check.json"));
    assert.equal(targetCheck.verification, verification);
    const pub = await crypto.subtle.importKey(
      "raw",
      raw(inspected.publicKey),
      "Ed25519",
      true,
      ["verify"],
    );
    await proof(
      origin,
      discovery,
      { ...owner, device: { publicKey: pub } },
      "device.register",
      "",
      "",
      {
        pairingId: inspected.id,
        administrative: false,
        expectedScopes: ["chat.read", "chat.write"],
      },
    );
    await finished;
    process.stdout.write(output);
    await assert.rejects(
      request(origin, "/api/v1/auth/pairings/inspect", { code: started.code }),
      (e) => e.status === 404,
    );
  } finally {
    if (child.exitCode === null) child.kill();
    await rm(directory, { recursive: true, force: true });
  }
  // Обратное направление: новый браузер получает криптографически проверяемое разрешение.
  const pending = await startPairing(origin, true);
  const p = pending.request.pairing;
  await request(
    origin,
    "/api/v1/auth/pairings/propose",
    { pairingId: p.id },
    session.accessToken,
  );
  const proposed = await request(origin, "/api/v1/auth/pairings/poll", {
    pollToken: pending.request.pollToken,
  });
  const browserCode = await observeProposal(discovery, pending, proposed);
  assert.equal(
    browserCode,
    await verificationCode(
      discovery.server_id,
      p.id,
      root,
      raw(p.publicKey),
      true,
    ),
  );
  await proof(
    origin,
    discovery,
    { ...owner, device: pending.device },
    "device.register",
    "",
    "",
    { pairingId: p.id, administrative: true },
  );
  const response = await request(origin, "/api/v1/auth/pairings/poll", {
    pollToken: pending.request.pollToken,
  });
  const paired = await verifyPairing(origin, discovery, pending, response);
  const forged = {
    ...response,
    signature: btoa(String.fromCharCode(...new Uint8Array(64))),
  };
  await assert.rejects(verifyPairing(origin, discovery, pending, forged));
  const login = await proof(
    origin,
    discovery,
    paired.record,
    "auth.login",
    paired.record.grantId,
  );
  assert.equal(login.principalId, session.principalId);
  await request(
    origin,
    "/api/v1/auth/pairings/claim",
    { pairingId: p.id },
    login.accessToken,
  );
  await request(origin, "/api/v1/space/settings", undefined, login.accessToken);
  await assert.rejects(
    request(origin, "/api/v1/auth/pairings/poll", {
      pollToken: pending.request.pollToken,
    }),
    (e) => e.status === 404,
  );
  await request(
    origin,
    "/api/v1/auth/devices/current/revoke",
    {},
    login.accessToken,
  );
  console.log(
    "WebCrypto/Go: pairing браузера, независимая root-подпись, management scope, одноразовость и подмена подписи — успешно.",
  );
  const nativeSourceTarget = await startPairing(origin, true);
  const sourceDirectory = await mkdtemp(join(tmpdir(), "space-pair-source-"));
  const sourceChild = spawn(
    "dart",
    [
      "run",
      "tool/pairing_approval_interoperability.dart",
      origin,
      nativeSourceTarget.request.code,
      sourceDirectory,
    ],
    { cwd: resolve("../client"), stdio: ["ignore", "pipe", "pipe"] },
  );
  let sourceOutput = "",
    sourceErrors = "";
  sourceChild.stdout.on("data", (chunk) => (sourceOutput += chunk));
  sourceChild.stderr.on("data", (chunk) => (sourceErrors += chunk));
  const sourceFinished = new Promise((resolve, reject) => {
    sourceChild.on("error", reject);
    sourceChild.on("exit", (code) =>
      code === 0
        ? resolve()
        : reject(new Error("Native approver test failed: " + sourceErrors)),
    );
  });
  sourceFinished.catch(() => {});
  try {
    const sourceInfo = await fileJSON(join(sourceDirectory, "source.json"));
    const preflight = await request(origin, "/api/v1/auth/pairings/poll", {
      pollToken: nativeSourceTarget.request.pollToken,
    });
    const targetCode = await observeProposal(
      discovery,
      nativeSourceTarget,
      preflight,
    );
    assert.equal(targetCode, sourceInfo.verification);
    await writeFile(
      join(sourceDirectory, "confirm.json"),
      JSON.stringify({ verification: targetCode }),
    );
    await sourceFinished;
    process.stdout.write(sourceOutput);
    const signed = await request(origin, "/api/v1/auth/pairings/poll", {
      pollToken: nativeSourceTarget.request.pollToken,
    });
    const verified = await verifyPairing(
      origin,
      discovery,
      nativeSourceTarget,
      signed,
    );
    const nativeLogin = await proof(
      origin,
      discovery,
      verified.record,
      "auth.login",
      verified.record.grantId,
    );
    assert.equal(nativeLogin.principalId, sourceInfo.principalId);
    await request(
      origin,
      "/api/v1/auth/pairings/claim",
      { pairingId: signed.pairing.id },
      nativeLogin.accessToken,
    );
    const member = await request(
      origin,
      "/api/v1/membership",
      undefined,
      nativeLogin.accessToken,
    );
    assert.equal(member.member.role, "member");
    await assert.rejects(
      request(
        origin,
        "/api/v1/space/settings",
        undefined,
        nativeLogin.accessToken,
      ),
      (e) => e.status === 403,
    );
    await request(
      origin,
      "/api/v1/auth/devices/current/revoke",
      {},
      nativeLogin.accessToken,
    );
  } finally {
    if (sourceChild.exitCode === null) sourceChild.kill();
    await rm(sourceDirectory, { recursive: true, force: true });
  }
}
