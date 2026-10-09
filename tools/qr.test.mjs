import assert from "node:assert/strict";
import { PNG } from "pngjs";
import { qrRaster, qrText } from "./crypto_reference/recovery-qr.mjs";
import { sealCard, openCard } from "./crypto_reference/recovery.mjs";
const packet = await sealCard(
  { v: 1, credential: "root", secret: "synthetic fixture" },
  "Several random CI words 2026",
);
const raster = qrRaster(packet),
  png = PNG.sync.write({
    width: raster.width,
    height: raster.height,
    data: Buffer.from(raster.data),
  }),
  read = PNG.sync.read(png);
const decoded = qrText(
  new Uint8ClampedArray(read.data),
  read.width,
  read.height,
);
assert.equal(decoded, packet);
assert.deepEqual(await openCard(decoded, "Several random CI words 2026"), {
  v: 1,
  credential: "root",
  secret: "synthetic fixture",
});
assert.throws(() => qrRaster("x".repeat(3000)));
assert.throws(() =>
  qrText(new Uint8ClampedArray(64 * 64 * 4).fill(255), 64, 64),
);
console.log("QR/PNG: зашифрованная карточка, чтение и границы — успешно.");
