import qrcode from "./vendor/qrcode.mjs";
import jsQR from "./vendor/jsqr.mjs";
export const qrPrefix = "space-recovery:v1:";
export function qrRaster(packet) {
  const text = qrPrefix + packet;
  if (new TextEncoder().encode(text).length > 2048)
    throw new Error("Карточка слишком большая для QR; сохраните JSON");
  const qr = qrcode(0, "M");
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
    new TextEncoder().encode(result.data).length > 2048
  )
    throw new Error("QR-карточка Space не прочитана");
  return result.data.slice(qrPrefix.length);
}
export async function pngBlob(packet) {
  const raster = qrRaster(packet),
    canvas = document.createElement("canvas");
  canvas.width = raster.width;
  canvas.height = raster.height;
  canvas
    .getContext("2d")
    .putImageData(
      new ImageData(raster.data, raster.width, raster.height),
      0,
      0,
    );
  return new Promise((resolve, reject) =>
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error("PNG не создан"))),
      "image/png",
    ),
  );
}
export async function readCardFile(file) {
  if (!file || file.size > 8 * 1024 * 1024)
    throw new Error("Выберите JSON или PNG до 8 MiB");
  const header = new Uint8Array(await file.slice(0, 24).arrayBuffer());
  const png =
    header.length >= 8 &&
    [137, 80, 78, 71, 13, 10, 26, 10].every((v, i) => header[i] === v);
  if (!png) {
    if (file.size > 16384) throw new Error("JSON-карточка слишком большая");
    return file.text();
  }
  if (header.length < 24) throw new Error("Повреждённый PNG");
  const view = new DataView(header.buffer),
    width = view.getUint32(16),
    height = view.getUint32(20);
  if (
    width < 64 ||
    height < 64 ||
    width > 2048 ||
    height > 2048 ||
    width * height > 4194304
  )
    throw new Error("PNG должен быть от 64 до 2048 пикселей по каждой стороне");
  const bitmap = await createImageBitmap(file);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    context.fillStyle = "#fff";
    context.fillRect(0, 0, width, height);
    context.drawImage(bitmap, 0, 0);
    return qrText(
      context.getImageData(0, 0, width, height).data,
      width,
      height,
    );
  } finally {
    bitmap.close();
  }
}
