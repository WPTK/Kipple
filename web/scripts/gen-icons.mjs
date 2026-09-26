// Generates the PWA icons in web/public from one drawing (a white K on the accent blue). No dependencies:
// polygons are rasterised with 4x4 supersampling and written as PNG (and one PNG inside favicon.ico).
// Run by hand after changing the drawing: `node scripts/gen-icons.mjs`. The output is committed.
import { deflateSync } from "node:zlib";
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const out = fileURLToPath(new URL("../public/", import.meta.url));
mkdirSync(out, { recursive: true });

const BG = [0x1a, 0x5f, 0xb4];
// The K in a 100x100 box, centred on (50,50), inside the 80% maskable safe zone.
const K = [
  [[30, 24], [42, 24], [42, 76], [30, 76]],
  [[40, 56], [60, 24], [75, 24], [54, 54]],
  [[40, 46], [56, 46], [76, 76], [61, 76], [40, 56]],
];

function inPoly(x, y, p) {
  let c = false;
  for (let i = 0, j = p.length - 1; i < p.length; j = i++) {
    const [xi, yi] = p[i];
    const [xj, yj] = p[j];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) c = !c;
  }
  return c;
}

// rounded: corner radius as a fraction of the size (0 = square, full bleed, for maskable and Apple icons).
function render(size, rounded, scale = 1) {
  const px = Buffer.alloc(size * size * 4);
  const ss = 4;
  const r = rounded * size;
  const inBox = (x, y) => {
    if (!rounded) return true;
    const cx = Math.min(Math.max(x, r), size - r);
    const cy = Math.min(Math.max(y, r), size - r);
    return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
  };
  for (let py = 0; py < size; py++) {
    for (let pxl = 0; pxl < size; pxl++) {
      let box = 0;
      let glyph = 0;
      for (let sy = 0; sy < ss; sy++) {
        for (let sx = 0; sx < ss; sx++) {
          const x = pxl + (sx + 0.5) / ss;
          const y = py + (sy + 0.5) / ss;
          if (!inBox(x, y)) continue;
          box++;
          const ux = ((x / size - 0.5) / scale + 0.5) * 100;
          const uy = ((y / size - 0.5) / scale + 0.5) * 100;
          if (K.some((p) => inPoly(ux, uy, p))) glyph++;
        }
      }
      const n = ss * ss;
      const a = box / n;
      const g = box ? glyph / box : 0;
      const o = (py * size + pxl) * 4;
      for (let k = 0; k < 3; k++) px[o + k] = Math.round(BG[k] * (1 - g) + 255 * g);
      px[o + 3] = Math.round(a * 255);
    }
  }
  return png(size, px);
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type), data]);
  const c = Buffer.alloc(4);
  c.writeUInt32BE(crc(td));
  return Buffer.concat([len, td, c]);
}
function png(size, rgba) {
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (size * 4 + 1)] = 0;
    rgba.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8;
  ihdr[9] = 6;
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw, { level: 9 })), chunk("IEND", Buffer.alloc(0))]);
}

const write = (name, data) => writeFileSync(out + name, data);
write("icon-192.png", render(192, 0.22));
write("icon-512.png", render(512, 0.22));
// Maskable: full bleed, the K shrunk so it stays inside the 80% safe circle whatever shape the OS cuts.
write("icon-maskable-512.png", render(512, 0, 0.8));
// iOS applies its own corner mask, and does not like transparency.
write("apple-touch-icon.png", render(180, 0));
const fav = render(32, 0.22);
const dir = Buffer.alloc(22);
dir.writeUInt16LE(1, 2); // type: icon
dir.writeUInt16LE(1, 4); // one image
dir[6] = 32;
dir[7] = 32;
dir.writeUInt16LE(1, 10); // planes
dir.writeUInt16LE(32, 12); // bits per pixel
dir.writeUInt32LE(fav.length, 14);
dir.writeUInt32LE(22, 18);
write("favicon.ico", Buffer.concat([dir, fav]));
write(
  "favicon.svg",
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect width="100" height="100" rx="22" fill="#1a5fb4"/><g fill="#fff">${K.map((p) => `<polygon points="${p.map((q) => q.join(",")).join(" ")}"/>`).join("")}</g></svg>\n`,
);
