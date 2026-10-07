import { request, newKeys, proof } from "./identity.mjs";
const $ = (selector) => document.querySelector(selector);
const origin = location.origin;
let token = "",
  expires = 0,
  record,
  discovery,
  config,
  busy = false;
const status = (message) => {
  $("#status").textContent = message;
};
function pending(value) {
  busy = value;
  document
    .querySelectorAll("button")
    .forEach((button) => (button.disabled = value));
}
async function run(action) {
  if (busy) return;
  pending(true);
  try {
    await action();
  } catch (error) {
    status(
      error.status === 403
        ? "Нет права управления или код первоначальной настройки недействителен."
        : error.message,
    );
  } finally {
    pending(false);
  }
}
function database() {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open("space-panel-identity-v1", 1);
    open.onupgradeneeded = () => open.result.createObjectStore("keys");
    open.onsuccess = () => resolve(open.result);
    open.onerror = () => reject(open.error);
  });
}
async function vault(value) {
  const db = await database();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction("keys", value ? "readwrite" : "readonly");
      const store = tx.objectStore("keys");
      let result;
      const operation = value ? store.put(value, origin) : store.get(origin);
      operation.onsuccess = () => {
        result = operation.result;
      };
      tx.oncomplete = () => resolve(result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
  } finally {
    db.close();
  }
}
async function renew() {
  const session = await proof(
    origin,
    discovery,
    record,
    "auth.login",
    record.grantId,
  );
  token = session.accessToken;
  expires = Number(session.expiresAt);
}
async function api(path, body, method) {
  if (!token) throw new Error("Сначала войдите по ключу");
  if (Date.now() / 1000 > expires - 15) await renew();
  return request(origin, path, body, token, method);
}
function display(settings) {
  config = settings;
  $("#title").value = settings.title;
  $("#chat-title").value = settings.chatTitle;
  $("#chat-enabled").checked = !!settings.chatEnabled;
  $("#registration-policy").value = settings.registrationPolicy;
  $("#revision").textContent = `Версия ${settings.revision}`;
  $("#claim").hidden = true;
  $("#login").hidden = true;
  $("#management").hidden = false;
}
async function load() {
  const result = await api("/api/v1/space/settings");
  display(result.settings);
  status("Настройки загружены.");
}
async function signIn(forceGrant = false) {
  if (!isSecureContext || !crypto.subtle || !indexedDB)
    throw new Error("Нужны современный браузер и локальный безопасный origin.");
  const response = await fetch("/.well-known/space-protocol", {
    cache: "no-store",
    redirect: "error",
  });
  if (!response.ok) throw new Error("Discovery недоступен");
  discovery = await response.json();
  if (
    discovery.protocol_version !== "0.1-experimental" ||
    discovery.signing_algorithm !== "Ed25519" ||
    !/^srv_[0-9a-f]{32}$/.test(discovery.server_id) ||
    !/^[A-Za-z0-9_-]{43}$/.test(discovery.signing_public_key)
  )
    throw new Error("Нужен совместимый сервер с PostgreSQL");
  record = await vault();
  if (
    record &&
    (record.serverId !== discovery.server_id ||
      record.serverKey !== discovery.signing_public_key)
  )
    throw new Error("Серверная идентичность изменилась. Вход заблокирован.");
  if (!record) {
    if (
      !confirm(
        "Создать административные ключи для этого сервера в текущем браузере? Восстановление пока не поддерживается.",
      )
    )
      return;
    record = {
      ...(await newKeys()),
      serverId: discovery.server_id,
      serverKey: discovery.signing_public_key,
    };
    await vault(record);
  }
  if (forceGrant || !record.grantId) {
    const grant = await proof(origin, discovery, record, "device.register", "");
    record.grantId = grant.grantId;
    await vault(record);
  }
  await renew();
  const setup = await request(origin, "/api/v1/space/setup");
  if (!setup.initialized) {
    $("#claim").hidden = false;
    status("Устройство вошло. Для роли владельца нужен код сервера.");
  } else {
    await load();
  }
}
$("#sign-in").addEventListener("click", () => run(() => signIn()));
$("#renew-grant").addEventListener("click", () => {
  if (
    confirm("Явно разрешить этому устройству управление через корневой ключ?")
  )
    run(() => signIn(true));
});
$("#claim-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    const code = $("#setup-code").value;
    $("#setup-code").value = "";
    const result = await api("/api/v1/space/setup/claim", { setupCode: code });
    display(result.settings);
    status("Владелец назначен. Код использован один раз.");
  });
});
$("#settings-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    if (!config) throw new Error("Сначала загрузите настройки");
    if (
      config.chatEnabled &&
      !$("#chat-enabled").checked &&
      !confirm(
        "Закрыть общий чат? Подписки участников завершатся; сообщения не удаляются.",
      )
    )
      return;
    try {
      const result = await api(
        "/api/v1/space/settings",
        {
          title: $("#title").value,
          chatTitle: $("#chat-title").value,
          chatEnabled: $("#chat-enabled").checked,
          registrationPolicy: $("#registration-policy").value,
          expectedRevision: config.revision,
        },
        "PATCH",
      );
      display(result.settings);
      status("Настройки сохранены на сервере.");
    } catch (error) {
      if (error.status === 409)
        throw new Error(
          "Другой запрос уже изменил настройки. Нажмите «Загрузить заново» перед повтором.",
        );
      throw error;
    }
  });
});
$("#reload").addEventListener("click", () => run(load));
$("#sign-out").addEventListener("click", () =>
  run(async () => {
    try {
      await api("/api/v1/auth/logout", {});
    } catch (error) {
      if (error.status !== 401) throw error;
    }
    token = "";
    expires = 0;
    config = null;
    $("#management").hidden = true;
    $("#claim").hidden = true;
    $("#login").hidden = false;
    status("Сессия закрыта на сервере. Ключи сохранены в браузере.");
  }),
);
request(origin, "/api/v1/space/setup")
  .then((result) => {
    $("#login-title").textContent = result.initialized
      ? "Вход владельца"
      : "Первый запуск пространства";
  })
  .catch(() => status("Для рабочей панели нужен сервер с PostgreSQL."));
