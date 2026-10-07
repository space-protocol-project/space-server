const encoder = new TextEncoder();
export const base64 = (bytes) =>
  btoa(String.fromCharCode(...new Uint8Array(bytes)));
export const url64 = (bytes) =>
  base64(bytes).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
const hex = (bytes) =>
  Array.from(new Uint8Array(bytes), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
export async function request(
  origin,
  path,
  body,
  token = "",
  method = body === undefined ? "GET" : "POST",
) {
  const headers = { "Content-Type": "application/json" };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(origin + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: "error",
    cache: "no-store",
    credentials: "omit",
    signal: AbortSignal.timeout(10000),
  });
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(data.message || `HTTP ${response.status}`);
    error.status = response.status;
    throw error;
  }
  return data;
}
export async function newKeys() {
  const root = await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ]);
  const device = await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ]);
  return { root, device, grantId: "" };
}
export async function proof(
  origin,
  discovery,
  keys,
  purpose,
  grantId,
  invitationToken = "",
) {
  const root = new Uint8Array(
    await crypto.subtle.exportKey("raw", keys.root.publicKey),
  );
  const device = new Uint8Array(
    await crypto.subtle.exportKey("raw", keys.device.publicKey),
  );
  const input = { purpose };
  if (purpose === "device.register") {
    input.rootPublicKey = base64(root);
    input.devicePublicKey = base64(device);
    input.administrative = true;
  } else {
    input.grantId = grantId;
  }
  const response = await request(origin, "/api/v1/auth/challenges", input);
  const raw = atob(response.transcript);
  const transcript = JSON.parse(raw);
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
  const names = Object.keys(transcript).sort();
  const canonical = JSON.stringify(
    Object.fromEntries(names.map((key) => [key, transcript[key]])),
  );
  const principal = `u_${hex(await crypto.subtle.digest("SHA-256", root))}`;
  const now = Math.floor(Date.now() / 1000);
  const valid =
    raw === canonical &&
    JSON.stringify(names) === JSON.stringify(expected) &&
    transcript.v === 1 &&
    transcript.auth_epoch === 1 &&
    transcript.origin === origin &&
    transcript.server_id === discovery.server_id &&
    transcript.purpose === purpose &&
    transcript.principal_id === principal &&
    transcript.root_public_key === url64(root) &&
    transcript.device_public_key === url64(device) &&
    transcript.challenge_id === response.challengeId &&
    /^ac_[A-Za-z0-9_-]{43}$/.test(response.challengeId) &&
    /^dg_[A-Za-z0-9_-]{43}$/.test(transcript.grant_id) &&
    /^[A-Za-z0-9_-]{43}$/.test(transcript.nonce) &&
    JSON.stringify(transcript.scopes) ===
      '["chat.read","chat.write","space.manage"]' &&
    transcript.expires_at === transcript.issued_at + 60 &&
    transcript.expires_at > now &&
    transcript.issued_at <= now + 5 &&
    (purpose === "device.register"
      ? transcript.grant_expires_at === transcript.issued_at + 30 * 86400
      : transcript.grant_id === grantId);
  if (!valid)
    throw new Error("Проверка challenge не пройдена. Ключ не использован.");
  const prefixes = {
    "device.register": "space/device-register/v1",
    "auth.login": "space/auth-login/v1",
  };
  const signature = await crypto.subtle.sign(
    "Ed25519",
    purpose === "auth.login" ? keys.device.privateKey : keys.root.privateKey,
    encoder.encode(prefixes[purpose] + "\0" + canonical),
  );
  const result = await request(origin, "/api/v1/auth/sessions", {
    challengeId: response.challengeId,
    signature: base64(signature),
    invitationToken: purpose === "device.register" ? invitationToken : "",
  });
  if (
    result.principalId !== principal ||
    result.grantId !== transcript.grant_id
  )
    throw new Error("Ответ не соответствует идентичности");
  if (purpose === "auth.login") {
    const expiry = Number(result.expiresAt);
    if (
      !/^st_[A-Za-z0-9_-]{43}$/.test(result.accessToken) ||
      !Number.isSafeInteger(expiry) ||
      expiry <= now ||
      expiry > Math.floor(Date.now() / 1000) + 605 ||
      expiry > transcript.grant_expires_at
    )
      throw new Error("Ответ содержит недействительную сессию");
  }
  return result;
}
