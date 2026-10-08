import {verifyRootHistory} from './root-history.mjs';
import { request, url64, base64 } from "./identity.mjs";
const encoder = new TextEncoder();
const bytes = (value) => Uint8Array.from(atob(value), (c) => c.charCodeAt(0));
export async function verificationCode(
  serverId,
  pairId,
  root,
  device,
  administrative = false,
) {
  const prefix = encoder.encode(
      `space/pair-check/v1\0${serverId}\0${pairId}\0${administrative ? "manage" : "chat"}\0`,
    ),
    all = new Uint8Array(prefix.length + 64);
  all.set(prefix);
  all.set(root, prefix.length);
  all.set(device, prefix.length + 32);
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", all));
  return Array.from(hash.slice(0, 8), (b) => b.toString(16).padStart(2, "0"))
    .join("")
    .toUpperCase()
    .match(/.{4}/g)
    .join("-");
}
export async function startPairing(origin, administrative = true) {
  const device = await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ]);
  const key = await crypto.subtle.exportKey("raw", device.publicKey);
  const requestData = await request(origin, "/api/v1/auth/pairings", {
    publicKey: base64(key),
    deviceName: "Новый браузер",
    administrative,
  });
  const p = requestData.pairing,
    now = Math.floor(Date.now() / 1000);
  if (
    !/^pr_[A-Za-z0-9_-]{43}$/.test(p.id) ||
    !/^pc_[A-Za-z0-9_-]{43}$/.test(requestData.code) ||
    !/^pt_[A-Za-z0-9_-]{43}$/.test(requestData.pollToken) ||
    p.state !== "pending" ||
    !!p.administrative !== administrative ||
    url64(bytes(p.publicKey)) !== url64(key) ||
    Number(p.expiresAt) !== Number(p.createdAt) + 300 ||
    Number(p.createdAt) > now + 5 ||
    Number(p.createdAt) < now - 15
  )
    throw new Error("Запрос сопряжения не соответствует устройству");
  return { device, request: requestData };
}
export async function verifyPairing(origin, discovery, pending, response) {
  const p = response.pairing,
    expected = pending.request.pairing,
    root = bytes(response.rootPublicKey),
    device = bytes(p.publicKey);
  if (
    p.id !== expected.id ||
    p.state !== "approved" ||
    p.expiresAt !== expected.expiresAt ||
    p.createdAt !== expected.createdAt ||
    !!p.administrative !== !!expected.administrative ||
    p.publicKey !== expected.publicKey ||
    Number(p.expiresAt) <= Date.now() / 1000 ||
    root.length !== 32 ||
    device.length !== 32 ||
    !pending.proposedRoot ||
    url64(pending.proposedRoot) !== url64(root) ||
    response.rootPublicKey !== p.proposedRootPublicKey
  )
    throw new Error("Сопряжение изменено или истекло");
  const raw = atob(response.transcript),
    t = JSON.parse(raw),
    names = Object.keys(t).sort();
  const wanted = [
    "auth_epoch",
    "challenge_id",
    "device_public_key",
    "expires_at",
    "grant_expires_at",
    "grant_id",
    "issued_at",
    "nonce",
    "origin",
    "pairing_id",
    "principal_id",
    "purpose",
    "root_public_key",
    "scopes",
    "server_id",
    "v",
  ];
  const canonical = JSON.stringify(
    Object.fromEntries(names.map((n) => [n, t[n]])),
  );
  const delegated = t.purpose === "device.delegate";
  if (delegated) wanted.splice(1, 0, "authorizer_grant_id");
  const history=(response.rootHistory || []).map(p=>({transcript:url64(bytes(p.transcript)),old_signature:url64(bytes(p.oldSignature)),new_signature:url64(bytes(p.newSignature))}));
  const identity=await verifyRootHistory(history,root,origin,discovery.server_id);
  const principal=identity.principalId;
  if (
    raw !== canonical ||
    JSON.stringify(names) !== JSON.stringify(wanted) ||
    t.auth_epoch !== identity.epoch ||
    t.v !== 1 ||
    t.purpose !== (delegated ? "device.delegate" : "device.register") ||
    t.pairing_id !== p.id ||
    t.origin !== origin ||
    t.server_id !== discovery.server_id ||
    t.root_public_key !== url64(root) ||
    t.device_public_key !== url64(device) ||
    t.principal_id !== principal ||
    t.grant_id !== response.grantId ||
    !/^dg_[A-Za-z0-9_-]{43}$/.test(t.grant_id) ||
    !/^ac_[A-Za-z0-9_-]{43}$/.test(t.challenge_id) ||
    !/^[A-Za-z0-9_-]{43}$/.test(t.nonce) ||
    JSON.stringify(t.scopes) !==
      JSON.stringify([
        "chat.read",
        "chat.write",
        ...(p.administrative ? ["space.manage"] : []),
      ]) ||
    t.expires_at !== t.issued_at + 60 ||
    (delegated
      ? t.grant_expires_at > t.issued_at + 30 * 86400 ||
        t.grant_expires_at <= t.expires_at
      : t.grant_expires_at !== t.issued_at + 30 * 86400) ||
    t.issued_at < Number(p.createdAt) - 5 ||
    t.issued_at >= Number(p.expiresAt) ||
    t.issued_at > Date.now() / 1000 + 5
  )
    throw new Error("Подтверждение не соответствует запросу");
  const publicKey = await crypto.subtle.importKey(
    "raw",
    root,
    "Ed25519",
    true,
    ["verify"],
  );
  let signer = publicKey;
  if (delegated)
    signer = await verifyRecoveryParent(origin, discovery, response, t, root, identity);
  else if (
    response.parentGrantId ||
    response.parentTranscript ||
    response.parentSignature
  )
    throw new Error("Неожиданная цепочка подписи");
  if (
    !(await crypto.subtle.verify(
      "Ed25519",
      signer,
      bytes(response.signature),
      encoder.encode(
        (delegated
          ? "space/device-delegate/v1\0"
          : "space/device-register/v1\0") + canonical,
      ),
    ))
  )
    throw new Error("Root-подпись сопряжения неверна");
  return {
    record: {
      rootHistory: history,
      root: { publicKey },
      device: pending.device,
      grantId: response.grantId,
      serverId: discovery.server_id,
      serverKey: discovery.signing_public_key,
    },
    verification: await verificationCode(
      discovery.server_id,
      p.id,
      root,
      device,
      !!p.administrative,
    ),
  };
}
async function verifyRecoveryParent(origin, discovery, response, child, root, identity) {
  if (
    !response.parentTranscript ||
    !response.parentSignature ||
    response.parentGrantId !== child.authorizer_grant_id
  )
    throw new Error("Нет доказательства recovery authority");
  const raw = atob(response.parentTranscript),
    t = JSON.parse(raw),
    keys = Object.keys(t).sort();
  const expected = [
    "auth_epoch",
    "challenge_id",
    "device_public_key",
    "expires_at",
    "grant_expires_at",
    "grant_id",
    "issued_at",
    "nonce",
    "origin",
    "principal_id",
    "purpose",
    "root_public_key",
    "scopes",
    "server_id",
    "v",
  ];
  const canonical = JSON.stringify(
      Object.fromEntries(keys.map((k) => [k, t[k]])),
    ),
    managed = t.scopes?.includes("space.manage");
  if (
    raw !== canonical ||
    JSON.stringify(keys) !== JSON.stringify(expected) ||
    !["auth_epoch", "issued_at", "expires_at", "grant_expires_at", "v"].every(
      (k) => Number.isSafeInteger(t[k]),
    ) ||
    t.v !== 1 ||
    t.auth_epoch !== identity.epoch ||
    t.purpose !== "device.register" ||
    t.origin !== origin ||
    t.server_id !== discovery.server_id ||
    t.root_public_key !== url64(root) ||
    t.principal_id !== child.principal_id ||
    t.grant_id !== response.parentGrantId ||
    !/^dg_[A-Za-z0-9_-]{43}$/.test(t.grant_id) ||
    !/^ac_[A-Za-z0-9_-]{43}$/.test(t.challenge_id) ||
    !/^[A-Za-z0-9_-]{43}$/.test(t.nonce) ||
    bytes(t.device_public_key.replaceAll("-", "+").replaceAll("_", "/"))
      .length !== 32 ||
    JSON.stringify(t.scopes) !==
      JSON.stringify([
        "chat.read",
        "chat.write",
        ...(managed ? ["space.manage"] : []),
        "identity.recover",
      ]) ||
    (child.scopes.includes("space.manage") && !managed) ||
    t.expires_at !== t.issued_at + 60 ||
    t.grant_expires_at !== t.issued_at + 3650 * 86400 ||
    t.grant_expires_at < Date.now() / 1000 ||
    t.grant_expires_at < child.grant_expires_at ||
    t.issued_at > child.issued_at + 5
  )
    throw new Error("Неподходящая цепочка recovery");
  const rootKey = await crypto.subtle.importKey("raw", root, "Ed25519", true, [
    "verify",
  ]);
  if (
    !(await crypto.subtle.verify(
      "Ed25519",
      rootKey,
      bytes(response.parentSignature),
      encoder.encode("space/device-register/v1\0" + canonical),
    ))
  )
    throw new Error("Root-подпись recovery неверна");
  return crypto.subtle.importKey(
    "raw",
    bytes(t.device_public_key.replaceAll("-", "+").replaceAll("_", "/")),
    "Ed25519",
    true,
    ["verify"],
  );
}
export async function observeProposal(discovery, pending, response) {
  const p = response.pairing,
    expected = pending.request.pairing;
  if (
    p.id !== expected.id ||
    p.createdAt !== expected.createdAt ||
    p.expiresAt !== expected.expiresAt ||
    p.publicKey !== expected.publicKey ||
    !!p.administrative !== !!expected.administrative ||
    Number(p.expiresAt) <= Date.now() / 1000
  )
    throw new Error("Запрос сопряжения изменён");
  if (!response.rootPublicKey) return "";
  const root = bytes(response.rootPublicKey);
  if (
    root.length !== 32 ||
    response.rootPublicKey !== p.proposedRootPublicKey ||
    (pending.proposedRoot && url64(pending.proposedRoot) !== url64(root))
  )
    throw new Error("Подтверждающая идентичность изменилась");
  pending.proposedRoot = root;
  return verificationCode(
    discovery.server_id,
    p.id,
    root,
    bytes(p.publicKey),
    !!p.administrative,
  );
}
