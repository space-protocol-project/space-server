import { url64 } from "./identity.mjs";
const encoder = new TextEncoder(),
  decoder = new TextDecoder("utf-8", { fatal: true });
const aad = encoder.encode("space/recovery-card/v1");
export function decode64(value, length) {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]+$/.test(value))
    throw new Error("Повреждена карточка");
  const bytes = Uint8Array.from(
    atob(value.replaceAll("-", "+").replaceAll("_", "/")),
    (c) => c.charCodeAt(0),
  );
  if (url64(bytes) !== value || (length && bytes.length !== length))
    throw new Error("Повреждена карточка");
  return bytes;
}
function passwordCheck(password) {
  if (
    typeof password !== "string" ||
    password.length < 12 ||
    encoder.encode(password).length > 512
  )
    throw new Error(
      "Используйте пароль длиной от 12 символов, например несколько случайных слов.",
    );
}
async function cardKey(password, salt) {
  passwordCheck(password);
  const raw = await crypto.subtle.importKey(
    "raw",
    encoder.encode(password),
    "PBKDF2",
    false,
    ["deriveKey"],
  );
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt, iterations: 600000 },
    raw,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}
export async function sealCard(payload, password) {
  passwordCheck(password);
  const salt = crypto.getRandomValues(new Uint8Array(16)),
    nonce = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce, additionalData: aad, tagLength: 128 },
    await cardKey(password, salt),
    encoder.encode(JSON.stringify(payload)),
  );
  return JSON.stringify({
    v: 1,
    kind: "space-recovery-card",
    kdf: "PBKDF2-SHA256",
    iterations: 600000,
    salt: url64(salt),
    nonce: url64(nonce),
    ciphertext: url64(ciphertext),
  });
}
export async function openCard(text, password) {
  passwordCheck(password);
  if (typeof text !== "string" || encoder.encode(text).length > 16384)
    throw new Error("Карточка слишком большая");
  const p = JSON.parse(text);
  if (
    p.v !== 1 ||
    p.kind !== "space-recovery-card" ||
    p.kdf !== "PBKDF2-SHA256" ||
    p.iterations !== 600000 ||
    Object.keys(p).sort().join(",") !==
      "ciphertext,iterations,kdf,kind,nonce,salt,v"
  )
    throw new Error("Неподдерживаемый формат карточки");
  const salt = decode64(p.salt, 16),
    nonce = decode64(p.nonce, 12),
    ciphertext = decode64(p.ciphertext);
  if (ciphertext.length < 16 || ciphertext.length > 8192)
    throw new Error("Повреждена карточка");
  try {
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: nonce, additionalData: aad, tagLength: 128 },
      await cardKey(password, salt),
      ciphertext,
    );
    return JSON.parse(decoder.decode(plain));
  } catch {
    throw new Error("Неверный пароль или повреждённая карточка");
  }
}
export async function importSeed(seed, publicKey) {
  const privateKey = await crypto.subtle.importKey(
    "jwk",
    {
      kty: "OKP",
      crv: "Ed25519",
      x: url64(publicKey),
      d: url64(seed),
      ext: false,
      key_ops: ["sign"],
    },
    "Ed25519",
    false,
    ["sign"],
  );
  const publicCryptoKey = await crypto.subtle.importKey(
    "raw",
    publicKey,
    "Ed25519",
    true,
    ["verify"],
  );
  const sample = encoder.encode("space/recovery-key-check/v1");
  const signature = await crypto.subtle.sign("Ed25519", privateKey, sample);
  if (
    !(await crypto.subtle.verify("Ed25519", publicCryptoKey, signature, sample))
  )
    throw new Error("Карточка содержит несовместимые ключи");
  return { publicKey: publicCryptoKey, privateKey };
}
export async function keysFromCard(payload, origin, discovery) {
  if (
    payload.v !== 1 ||
    payload.origin !== origin ||
    payload.server_id !== discovery.server_id ||
    payload.server_key !== discovery.signing_public_key ||
    !["root", "recovery"].includes(payload.credential)
  )
    throw new Error("Карточка принадлежит другому серверу или формату");
  const rootPublic = decode64(payload.root_public_key, 32),
    secret = decode64(payload.secret, 32);
  const device = await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ]);
  if (payload.credential === "root")
    return { root: await importSeed(secret, rootPublic), device, grantId: "" };
  if (
    !/^dg_[A-Za-z0-9_-]{43}$/.test(payload.recovery_grant_id) ||
    !Number.isSafeInteger(payload.expires_at) ||
    payload.expires_at <= Date.now() / 1000
  )
    throw new Error("Разрешение восстановления истекло или повреждено");
  const recovery = await importSeed(
    secret,
    decode64(payload.credential_public_key, 32),
  );
  const root = {
    publicKey: await crypto.subtle.importKey(
      "raw",
      rootPublic,
      "Ed25519",
      true,
      ["verify"],
    ),
  };
  return {
    root,
    device,
    recoveryPrivate: recovery.privateKey,
    recoveryGrantId: payload.recovery_grant_id,
    grantId: "",
  };
}
export async function createRecoveryCard(
  record,
  origin,
  discovery,
  password,
  register,
) {
  passwordCheck(password);
  if (!record.root.privateKey)
    throw new Error(
      "Для новой карточки нужен исходный root. Сохраните ранее созданную карточку.",
    );
  const recovery = await crypto.subtle.generateKey("Ed25519", true, [
    "sign",
    "verify",
  ]);
  const jwk = await crypto.subtle.exportKey("jwk", recovery.privateKey);
  const granted = await register({ ...record, device: recovery });
  const payload = {
    v: 1,
    credential: "recovery",
    origin,
    server_id: discovery.server_id,
    server_key: discovery.signing_public_key,
    root_public_key: url64(
      await crypto.subtle.exportKey("raw", record.root.publicKey),
    ),
    credential_public_key: url64(
      await crypto.subtle.exportKey("raw", recovery.publicKey),
    ),
    secret: jwk.d,
    recovery_grant_id: granted.grantId,
    expires_at: granted.recoveryExpiresAt,
  };
  return sealCard(payload, password);
}
