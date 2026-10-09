import qrcode from "./vendor/qrcode.mjs";
import jsQR from "./vendor/jsqr.mjs";
export const qrPrefix = "space-recovery:v1:";
export function qrRaster(packet) {
  const text = qrPrefix + packet;
  if (new TextEncoder().encode(text).length > 2800)
    throw new Error("Карточка слишком большая для QR; сохраните JSON");
  const qr = qrcode(0, new TextEncoder().encode(text).length<=2048 ? "M" : "L");
  qr.addData(text, "Byte");
  qr.make();
  const scale = 6,
    margin = 4,
    size = (qr.getModuleCount() + margin * 2) * scale;
  const data = new Uint8ClampedArray(size * size * 4);
  data.fill(255);
  for (let r = 0; r < qr.getModuleCount(); r++)
    for (let c = 0; c < qr.getModuleCount(); c++)
      if (qr.isDark(r, c))
        for (let y = 0; y < scale; y++)
          for (let x = 0; x < scale; x++) {
            const index =
              (((r + margin) * scale + y) * size + (c + margin) * scale + x) *
              4;
            data[index] = data[index + 1] = data[index + 2] = 0;
          }
  return { data, width: size, height: size };
}
export function qrText(data, width, height) {
  const result = jsQR(data, width, height, { inversionAttempts: "dontInvert" });
  if (
    !result ||
    !result.data.startsWith(qrPrefix) ||
    new TextEncoder().encode(result.data).length > 2800
  )
    throw new Error("QR-карточка Space не прочитана");
  return result.data.slice(qrPrefix.length);
}
