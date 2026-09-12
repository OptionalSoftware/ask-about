// A synthetic presenter: a faceted low-poly head, flat-shaded, with a jaw that
// moves while answers stream.
//
// It is deliberately not a likeness of anyone. The head is built from six
// elliptical rings so it reads as constructed rather than scanned — the point
// of reference is Max Headroom's geometry, not his CRT artifacts.
//
// Everything visual is in LOOK below. Trying a different direction means
// editing those numbers, not the renderer. The backdrop behind the head is
// selected by name from backgrounds.js.

import { backgroundNames, getBackground } from "/ask-about/backgrounds.js";

const LOOK = {
  segments: 10, // points per ring — lower is chunkier
  scale: 0.40, // head size as a fraction of canvas height
  lightDir: [-0.45, -0.55, 0.7],
  faceHue: 168, // base hue; shading walks lightness
  faceSat: 32,
  litness: [20, 70], // lightness range from unlit to fully lit
  jawShade: -8, // jaw sits a touch darker, so the seam reads
  neckShade: -14, // the neck sits in the head's shadow
  bodyHue: 223, // navy shirt
  bodySat: 44,
  bodyLitness: [15, 37], // dark enough to stay navy rather than drifting blue
  edge: "rgba(255,255,255,0.10)", // facet outlines
  seam: "rgba(180,255,232,0.55)", // the jaw line itself
  lensColor: "#070d0c", // near-black; the lens should read as opaque
  lensGlint: "rgba(170,255,225,0.85)", // the specular sweep across the lens
  frameColor: "rgba(198,255,232,0.62)", // rims and bridge
  lipColor: "hsl(168 26% 42%)", // skin-toned margin separating mouth from beard
  mouthColor: "#050908", // darker than the beard, or the aperture disappears
  mouthLip: "rgba(150,235,205,0.45)", // lower lip catching light
  sway: 0.40, // radians of idle yaw
  swaySpeed: 0.00028,
  glitchChance: 0.006, // per frame, when idle
  glitchSplit: 4, // px of RGB separation
};

// Rings from crown to chin: y, x-radius, z-radius, z-offset.
//
// These are the original proportions. Widening them turned the silhouette into
// a bulb — a solid of revolution gets rounder, not more head-like, so the taper
// is what keeps it reading as a head rather than an onion.
const RINGS = [
  { y: -1.0, rx: 0.30, rz: 0.30, dz: 0.0 },
  { y: -0.72, rx: 0.63, rz: 0.60, dz: 0.02 },
  { y: -0.32, rx: 0.74, rz: 0.66, dz: 0.04 },
  { y: 0.04, rx: 0.68, rz: 0.58, dz: 0.02 },
  { y: 0.40, rx: 0.53, rz: 0.45, dz: -0.03 },
  { y: 0.72, rx: 0.28, rz: 0.26, dz: -0.08 },
];

// Rings at or below this index belong to the jaw and drop when speaking.
const JAW_FROM = 4;

// Neck and shoulders, continuing down from the chin as one strip.
//
// Shoulders are wide and shallow, so rx grows far faster than rz — a ring
// with equal radii would produce a barrel, the same lathe trap the head fell
// into. The lowest ring runs past the bottom edge on purpose; a bust that
// stops inside the frame looks severed.
// `sway` damps how much of the head's rotation each ring inherits. A rigid
// bust turning as one piece looks like a mannequin on a turntable; the head
// should turn against a body that barely moves. The seam this creates at the
// chin is hidden under the beard.
// The frame shows model y from about -1.25 to 1.25, so the whole torso has to
// live inside roughly half a unit below the chin. A neck at natural proportions
// would push the shoulders past the bottom edge and they would never be seen.
const TORSO = [
  { y: 0.68, rx: 0.32, rz: 0.30, dz: -0.06, body: false, sway: 0.8 },
  { y: 0.84, rx: 0.34, rz: 0.31, dz: -0.06, body: false, sway: 0.45 },
  { y: 0.96, rx: 0.46, rz: 0.40, dz: -0.05, body: false, sway: 0.25 },
  // The flare needs vertical room. Jumping straight to full width produced a
  // disc seen edge-on, and rz has to grow with rx or the torso is a flat plate
  // rather than a body with depth.
  { y: 1.08, rx: 0.82, rz: 0.54, dz: -0.05, body: true, sway: 0.12 },
  { y: 1.22, rx: 1.22, rz: 0.64, dz: -0.04, body: true, sway: 0.06 },
  { y: 1.50, rx: 1.48, rz: 0.70, dz: -0.04, body: true, sway: 0.03 },
];

// A shell of darker facets laid over the lower face. Built from the same rings
// pushed outward, so it follows the jaw and moves with it — a beard modelled
// separately would slide around as the jaw opens.
const BEARD = {
  from: 3, // first ring it covers (cheek line)
  thickness: 1.09, // radius multiplier — how much it stands off the face
  wrap: -0.3, // include segments with sin(angle) above this; a beard runs
  // ear to ear, not around the back of the skull
  jitter: 0.035, // per-vertex noise so the edge isn't a clean lathe curve
  extend: 0.09, // how far below the chin it hangs
  hue: 174,
  sat: 12,
  litness: [9, 27], // much darker than the face, or it reads as a helmet
};

// Deterministic per-vertex noise. Math.random would shimmer every frame, so
// the jitter has to be a pure function of the vertex's position in the mesh.
function noise(r, i) {
  const n = Math.sin(r * 12.9898 + i * 78.233) * 43758.5453;
  return (n - Math.floor(n)) * 2 - 1;
}

function buildGeometry() {
  const verts = [];
  const faces = [];

  RINGS.forEach((ring, r) => {
    for (let i = 0; i < LOOK.segments; i++) {
      const a = (i / LOOK.segments) * Math.PI * 2;
      verts.push({
        x: ring.rx * Math.cos(a),
        y: ring.y,
        z: ring.rz * Math.sin(a) + ring.dz,
        jaw: r >= JAW_FROM,
      });
    }
  });

  for (let r = 0; r < RINGS.length - 1; r++) {
    for (let i = 0; i < LOOK.segments; i++) {
      const j = (i + 1) % LOOK.segments;
      const a = r * LOOK.segments + i;
      const b = r * LOOK.segments + j;
      const c = (r + 1) * LOOK.segments + j;
      const d = (r + 1) * LOOK.segments + i;
      faces.push({
        idx: [a, b, c, d],
        jaw: r + 1 >= JAW_FROM,
        // The band just above the jaw carries the seam on its lower edge, so
        // the jaw's motion is legible even at small sizes.
        seam: r === JAW_FROM - 1,
      });
    }
  }

  // Caps: a fan to an apex above the crown and below the chin.
  const apex = verts.length;
  verts.push({ x: 0, y: -1.12, z: 0, jaw: false });
  const base = verts.length;
  verts.push({ x: 0, y: 0.84, z: -0.06, jaw: true });

  for (let i = 0; i < LOOK.segments; i++) {
    const j = (i + 1) % LOOK.segments;
    faces.push({ idx: [apex, j, i] });
    const last = (RINGS.length - 1) * LOOK.segments;
    faces.push({ idx: [base, last + i, last + j] });
  }

  // Neck and shoulders. Fixed to the body, so no jaw flag — these must stay
  // put while the jaw moves above them.
  const torsoStart = verts.length;
  TORSO.forEach((ring) => {
    for (let i = 0; i < LOOK.segments; i++) {
      const a = (i / LOOK.segments) * Math.PI * 2;
      verts.push({
        x: ring.rx * Math.cos(a),
        y: ring.y,
        z: ring.rz * Math.sin(a) + ring.dz,
        jaw: false,
        sway: ring.sway,
      });
    }
  });

  for (let r = 0; r < TORSO.length - 1; r++) {
    for (let i = 0; i < LOOK.segments; i++) {
      const j = (i + 1) % LOOK.segments;
      const a = torsoStart + r * LOOK.segments + i;
      const b = torsoStart + r * LOOK.segments + j;
      const c = torsoStart + (r + 1) * LOOK.segments + j;
      const d = torsoStart + (r + 1) * LOOK.segments + i;
      faces.push({
        idx: [a, b, c, d],
        // A band is garment once its lower ring is; that puts the collar
        // line exactly where the shoulders start to flare.
        body: TORSO[r + 1].body,
        neck: !TORSO[r + 1].body,
      });
    }
  }

  // Beard: the lower rings re-skinned at a larger radius, plus one extra ring
  // hanging below the chin. Only the forward-facing segments are emitted, so
  // it wraps ear to ear and stops rather than circling the skull.
  const last = RINGS[RINGS.length - 1];
  const beardRings = [
    ...RINGS.slice(BEARD.from),
    {
      y: last.y + BEARD.extend,
      rx: last.rx * 0.58,
      rz: last.rz * 0.58,
      dz: last.dz,
    },
  ];

  const beardStart = verts.length;
  beardRings.forEach((ring, r) => {
    for (let i = 0; i < LOOK.segments; i++) {
      const a = (i / LOOK.segments) * Math.PI * 2;
      const wobble = 1 + noise(r, i) * BEARD.jitter;
      const scale = BEARD.thickness * wobble;
      verts.push({
        x: ring.rx * scale * Math.cos(a),
        y: ring.y + noise(r + 7, i) * BEARD.jitter * 0.6,
        z: ring.rz * scale * Math.sin(a) + ring.dz,
        // Ring 0 of the beard is the cheek line, which sits above the jaw.
        jaw: BEARD.from + r >= JAW_FROM,
      });
    }
  });

  const facesForward = (i) =>
    Math.sin((i / LOOK.segments) * Math.PI * 2) > BEARD.wrap;

  for (let r = 0; r < beardRings.length - 1; r++) {
    for (let i = 0; i < LOOK.segments; i++) {
      const j = (i + 1) % LOOK.segments;
      if (!facesForward(i) || !facesForward(j)) continue;
      const a = beardStart + r * LOOK.segments + i;
      const b = beardStart + r * LOOK.segments + j;
      const c = beardStart + (r + 1) * LOOK.segments + j;
      const d = beardStart + (r + 1) * LOOK.segments + i;
      // Reversed relative to the skull's quads: the beard is only ever seen
      // from outside, so its front faces have to wind the opposite way to
      // survive the same cull.
      faces.push({ idx: [d, c, b, a], beard: true });
    }
  }

  // Mouth. Without it the beard is just a dark lower face — a beard is read
  // by what it surrounds, not by its own shape.
  //
  // The top two vertices are fixed and the bottom two carry the jaw flag, so
  // the aperture opens on its own as the jaw drops. That also makes the
  // speaking state legible, which the jaw alone never managed at 96px.
  // Two layers. The lip surround is the skin-toned margin a beard leaves
  // around the mouth — without it the aperture is a dark shape on a dark
  // beard, which reads as a hole rather than a mouth. The mouth sits on top
  // of it, darker still.
  //
  // Both grow with the jaw: bottom vertices carry the jaw flag, top ones
  // don't, so the whole assembly stretches open rather than sliding down.
  const LIPS = { halfWidth: 0.25, top: 0.16, bottom: 0.41, z: 0.62 };
  const MOUTH = { halfWidth: 0.17, top: 0.23, bottom: 0.34, z: 0.645 };

  for (const [part, flag] of [
    [LIPS, "lips"],
    [MOUTH, "mouth"],
  ]) {
    const base = verts.length;
    verts.push({ x: -part.halfWidth, y: part.top, z: part.z, jaw: false });
    verts.push({ x: part.halfWidth, y: part.top, z: part.z, jaw: false });
    verts.push({ x: part.halfWidth, y: part.bottom, z: part.z, jaw: true });
    verts.push({ x: -part.halfWidth, y: part.bottom, z: part.z, jaw: true });
    faces.push({ idx: [base, base + 1, base + 2, base + 3], [flag]: true });
  }

  // Glasses: two wraparound lenses and a bridge, sitting proud of the face.
  //
  // Painter's algorithm can't cut a hole, so every piece has to stay in front
  // of the skull surface beneath it — z falls off toward the temples because
  // the head narrows there, and a lens that dipped below the surface would be
  // swallowed by it. Dark lenses also mean no eyes to render, which is the
  // whole reason this reads better: eyes are what make a synthetic face
  // uncanny.
  const LENS = {
    inner: 0.10,
    outer: 0.60, // wraps well past the brow so the silhouette reads at 96px
    innerZ: 0.74, // clears the brow ring (surface sits at ~0.69 here)
    outerZ: 0.50, // clears the temple, where the surface drops to ~0.45
    top: -0.36,
    bottom: 0.02, // deeper lens; small lenses vanish at header size
    rake: 0.04, // outer corners ride slightly higher — an angular, worn look
  };

  for (const side of [-1, 1]) {
    const xi = side * LENS.inner;
    const xo = side * LENS.outer;
    const corners = [
      { x: xi, y: LENS.top, z: LENS.innerZ },
      { x: xo, y: LENS.top + LENS.rake, z: LENS.outerZ },
      { x: xo, y: LENS.bottom + LENS.rake, z: LENS.outerZ },
      { x: xi, y: LENS.bottom, z: LENS.innerZ },
    ];
    // Mirroring flips the winding order, which would cull the right lens.
    const ordered = side === -1 ? corners.slice().reverse() : corners;

    const start = verts.length;
    for (const c of ordered) verts.push({ ...c, jaw: false });
    faces.push({ idx: [start, start + 1, start + 2, start + 3], glass: true });
  }

  // Bridge across the nose — thick enough to be visible at header size, which
  // is what makes two dark shapes read as one pair of glasses.
  const bridge = verts.length;
  verts.push({ x: -LENS.inner - 0.01, y: -0.32, z: LENS.innerZ, jaw: false });
  verts.push({ x: LENS.inner + 0.01, y: -0.32, z: LENS.innerZ, jaw: false });
  verts.push({ x: LENS.inner + 0.01, y: -0.19, z: LENS.innerZ, jaw: false });
  verts.push({ x: -LENS.inner - 0.01, y: -0.19, z: LENS.innerZ, jaw: false });
  faces.push({ idx: [bridge, bridge + 1, bridge + 2, bridge + 3], frame: true });

  return { verts, faces };
}

const GEO = buildGeometry();

function normalize(v) {
  const len = Math.hypot(v[0], v[1], v[2]) || 1;
  return [v[0] / len, v[1] / len, v[2] / len];
}
const LIGHT = normalize(LOOK.lightDir);

export function createAvatar(canvas, { background = "maxheadroom" } = {}) {
  // Resolved lazily on change so a stateful backdrop gets a fresh instance
  // rather than resuming wherever it left off.
  let backdropName = backgroundNames.includes(background)
    ? background
    : backgroundNames[0];
  let backdrop = getBackground(backdropName);
  const ctx = canvas.getContext("2d");
  const layer = document.createElement("canvas");
  const lctx = layer.getContext("2d");

  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  let state = "idle";
  let jaw = 0; // 0 closed, 1 open
  let jawTarget = 0;
  let energy = 0; // decays after each released sentence
  let glitch = 0; // frames remaining
  let raf = null;
  let dpr = 1;

  function resize() {
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    const rect = canvas.getBoundingClientRect();
    for (const c of [canvas, layer]) {
      c.width = Math.round(rect.width * dpr);
      c.height = Math.round(rect.height * dpr);
    }
  }

  function project(v, t) {
    // Idle sway, plus a faster nod while speaking. Torso vertices carry a
    // damping factor so the head turns against a near-still body.
    const damp = v.sway ?? 1;
    const yaw =
      (Math.sin(t * LOOK.swaySpeed) * LOOK.sway +
        (state === "thinking" ? Math.sin(t * 0.004) * 0.1 : 0)) *
      damp;
    const pitch =
      (Math.sin(t * LOOK.swaySpeed * 1.7) * 0.1 + energy * 0.06) * damp;

    let { x, y, z } = v;
    if (v.jaw) {
      y += jaw * 0.16; // drop the jaw
      z += jaw * 0.03; // and push it very slightly forward
    }

    // Yaw about Y, then pitch about X.
    const cx = x * Math.cos(yaw) + z * Math.sin(yaw);
    const cz = -x * Math.sin(yaw) + z * Math.cos(yaw);
    const cy = y * Math.cos(pitch) - cz * Math.sin(pitch);
    const dz = y * Math.sin(pitch) + cz * Math.cos(pitch);

    return { x: cx, y: cy, z: dz };
  }

  function drawHead(t) {
    const w = layer.width;
    const h = layer.height;
    lctx.clearRect(0, 0, w, h);

    const size = h * LOOK.scale;
    backdrop.draw(lctx, { w, h, t, dpr, size });
    const rotated = GEO.verts.map((v) => project(v, t));

    const drawable = GEO.faces.map((face) => {
      const pts = face.idx.map((i) => rotated[i]);
      const depth = pts.reduce((sum, p) => sum + p.z, 0) / pts.length;
      return { face, pts, depth };
    });
    drawable.sort((a, b) => a.depth - b.depth); // painter's algorithm

    for (const { face, pts } of drawable) {
      // Backface cull using the projected winding order.
      const screen = pts.map((p) => {
        const persp = 1 / (1 + p.z * 0.3);
        return {
          x: w / 2 + p.x * size * persp,
          y: h / 2 + p.y * size * persp,
        };
      });

      // Backface cull: positive signed area is front-facing here.
      const area =
        (screen[1].x - screen[0].x) * (screen[2].y - screen[0].y) -
        (screen[2].x - screen[0].x) * (screen[1].y - screen[0].y);
      if (area <= 0) continue;

      // Flat shading from the face normal in rotated space.
      const u = [pts[1].x - pts[0].x, pts[1].y - pts[0].y, pts[1].z - pts[0].z];
      const v2 = [pts[2].x - pts[0].x, pts[2].y - pts[0].y, pts[2].z - pts[0].z];
      const n = normalize([
        u[1] * v2[2] - u[2] * v2[1],
        u[2] * v2[0] - u[0] * v2[2],
        u[0] * v2[1] - u[1] * v2[0],
      ]);
      const lit = Math.max(0, n[0] * LIGHT[0] + n[1] * LIGHT[1] + n[2] * LIGHT[2]);

      lctx.beginPath();
      lctx.moveTo(screen[0].x, screen[0].y);
      for (let i = 1; i < screen.length; i++) lctx.lineTo(screen[i].x, screen[i].y);
      lctx.closePath();

      if (face.frame) {
        lctx.fillStyle = LOOK.frameColor;
        lctx.fill();
        continue;
      }

      if (face.glass) {
        // Near-black lens, a lit top rim where the frame catches light, and a
        // specular streak that sweeps across as answers arrive. The sweep is
        // what gives the avatar life now that there are no eyes to animate.
        lctx.fillStyle = LOOK.lensColor;
        lctx.fill();

        lctx.strokeStyle = LOOK.frameColor;
        lctx.lineWidth = 1.6 * dpr;
        lctx.stroke();

        const xs = screen.map((p) => p.x);
        const ys = screen.map((p) => p.y);
        const x0 = Math.min(...xs);
        const x1 = Math.max(...xs);
        const y0 = Math.min(...ys);
        const y1 = Math.max(...ys);

        // Sweep position cycles slowly at rest and snaps forward on energy.
        const phase = ((t * 0.00022 + energy * 0.6) % 1.4) - 0.2;
        const cx = x0 + (x1 - x0) * phase;
        const halfW = (x1 - x0) * 0.16;

        lctx.save();
        lctx.clip(); // keep the streak inside the lens
        lctx.strokeStyle = LOOK.lensGlint;
        lctx.globalAlpha = 0.5 + energy * 0.5;
        lctx.lineWidth = Math.max(1.5, halfW) * dpr;
        lctx.beginPath();
        lctx.moveTo(cx - halfW, y1 + 2);
        lctx.lineTo(cx + halfW, y0 - 2);
        lctx.stroke();
        lctx.restore();
        lctx.globalAlpha = 1;
        continue;
      }

      if (face.lips) {
        lctx.fillStyle = LOOK.lipColor;
        lctx.fill();
        continue;
      }

      if (face.mouth) {
        lctx.fillStyle = LOOK.mouthColor;
        lctx.fill();
        // A lit lower edge reads as the lip catching light, and it's what
        // separates an open mouth from a hole in the beard.
        lctx.strokeStyle = LOOK.mouthLip;
        lctx.lineWidth = 1.2 * dpr;
        lctx.beginPath();
        lctx.moveTo(screen[2].x, screen[2].y);
        lctx.lineTo(screen[3].x, screen[3].y);
        lctx.stroke();
        continue;
      }

      if (face.body) {
        const [blo, bhi] = LOOK.bodyLitness;
        lctx.fillStyle = `hsl(${LOOK.bodyHue} ${LOOK.bodySat}% ${blo + (bhi - blo) * lit}%)`;
        lctx.fill();
        lctx.strokeStyle = LOOK.edge;
        lctx.lineWidth = 1 * dpr;
        lctx.stroke();
        continue;
      }

      if (face.beard) {
        const [blo, bhi] = BEARD.litness;
        lctx.fillStyle = `hsl(${BEARD.hue} ${BEARD.sat}% ${blo + (bhi - blo) * lit}%)`;
        lctx.fill();
        lctx.strokeStyle = LOOK.edge;
        lctx.lineWidth = 1 * dpr;
        lctx.stroke();
        continue;
      }

      const [lo, hi] = LOOK.litness;
      const shade =
        lo +
        (hi - lo) * lit +
        (face.jaw ? LOOK.jawShade : 0) +
        (face.neck ? LOOK.neckShade : 0);
      lctx.fillStyle = `hsl(${LOOK.faceHue} ${LOOK.faceSat}% ${Math.max(0, shade)}%)`;
      lctx.fill();
      lctx.strokeStyle = LOOK.edge;
      lctx.lineWidth = 1 * dpr;
      lctx.stroke();

      // The jaw seam: the lower edge of the band above the jaw.
      if (face.seam && screen.length === 4) {
        lctx.strokeStyle = LOOK.seam;
        lctx.lineWidth = 1.5 * dpr;
        lctx.beginPath();
        lctx.moveTo(screen[2].x, screen[2].y);
        lctx.lineTo(screen[3].x, screen[3].y);
        lctx.stroke();
      }
    }
  }

  function composite() {
    ctx.clearRect(0, 0, canvas.width, canvas.height);

    if (glitch > 0) {
      // Chromatic split — the one retro artifact worth keeping, used sparingly.
      const off = LOOK.glitchSplit * dpr;
      ctx.globalCompositeOperation = "lighter";
      ctx.globalAlpha = 0.85;
      ctx.drawImage(layer, -off, 0);
      ctx.drawImage(layer, off, 0);
      ctx.globalAlpha = 1;
      ctx.globalCompositeOperation = "source-over";
      ctx.drawImage(layer, 0, 0);
    } else {
      ctx.drawImage(layer, 0, 0);
    }
  }

  function frame(t) {
    // Jaw chatters while speaking, settles otherwise.
    if (state === "speaking") {
      jawTarget = 0.25 + Math.random() * 0.75 * (0.4 + energy);
    } else if (state === "thinking") {
      jawTarget = 0.04 + Math.sin(t * 0.006) * 0.03;
    } else {
      jawTarget = 0;
    }
    jaw += (jawTarget - jaw) * (reduced ? 0.08 : 0.35);
    energy *= 0.96;

    if (glitch > 0) glitch--;
    else if (!reduced && Math.random() < LOOK.glitchChance) glitch = 3;

    drawHead(t);
    composite();
    raf = requestAnimationFrame(frame);
  }

  const observer = new ResizeObserver(resize);
  observer.observe(canvas);
  resize();
  raf = requestAnimationFrame(frame);

  return {
    /** The backdrop currently drawn behind the head. */
    get background() {
      return backdropName;
    },

    /** Advances to the next registered backdrop and returns its name. */
    nextBackground() {
      const i = backgroundNames.indexOf(backdropName);
      backdropName = backgroundNames[(i + 1) % backgroundNames.length];
      backdrop = getBackground(backdropName);
      return backdropName;
    },

    /** idle | thinking | speaking */
    setState(next) {
      state = next;
      if (next === "speaking") energy = 1;
    },
    /** Called per released sentence — a beat of extra motion. */
    pulse() {
      energy = Math.min(1, energy + 0.6);
    },
    destroy() {
      cancelAnimationFrame(raf);
      observer.disconnect();
    },
  };
}
