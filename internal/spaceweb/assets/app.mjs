import { request, newKeys, proof, url64 } from "./identity.mjs";
import { createRecoveryCard, openCard, keysFromCard } from "./recovery.mjs";
import {
  startPairing,
  observeProposal,
  verifyPairing,
  verificationCode,
} from "./pairing.mjs";
const $ = (selector) => document.querySelector(selector);
const origin = location.origin;
let currentRole = "",
  memberCursor = "",
  inviteCursor = "";
let stagedRecovery;
let stagedPairId = "",
  pendingPair,
  pairTimer,
  preparedPair,
  preparedCode = "",
  preparedAuthority;
let pollingPair = false;
let serverReady = false;
const roleLabel = (role) =>
  ({
    owner: "Владелец",
    admin: "Администратор",
    member: "Участник",
    reader: "Читатель",
  })[role] || "Неизвестная роль";
function joinToken() {
  return $("#join-token").value.trim();
}
const fragmentToken = new URLSearchParams(location.hash.slice(1)).get("invite");
if (fragmentToken) {
  $("#join-token").value = fragmentToken;
  history.replaceState(null, "", location.pathname);
}
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
    .forEach((button) => (button.disabled = value || !serverReady));
}
async function run(action) {
  if (busy) return;
  pending(true);
  try {
    await action();
  } catch (error) {
    status(
      error.status === 403
        ? "Недостаточно прав, код недействителен или приглашение больше не действует."
        : error.status === 501
          ? "Для входа, приглашений и восстановления нужен сервер с PostgreSQL."
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
  record = stagedRecovery || (await vault());
  if (
    record &&
    (record.serverId !== discovery.server_id ||
      record.serverKey !== discovery.signing_public_key)
  )
    throw new Error("Серверная идентичность изменилась. Вход заблокирован.");
  if (!record) {
    if (
      !confirm(
        "Создать ключи пространства в этом браузере? Управление доступно только владельцу или назначенному администратору. Вход по приглашению расходует место для этой отдельной браузерной идентичности. Восстановление пока не поддерживается.",
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
    const authority =
      record.root.privateKey || record.recoveryPrivate
        ? record
        : await unlockAuthority();
    const grant = await proof(
      origin,
      discovery,
      {
        ...record,
        root: authority.root,
        recoveryPrivate: authority.recoveryPrivate,
        recoveryGrantId: authority.recoveryGrantId || record.recoveryGrantId,
      },
      "device.register",
      "",
      joinToken(),
    );
    record.grantId = grant.grantId;
    delete record.recoveryPrivate;
    await vault(record);
    stagedRecovery = undefined;
  }
  await renew();
  if (stagedPairId) {
    await api("/api/v1/auth/pairings/claim", { pairingId: stagedPairId });
    await vault(record);
    stagedRecovery = undefined;
    stagedPairId = "";
  }
  $("#identity-tools").hidden = false;
  $("#download-card").hidden = !record.cardCipher;
  $("#device-password-label").hidden = !!record.root.privateKey;
  await loadDevices();
  if (joinToken()) {
    await api("/api/v1/membership/invites/accept", { token: joinToken() });
    $("#join-token").value = "";
  }
  const setup = await request(origin, "/api/v1/space/setup");
  if (!setup.initialized) {
    $("#claim").hidden = false;
    status("Устройство вошло. Для роли владельца нужен код сервера.");
  } else {
    const current = await api("/api/v1/membership");
    currentRole = current.member.role;
    if (["owner", "admin"].includes(currentRole) && !current.member.blocked) {
      await load();
      await loadMembers();
      await loadInvites();
    } else {
      $("#login").hidden = true;
      $("#participant").hidden = false;
      $("#participant-role").textContent = current.member.blocked
        ? "Доступ заблокирован владельцем."
        : `Ваша роль: ${roleLabel(currentRole)}.`;
      status("Вход выполнен.");
    }
  }
}
$("#sign-in").addEventListener("click", () => run(() => signIn()));
$("#preview-invite").addEventListener("click", () =>
  run(async () => {
    const v = await request(origin, "/api/v1/membership/invites/preview", {
      token: joinToken(),
    });
    $("#invite-preview").textContent =
      `${v.title}: ${roleLabel(v.role)}. Действует до ${new Date(Number(v.expiresAt) * 1000).toLocaleString()}. Вход использует приглашение для отдельной браузерной идентичности. Для Flutter вставьте код в самом клиенте.`;
  }),
);
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
    currentRole = "owner";
    await loadMembers();
    await loadInvites();
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
    $("#identity-tools").hidden = true;
    $("#participant").hidden = true;
    $("#created-invite").hidden = true;
    $("#invite-token").value = "";
    $("#invite-link").value = "";
    $("#claim").hidden = true;
    $("#login").hidden = false;
    status("Сессия закрыта на сервере. Ключи сохранены в браузере.");
  }),
);
request(origin, "/api/v1/space/setup")
  .then((result) => {
    serverReady = true;
    pending(false);
    $("#login-title").textContent = result.initialized
      ? "Вход в пространство"
      : "Первый запуск пространства";
  })
  .catch(() => status("Для рабочей панели нужен сервер с PostgreSQL."));
pending(false);

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function loadMembers(append = false) {
  const result = await api(
    "/api/v1/space/members" +
      (append && memberCursor
        ? `?after=${encodeURIComponent(memberCursor)}`
        : ""),
  );
  const list = $("#members");
  if (!append) list.replaceChildren();
  for (const m of result.members || []) {
    const row = element("div", undefined, "member-row");
    row.append(
      element("p", m.principalId, "identity"),
      element(
        "p",
        `${roleLabel(m.role)}${m.blocked ? " · Доступ заблокирован" : ""}`,
      ),
    );
    if (currentRole === "owner" && m.role !== "owner") {
      const select = element("select");
      select.setAttribute("aria-label", `Роль ${m.principalId}`);
      for (const role of ["reader", "member", "admin"]) {
        const o = element("option", roleLabel(role));
        o.value = role;
        select.append(o);
      }
      select.value = m.role;
      const blocked = element("input");
      blocked.type = "checkbox";
      blocked.checked = !!m.blocked;
      const label = element("label", undefined, "check");
      label.append(blocked, document.createTextNode("Заблокировать доступ"));
      const save = element("button", "Применить права", "secondary");
      save.addEventListener("click", () =>
        run(async () => {
          if (
            !confirm(
              `Изменить права ${m.principalId}? Новая роль: ${roleLabel(select.value)}; блокировка: ${blocked.checked ? "да" : "нет"}.`,
            )
          )
            return;
          await api(
            `/api/v1/space/members/${encodeURIComponent(m.principalId)}`,
            {
              role: select.value,
              blocked: blocked.checked,
              expectedRevision: m.revision,
            },
            "PATCH",
          );
          await loadMembers();
          status("Права сохранены. Сервер проверяет их при каждом действии.");
        }),
      );
      row.append(select, label, save);
    }
    list.append(row);
  }
  memberCursor = result.nextCursor || "";
  $("#more-members").hidden = !memberCursor;
}
async function loadInvites(append = false) {
  const result = await api(
    "/api/v1/space/invites" +
      (append && inviteCursor
        ? `?after=${encodeURIComponent(inviteCursor)}`
        : ""),
  );
  const list = $("#invites");
  if (!append) list.replaceChildren();
  for (const v of result.invites || []) {
    const row = element("div", undefined, "member-row");
    row.append(
      element(
        "p",
        `${roleLabel(v.role)} · ${v.uses || 0}/${v.maxUses} вступлений`,
      ),
      element(
        "p",
        `${v.revoked ? "Отозвано" : Number(v.expiresAt) * 1000 <= Date.now() ? "Истекло" : "Действует"} · до ${new Date(Number(v.expiresAt) * 1000).toLocaleString()}`,
      ),
    );
    if (!v.revoked) {
      const revoke = element("button", "Отозвать приглашение", "secondary");
      revoke.addEventListener("click", () =>
        run(async () => {
          if (
            !confirm(
              "Отозвать приглашение? Уже вступившие участники сохранят доступ; для них используйте блокировку в списке участников.",
            )
          )
            return;
          await api(
            `/api/v1/space/invites/${encodeURIComponent(v.id)}/revoke`,
            {},
          );
          await loadInvites();
          status("Приглашение отозвано.");
        }),
      );
      row.append(revoke);
    }
    list.append(row);
  }
  inviteCursor = result.nextCursor || "";
  $("#more-invites").hidden = !inviteCursor;
}
$("#invite-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    const result = await api("/api/v1/space/invites", {
      role: $("#invite-role").value,
      ttlSeconds: Number($("#invite-ttl").value),
      maxUses: Number($("#invite-uses").value),
    });
    $("#invite-token").value = result.token;
    $("#invite-link").value =
      `${origin}/space#invite=${encodeURIComponent(result.token)}`;
    $("#created-invite").hidden = false;
    await loadInvites();
    status("Приглашение создано. Сохраните ссылку или код.");
  });
});
$("#copy-invite").addEventListener("click", () =>
  run(async () => {
    await navigator.clipboard.writeText($("#invite-link").value);
    status("Ссылка скопирована.");
  }),
);
$("#reload-members").addEventListener("click", () => run(() => loadMembers()));
$("#more-members").addEventListener("click", () =>
  run(() => loadMembers(true)),
);
$("#reload-invites").addEventListener("click", () => run(() => loadInvites()));
$("#more-invites").addEventListener("click", () =>
  run(() => loadInvites(true)),
);
$("#participant-sign-out").addEventListener("click", () =>
  $("#sign-out").click(),
);

function downloadCard(packet) {
  const url = URL.createObjectURL(
    new Blob([packet], { type: "application/json" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = "space-recovery.json";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
$("#card-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    const pass = $("#card-password").value;
    if (pass.length < 12 || pass !== $("#card-confirm").value)
      throw new Error("Пароль слишком короткий или значения не совпадают.");
    if (
      !confirm(
        "Создать отдельный ключ восстановления? Карточка и пароль дают доступ вашей идентичности. Сохраните их раздельно и проверьте восстановление.",
      )
    )
      return;
    try {
      const packet = await createRecoveryCard(
        record,
        origin,
        discovery,
        pass,
        (keys) =>
          proof(origin, discovery, keys, "device.register", "", "", {
            recovery: true,
          }),
      );
      record.cardCipher = packet;
      await vault(record);
      $("#download-card").hidden = false;
      downloadCard(packet);
      await loadDevices();
      status(
        "Зашифрованная карточка создана. Проверьте восстановление на другом устройстве.",
      );
    } finally {
      $("#card-password").value = "";
      $("#card-confirm").value = "";
    }
  });
});
$("#download-card").addEventListener("click", () => {
  if (record?.cardCipher) downloadCard(record.cardCipher);
});
$("#restore-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    const file = $("#restore-file").files[0];
    if (!file || file.size > 16384)
      throw new Error("Выберите карточку JSON размером до 16 KiB.");
    try {
      const packet = await file.text(),
        payload = await openCard(packet, $("#restore-password").value);
      const response = await fetch("/.well-known/space-protocol", {
        redirect: "error",
        cache: "no-store",
      });
      if (!response.ok) throw new Error("Discovery недоступен");
      const server = await response.json();
      const previous = await vault();
      if (
        previous &&
        (previous.serverId !== server.server_id ||
          previous.serverKey !== server.signing_public_key)
      )
        throw new Error(
          "Закреплённая идентичность сервера изменилась. Восстановление заблокировано.",
        );
      const restored = await keysFromCard(payload, origin, server);
      if (
        !confirm(
          "Восстановить идентичность карточки? Сохранённые ключи этого браузера для данного адреса будут заменены. Сначала сохраните прежнюю карточку, если это другая идентичность.",
        )
      )
        return;
      stagedRecovery = {
        ...restored,
        serverId: server.server_id,
        serverKey: server.signing_public_key,
        cardCipher: packet,
      };
      await signIn();
      status(
        "Прежняя идентичность восстановлена с новым устройством. Проверьте список и отзовите потерянные устройства.",
      );
    } finally {
      stagedRecovery = undefined;
      $("#restore-password").value = "";
      $("#restore-file").value = "";
    }
  });
});
async function loadDevices() {
  const result = await api("/api/v1/auth/devices");
  const list = $("#devices");
  list.replaceChildren();
  for (const grant of result.devices || []) {
    const row = element("div", undefined, "member-row");
    row.append(
      element(
        "p",
        grant.recovery
          ? "Ключ восстановления"
          : grant.id === record.grantId
            ? "Это устройство"
            : "Другое устройство",
      ),
      element("p", grant.id, "identity"),
      element(
        "p",
        `${grant.revoked ? "Отозвано" : "Доступ до"} ${new Date(Number(grant.expiresAt) * 1000).toLocaleString()}`,
      ),
    );
    if (
      !grant.revoked &&
      Number(grant.expiresAt) > Date.now() / 1000 &&
      (record.root.privateKey || grant.id !== record.recoveryGrantId)
    ) {
      const revoke = element("button", "Отозвать доступ", "secondary");
      revoke.addEventListener("click", () =>
        run(async () => {
          if (
            !confirm(
              grant.recovery
                ? "Отозвать карточку и все подключённые через неё устройства? Сначала проверьте другой путь доступа."
                : "Отозвать это устройство? Его сессии и подписки завершатся.",
            )
          )
            return;
          if (grant.id === record.grantId) {
            await api("/api/v1/auth/devices/current/revoke", {});
            token = "";
            expires = 0;
            config = null;
            for (const id of [
              "management",
              "claim",
              "participant",
              "identity-tools",
            ])
              $(`#${id}`).hidden = true;
            $("#login").hidden = false;
            status(
              "Устройство отозвано. Новое разрешение автоматически не создаётся.",
            );
            return;
          }
          try {
            const authority = record.root.privateKey
              ? record
              : await unlockAuthority();
            const targetKey = await crypto.subtle.importKey(
              "raw",
              Uint8Array.from(atob(grant.publicKey), (c) => c.charCodeAt(0)),
              "Ed25519",
              true,
              ["verify"],
            );
            await proof(
              origin,
              discovery,
              {
                ...authority,
                device: { ...record.device, publicKey: targetKey },
              },
              "device.revoke",
              grant.id,
              "",
              { expectedScopes: grant.scopes },
            );
            await loadDevices();
            status("Устройство отозвано.");
          } finally {
            $("#device-card-password").value = "";
          }
        }),
      );
      row.append(revoke);
    }
    list.append(row);
  }
}
$("#reload-devices").addEventListener("click", () => run(loadDevices));
$("#start-pair").addEventListener("click", () =>
  run(async () => {
    const response = await fetch("/.well-known/space-protocol", {
      redirect: "error",
      cache: "no-store",
    });
    if (!response.ok) throw new Error("Discovery недоступен");
    discovery = await response.json();
    if (
      discovery.protocol_version !== "0.1-experimental" ||
      !/^srv_[0-9a-f]{32}$/.test(discovery.server_id) ||
      !/^[A-Za-z0-9_-]{43}$/.test(discovery.signing_public_key)
    )
      throw new Error("Нужен совместимый сервер");
    const existing = await vault();
    if (
      existing &&
      (existing.serverId !== discovery.server_id ||
        existing.serverKey !== discovery.signing_public_key)
    )
      throw new Error("Закреплённый серверный ключ изменился");
    if (pendingPair) await cancelPendingPair();
    pendingPair = await startPairing(origin, true);
    $("#pair-pending").hidden = false;
    $("#pair-code").textContent = pendingPair.request.code;
    $("#pair-check").textContent = "Ожидаем подготовку исходного устройства…";
    $("#accept-pair").hidden = true;
    pairTimer = setInterval(pollPair, 2000);
    status("Передайте одноразовый код на исходное устройство.");
  }),
);
async function pollPair() {
  const current = pendingPair;
  if (!current || pollingPair || current.approval) return;
  pollingPair = true;
  try {
    const result = await request(origin, "/api/v1/auth/pairings/poll", {
      pollToken: current.request.pollToken,
    });
    if (current !== pendingPair) return;
    if (result.pairing.state === "cancelled")
      throw new Error("Сопряжение отменено");
    const code = await observeProposal(discovery, current, result);
    if (code)
      $("#pair-check").textContent =
        `Код проверки: ${code}. Введите его на исходном устройстве до выдачи подписи.`;
    if (result.pairing.state === "approved") {
      current.approval = await verifyPairing(
        origin,
        discovery,
        current,
        result,
      );
      clearInterval(pairTimer);
      $("#pair-check").textContent =
        `Root-подпись проверена. Код: ${current.approval.verification}.`;
      $("#accept-pair").hidden = false;
    }
  } catch (error) {
    clearInterval(pairTimer);
    status(error.message || "Сопряжение не завершено");
  } finally {
    pollingPair = false;
  }
}
async function cancelPendingPair() {
  clearInterval(pairTimer);
  const current = pendingPair;
  pendingPair = undefined;
  $("#pair-pending").hidden = true;
  if (current)
    try {
      await request(origin, "/api/v1/auth/pairings/cancel", {
        pollToken: current.request.pollToken,
      });
    } catch {}
}
$("#cancel-pair").addEventListener("click", () => run(cancelPendingPair));
$("#accept-pair").addEventListener("click", () =>
  run(async () => {
    const current = pendingPair;
    if (!current?.approval) throw new Error("Нет проверенного подтверждения");
    if (
      !confirm(
        `Код ${current.approval.verification} совпадает с исходным устройством? Сохранённая идентичность этого браузера будет заменена. Сначала сохраните её карточку, если это другой аккаунт.`,
      )
    )
      return;
    stagedRecovery = current.approval.record;
    stagedPairId = current.request.pairing.id;
    try {
      await signIn();
      pendingPair = undefined;
      $("#pair-pending").hidden = true;
      status(
        "Устройство подключено под прежней идентичностью. Корневой секрет не передавался.",
      );
    } finally {
      stagedRecovery = undefined;
      stagedPairId = "";
    }
  }),
);
$("#prepare-pair-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    const result = await request(origin, "/api/v1/auth/pairings/inspect", {
      code: $("#approve-pair-code").value.trim(),
    });
    const p = result.pairing;
    if (p.state !== "pending")
      throw new Error("Запрос уже подтверждён или отменён");
    const authority = record.root.privateKey ? record : await unlockAuthority();
    if (!authority.root.privateKey)
      throw new Error("Нужен исходный root или корневая карточка Flutter");
    const localRoot = url64(
      await crypto.subtle.exportKey("raw", record.root.publicKey),
    );
    if (
      url64(await crypto.subtle.exportKey("raw", authority.root.publicKey)) !==
      localRoot
    )
      throw new Error("Карточка относится к другой идентичности");
    await api("/api/v1/auth/pairings/propose", { pairingId: p.id });
    preparedCode = await verificationCode(
      discovery.server_id,
      p.id,
      new Uint8Array(
        await crypto.subtle.exportKey("raw", authority.root.publicKey),
      ),
      Uint8Array.from(atob(p.publicKey), (c) => c.charCodeAt(0)),
      !!p.administrative,
    );
    preparedPair = p;
    preparedAuthority = authority;
    $("#prepared-pair").hidden = false;
    $("#prepared-device").textContent =
      `${p.deviceName}. ${p.administrative ? "Запрошено управление пространством с вашими правами." : "Запрошен доступ к чату с вашими правами."}`;
    $("#prepared-check").textContent = preparedCode;
    $("#approve-pair-check").value = "";
    status("Подпись ещё не выдана. Введите код проверки с нового устройства.");
  });
});
$("#approve-pair-form").addEventListener("submit", (event) => {
  event.preventDefault();
  run(async () => {
    if (!preparedPair || !preparedAuthority)
      throw new Error("Сначала подготовьте проверку");
    if (
      $("#approve-pair-check").value.replace(/[\s-]/g, "").toUpperCase() !==
      preparedCode.replaceAll("-", "")
    )
      throw new Error("Коды не совпадают. Не подтверждайте подключение.");
    if (
      !confirm(
        "Коды на двух устройствах совпадают? Выдать root-подпись для нового ключа с указанными правами?",
      )
    )
      return;
    const targetKey = await crypto.subtle.importKey(
      "raw",
      Uint8Array.from(atob(preparedPair.publicKey), (c) => c.charCodeAt(0)),
      "Ed25519",
      true,
      ["verify"],
    );
    await proof(
      origin,
      discovery,
      { ...preparedAuthority, device: { publicKey: targetKey } },
      "device.register",
      "",
      "",
      {
        pairingId: preparedPair.id,
        administrative: !!preparedPair.administrative,
        expectedScopes: [
          "chat.read",
          "chat.write",
          ...(preparedPair.administrative ? ["space.manage"] : []),
        ],
      },
    );
    preparedAuthority = undefined;
    preparedPair = undefined;
    $("#approve-pair-check").value = "";
    $("#prepared-pair").hidden = true;
    await loadDevices();
    status("Подпись выдана. Завершите вход на новом устройстве.");
  });
});
$("#approve-pair-code").addEventListener("input", () => {
  preparedPair = undefined;
  preparedAuthority = undefined;
  $("#prepared-pair").hidden = true;
});
async function unlockAuthority() {
  if (!record.cardCipher)
    throw new Error("Нужна исходная карточка восстановления.");
  try {
    const payload = await openCard(
      record.cardCipher,
      $("#device-card-password").value || $("#renew-card-password").value,
    );
    if (
      payload.root_public_key !==
      url64(await crypto.subtle.exportKey("raw", record.root.publicKey))
    )
      throw new Error("Карточка относится к другой идентичности");
    return await keysFromCard(payload, origin, discovery);
  } finally {
    $("#device-card-password").value = "";
    $("#renew-card-password").value = "";
  }
}
