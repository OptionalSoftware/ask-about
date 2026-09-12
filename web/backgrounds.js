// Backdrops for the avatar, selected by name from config.
//
// Each entry is a draw function called once per frame, before the head, with a
// 2D context and the frame's dimensions. Backdrops are deliberately 2D: they
// are flat plates behind the scene, not part of it, so they never go through
// the head's perspective transform.
//
// A backdrop that needs state between frames defines `create()` returning an
// object with its own `draw`; getBackground calls it so each avatar gets a
// private instance. Stateless backdrops skip it and stay plain objects.
//
// To add one: write a draw function, add it to `backgrounds`, and set
// `[avatar] background` in the config. Nothing else needs to change.

/** Max Headroom: three fields of parallel neon lines meeting at a Y-junction. */
const maxheadroom = {
  label: "Max Headroom",

  // All positions are fractions of the canvas.
  junction: [0.75, 0.22], // three-way meeting point, behind the head's upper right
  leftSeam: 0.46, // where the magenta/yellow seam meets the left edge
  rightSeam: 0.06, // where the magenta/cyan seam meets the right edge
  bottomSeam: 0.82, // where the yellow/cyan seam meets the bottom edge
  spacing: 0.036, // gap between parallel lines, as a fraction of height
  drift: 0.0015, // slow slide along each field's normal

  // Angles are screen-space, where +y is down, so a small positive angle
  // slopes gently downward from left to right.
  fields: {
    upper: { angle: 0.13, colors: ["rgba(234,98,192,0.78)", "rgba(198,166,238,0.6)"] },
    lower: { angle: 0.17, colors: ["rgba(206,226,74,0.72)", "rgba(166,204,62,0.54)"] },
    // Steeply diagonal, upper-left to lower-right, close to vertical.
    right: { angle: 1.31, colors: ["rgba(92,238,228,0.76)", "rgba(58,178,178,0.58)"] },
  },

  draw(ctx, { w, h, t, dpr }) {
    const J = [this.junction[0] * w, this.junction[1] * h];
    const L = [0, this.leftSeam * h];
    const R = [w, this.rightSeam * h];
    const B = [this.bottomSeam * w, h];

    // The three polygons tile the canvas exactly, so every shared edge is a
    // seam. Clipping each field to its own polygon is what makes lines
    // terminate against a seam at a hard angle instead of running through.
    const fields = [
      { poly: [[0, 0], L, J, R, [w, 0]], ...this.fields.upper },
      { poly: [L, [0, h], B, J], ...this.fields.lower },
      { poly: [J, B, [w, h], R], ...this.fields.right },
    ];

    const diag = Math.hypot(w, h);
    const spacing = this.spacing * h;
    const phase = ((t * this.drift) % spacing) - spacing;

    for (const field of fields) {
      ctx.save();

      ctx.beginPath();
      ctx.moveTo(field.poly[0][0], field.poly[0][1]);
      for (let i = 1; i < field.poly.length; i++) {
        ctx.lineTo(field.poly[i][0], field.poly[i][1]);
      }
      ctx.closePath();
      ctx.clip();

      // Lines run along `d`; successive lines step along the normal `n`.
      const dx = Math.cos(field.angle);
      const dy = Math.sin(field.angle);
      const nx = -dy;
      const ny = dx;

      ctx.lineWidth = 1 * dpr;
      let band = 0;

      for (let offset = -diag; offset <= diag; offset += spacing) {
        const o = offset + phase;
        const px = w / 2 + nx * o;
        const py = h / 2 + ny * o;

        ctx.strokeStyle = field.colors[band++ % field.colors.length];
        ctx.beginPath();
        ctx.moveTo(px - dx * diag, py - dy * diag);
        ctx.lineTo(px + dx * diag, py + dy * diag);
        ctx.stroke();
      }

      ctx.restore();
    }
  },
};

/** Falling glyph rain. Stateful: each column tracks its own head and speed. */
const matrix = {
  label: "Matrix",
  fontSize: 0.085, // glyph size as a fraction of canvas height
  minSpeed: 0.05, // rows advanced per frame
  maxSpeed: 0.16,
  trail: 10, // glyphs in the fading tail behind the head
  headColor: "rgba(214,255,228,0.95)",
  tailRGB: [104, 232, 150],
  tailAlpha: 0.62, // alpha at the brightest tail glyph, fading to zero
  glyphs:
    "ｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓﾔﾕﾖﾗﾘﾙﾚﾛﾜﾝ0123456789",
  churn: 90, // ms a glyph holds before being swapped

  create() {
    const spec = this;
    let columns = [];
    let cellW = 0;
    let lastWidth = 0;

    // Glyph choice is a hash of column, row, and a coarse time bucket, not
    // Math.random: a column redraws its whole tail every frame, so random
    // glyphs would make the entire trail seethe rather than the characters
    // flicking over occasionally.
    function glyphAt(col, row, bucket) {
      const n = Math.sin(col * 127.1 + row * 311.7 + bucket * 74.7) * 43758.5453;
      const i = Math.floor((n - Math.floor(n)) * spec.glyphs.length);
      return spec.glyphs[i];
    }

    return {
      draw(ctx, { w, h, t, dpr }) {
        const fontPx = Math.max(6 * dpr, spec.fontSize * h);
        cellW = fontPx * 0.72; // monospace advance is narrower than the em

        if (w !== lastWidth) {
          lastWidth = w;
          const count = Math.ceil(w / cellW);
          columns = Array.from({ length: count }, (_, i) => ({
            // Stagger the starts, or every column falls as one bright row.
            y: -Math.abs(Math.sin(i * 12.9898) * 43758.5453) % (h / fontPx),
            speed:
              spec.minSpeed +
              (Math.abs(Math.sin(i * 78.233) * 43758.5453) % 1) *
                (spec.maxSpeed - spec.minSpeed),
          }));
        }

        ctx.font = `${fontPx}px ui-monospace, monospace`;
        ctx.textBaseline = "top";
        const bucket = Math.floor(t / spec.churn);
        const rows = h / fontPx;
        const [r, g, b] = spec.tailRGB;

        columns.forEach((col, i) => {
          col.y += col.speed;
          if (col.y - spec.trail > rows) col.y = -spec.trail;

          const head = Math.floor(col.y);
          const x = i * cellW;

          for (let k = 0; k < spec.trail; k++) {
            const row = head - k;
            if (row < 0 || row > rows) continue;

            ctx.fillStyle =
              k === 0
                ? spec.headColor
                : `rgba(${r},${g},${b},${spec.tailAlpha * (1 - k / spec.trail)})`;
            ctx.fillText(glyphAt(i, row, bucket), x, row * fontPx);
          }
        });
      },
    };
  },
};

/**
 * An empty holodeck: amber wireframe grid on black, one-point perspective.
 *
 * Built as a tunnel rather than four separate walls. Nested rectangles
 * interpolated toward a vanishing point give floor, ceiling and both walls at
 * once, and the radial lines from the near edge tie them together — the same
 * geometry a real room's grid would project to, for a fraction of the work.
 */
const holodeck = {
  label: "Holodeck",
  vanish: [0.5, 0.42], // vanishing point, fraction of canvas — behind the head
  divisions: 4, // grid cells along each edge; more than this reads as a web
  rings: 3, // cross-lines between the near plane and the back wall
  falloff: 0.55, // perspective compression between those rings

  // How far the back wall sits toward the vanishing point. This is the number
  // that decides whether it looks like a room or a web: pushed too far back,
  // the wall shrinks to a dot and every line fans out from it. At 0.66 the
  // wall fills about half the frame and the lines stay close to parallel.
  wallDepth: 0.66,

  // The near plane sits outside the canvas so the nearest cross-line is
  // clipped away; anchored to the edge it reads as a picture frame instead of
  // a room carrying on past the view.
  overscan: 0.25,

  // No drift. An empty holodeck is a room standing still, and any motion here
  // turned it into a tunnel being flown down.
  line: "rgba(255,190,66,0.55)",
  glow: "rgba(255,146,20,0.14)",

  draw(ctx, { w, h, t, dpr }) {
    const vx = this.vanish[0] * w;
    const vy = this.vanish[1] * h;
    const lerp = (a, b, k) => a + (b - a) * k;

    // Near plane, pushed beyond the visible area.
    const nx0 = -this.overscan * w;
    const ny0 = -this.overscan * h;
    const nx1 = w + this.overscan * w;
    const ny1 = h + this.overscan * h;

    // Ring depths, compressed toward the wall and normalised so the last one
    // lands exactly on it rather than wherever the falloff happens to reach.
    const span = 1 - Math.pow(this.falloff, this.rings);
    const ringDepth = (k) =>
      (this.wallDepth * (1 - Math.pow(this.falloff, k))) / span;

    // Draw everything twice: a wide dim pass for glow, a narrow bright pass on
    // top. Cheaper than shadowBlur per stroke, and closer to how the original
    // reads on a CRT.
    const passes = [
      { color: this.glow, width: 3 * dpr },
      { color: this.line, width: 1 * dpr },
    ];

    for (const pass of passes) {
      ctx.strokeStyle = pass.color;
      ctx.lineWidth = pass.width;
      ctx.beginPath();

      // The back wall. A holodeck is a room, so the grid ends on a far surface
      // rather than collapsing into the vanishing point — that convergence is
      // what made the first version read as a spider web.
      const farD = this.wallDepth;
      const fx0 = lerp(nx0, vx, farD);
      const fy0 = lerp(ny0, vy, farD);
      const fx1 = lerp(nx1, vx, farD);
      const fy1 = lerp(ny1, vy, farD);

      // Lines running away from the viewer, stopping at the back wall. They
      // start off-canvas so they enter from outside the frame.
      for (let i = 0; i <= this.divisions; i++) {
        const f = i / this.divisions;
        const edges = [
          [lerp(nx0, nx1, f), ny0, lerp(fx0, fx1, f), fy0], // ceiling
          [lerp(nx0, nx1, f), ny1, lerp(fx0, fx1, f), fy1], // floor
          [nx0, lerp(ny0, ny1, f), fx0, lerp(fy0, fy1, f)], // left wall
          [nx1, lerp(ny0, ny1, f), fx1, lerp(fy0, fy1, f)], // right wall
        ];
        for (const [ax, ay, bx, by] of edges) {
          ctx.moveTo(ax, ay);
          ctx.lineTo(bx, by);
        }
      }

      // Cross-lines: rectangles stepping back toward the wall.
      for (let k = 0; k <= this.rings; k++) {
        const d = ringDepth(k);
        const x0 = lerp(nx0, vx, d);
        const y0 = lerp(ny0, vy, d);
        const x1 = lerp(nx1, vx, d);
        const y1 = lerp(ny1, vy, d);
        ctx.moveTo(x0, y0);
        ctx.lineTo(x1, y0);
        ctx.lineTo(x1, y1);
        ctx.lineTo(x0, y1);
        ctx.closePath();
      }

      // The back wall's own grid, so it reads as a surface rather than a hole.
      for (let i = 1; i < this.divisions; i++) {
        const f = i / this.divisions;
        ctx.moveTo(lerp(fx0, fx1, f), fy0);
        ctx.lineTo(lerp(fx0, fx1, f), fy1);
        ctx.moveTo(fx0, lerp(fy0, fy1, f));
        ctx.lineTo(fx1, lerp(fy0, fy1, f));
      }

      ctx.stroke();
    }
  },
};

/** Vertical lines on a slowly counter-rotating cylinder. Quieter than the above. */
const cylinder = {
  label: "Cylinder",
  lines: 12,
  radius: 1.35,
  speed: -0.00016,
  color: "rgba(126,220,190,0.16)",

  draw(ctx, { w, h, t, dpr, size }) {
    const spin = t * this.speed;
    ctx.lineWidth = 1 * dpr;

    for (let i = 0; i < this.lines; i++) {
      const a = (i / this.lines) * Math.PI * 2 + spin;
      const x = this.radius * Math.cos(a);
      const z = this.radius * Math.sin(a);
      const persp = 1 / (1 + z * 0.3);

      // Fade with depth so it reads as round rather than flat.
      const depth = (z + this.radius) / (2 * this.radius);
      ctx.globalAlpha = 0.18 + depth * 0.5;
      ctx.strokeStyle = this.color;

      const sx = w / 2 + x * size * persp;
      ctx.beginPath();
      ctx.moveTo(sx, h / 2 - 1.35 * size * persp);
      ctx.lineTo(sx, h / 2 + 1.35 * size * persp);
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
  },
};

/** Horizontal CRT scanlines. */
const scanlines = {
  label: "Scanlines",
  spacing: 0.045,
  drift: 0.0008,
  color: "rgba(140,230,205,0.20)",

  draw(ctx, { w, h, t, dpr }) {
    const spacing = this.spacing * h;
    const phase = (t * this.drift) % spacing;

    ctx.strokeStyle = this.color;
    ctx.lineWidth = 1 * dpr;

    for (let y = -spacing; y < h + spacing; y += spacing) {
      ctx.beginPath();
      ctx.moveTo(0, y + phase);
      ctx.lineTo(w, y + phase);
      ctx.stroke();
    }
  },
};

/** Nothing behind the head. */
const none = { label: "None", draw() {} };

export const backgrounds = {
  maxheadroom,
  matrix,
  holodeck,
  cylinder,
  scanlines,
  none,
};

/** Registry order, which is also the order clicking the avatar cycles through. */
export const backgroundNames = Object.keys(backgrounds);

/** Human-readable name, for tooltips and announcements. */
export function backgroundLabel(name) {
  return (backgrounds[name] ?? maxheadroom).label;
}

/** Resolves a configured name, falling back rather than rendering nothing. */
export function getBackground(name) {
  const def = backgrounds[name] ?? maxheadroom;
  return def.create ? def.create() : def;
}
