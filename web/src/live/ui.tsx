import { h, Fragment } from '../jsx-runtime';

// ---------- formatting ----------
export function num(v: number | null | undefined, digits = 0, unit = ''): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—';
  return v.toFixed(digits) + unit;
}

export function bytesPerSec(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—';
  const u = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let i = 0;
  let x = v;
  while (x >= 1024 && i < u.length - 1) {
    x /= 1024;
    i++;
  }
  return `${x.toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}

export function mib(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—';
  return v >= 1024 ? `${(v / 1024).toFixed(1)} GiB` : `${v.toFixed(0)} MiB`;
}

export function duration(sec: number | null | undefined): string {
  if (sec === null || sec === undefined) return '—';
  const s = Math.max(0, Math.round(sec));
  const d = Math.floor(s / 86400);
  const hh = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${hh}h`;
  if (hh > 0) return `${hh}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

export function clock(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString([], { hour12: false });
}

export function ago(iso: string | null | undefined, nowMs: number): string {
  if (!iso) return 'never';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return 'never';
  const s = Math.max(0, (nowMs - t) / 1000);
  return s < 2 ? 'just now' : `${duration(s)} ago`;
}

export function download(filename: string, text: string, mime = 'application/json') {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  window.setTimeout(() => URL.revokeObjectURL(url), 2000);
}

// ---------- status colour helpers ----------
export function tone(v: number | null | undefined, warn: number, crit: number): string {
  if (v === null || v === undefined) return 'muted';
  if (v >= crit) return 'critical';
  if (v >= warn) return 'warning';
  return 'healthy';
}

export const CHECK_ICON: Record<string, string> = { pass: '✓', warn: '!', fail: '✕', skip: '–', info: 'i', pending: '…' };
export const STATE_LABEL: Record<string, string> = {
  connected: 'Connected',
  degraded: 'Degraded',
  unreachable: 'Unreachable',
  not_found: 'Not found',
  disabled: 'Off',
  pending: 'Checking…',
  error: 'Error',
};

// ---------- small components (plain functions: they hold no state) ----------
export function Badge(props: { kind: string; children?: any }) {
  return <span className={`badge ${props.kind}`}>{props.children}</span>;
}

export function Meter(props: { value: number | null | undefined; max?: number; warn?: number; crit?: number; label?: string }) {
  const max = props.max || 100;
  const v = props.value;
  const pct = v === null || v === undefined ? 0 : Math.max(0, Math.min(100, (v / max) * 100));
  const t = tone(v, props.warn ?? 1e9, props.crit ?? 1e9);
  return (
    <div className="lv-meter" title={props.label}>
      <div className={`lv-meter-fill ${t === 'muted' ? 'muted' : t}`} style={{ width: `${pct}%` }} />
    </div>
  );
}

export function Stat(props: { label: string; value: any; sub?: any; kind?: string }) {
  return (
    <div className={`lv-stat ${props.kind || ''}`}>
      <div className="lv-stat-label">{props.label}</div>
      <div className="lv-stat-value">{props.value}</div>
      {props.sub !== undefined && props.sub !== null && <div className="lv-stat-sub">{props.sub}</div>}
    </div>
  );
}

export function Empty(props: { title: string; children?: any }) {
  return (
    <div className="empty-state">
      <div style={{ fontWeight: 600, color: 'var(--text-primary)', marginBottom: '4px' }}>{props.title}</div>
      <div>{props.children}</div>
    </div>
  );
}

// ---------- charts ----------
export interface Series {
  name: string;
  color: string;
  values: (number | null)[];
  dashed?: boolean;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / p;
  const n = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;
  return n * p;
}

export function LineChart(props: {
  series: Series[];
  times: string[];
  height?: number;
  yMax?: number;
  yMin?: number;
  unit?: string;
  threshold?: { value: number; label: string; color?: string };
  empty?: string;
}) {
  const W = 760;
  const H = props.height || 190;
  const padL = 40;
  const padR = 12;
  const padT = 10;
  const padB = 22;
  const n = props.times.length;
  const flat: number[] = [];
  for (const s of props.series) for (const v of s.values) if (v !== null && v !== undefined) flat.push(v);
  if (n < 2 || flat.length === 0) {
    return (
      <div className="lv-chart-empty">{props.empty || 'Collecting samples — the chart appears after the second refresh.'}</div>
    );
  }
  const yMin = props.yMin ?? 0;
  let yMax = props.yMax ?? niceMax(Math.max(...flat, props.threshold ? props.threshold.value : 0) * 1.08);
  if (yMax <= yMin) yMax = yMin + 1;
  const x = (i: number) => padL + (i / (n - 1)) * (W - padL - padR);
  const y = (v: number) => padT + (1 - (v - yMin) / (yMax - yMin)) * (H - padT - padB);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => yMin + f * (yMax - yMin));
  const labelIdx = [0, Math.floor((n - 1) / 2), n - 1];

  const paths = props.series.map((s) => {
    let d = '';
    let pen = false;
    s.values.forEach((v, i) => {
      if (v === null || v === undefined) {
        pen = false; // a gap, never a zero
        return;
      }
      d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)} `;
      pen = true;
    });
    return d.trim();
  });

  return (
    <div>
      <svg className="lv-chart" viewBox={`0 0 ${W} ${H}`} role="img">
        {ticks.map((t) => (
          <g>
            <line x1={padL} x2={W - padR} y1={y(t)} y2={y(t)} stroke="#263248" strokeWidth="1" />
            <text x={padL - 6} y={y(t) + 3} textAnchor="end" fontSize="10" fill="#64748b">
              {t >= 100 || yMax > 20 ? t.toFixed(0) : t.toFixed(1)}
            </text>
          </g>
        ))}
        {props.threshold && props.threshold.value >= yMin && props.threshold.value <= yMax && (
          <g>
            <line
              x1={padL}
              x2={W - padR}
              y1={y(props.threshold.value)}
              y2={y(props.threshold.value)}
              stroke={props.threshold.color || '#ef4444'}
              strokeWidth="1"
              strokeDasharray="5 4"
            />
            <text x={W - padR} y={y(props.threshold.value) - 4} textAnchor="end" fontSize="10" fill={props.threshold.color || '#ef4444'}>
              {props.threshold.label}
            </text>
          </g>
        )}
        {paths.map((d, i) => (
          <path
            d={d}
            fill="none"
            stroke={props.series[i].color}
            strokeWidth="1.8"
            strokeLinejoin="round"
            strokeDasharray={props.series[i].dashed ? '4 3' : undefined}
          />
        ))}
        {labelIdx.map((i) => (
          <text x={x(i)} y={H - 6} textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'} fontSize="10" fill="#64748b">
            {clock(props.times[i])}
          </text>
        ))}
      </svg>
      {props.series.length > 1 && (
        <div className="lv-legend">
          {props.series.map((s) => (
            <span>
              <i style={{ background: s.color }} />
              {s.name}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export function Sparkline(props: { values: (number | null)[]; color?: string; max?: number; width?: number; height?: number }) {
  const W = props.width || 120;
  const H = props.height || 28;
  const vals = props.values.filter((v) => v !== null && v !== undefined) as number[];
  if (vals.length < 2) return <svg className="lv-spark" viewBox={`0 0 ${W} ${H}`} />;
  const max = props.max ?? Math.max(1, ...vals);
  let d = '';
  let pen = false;
  props.values.forEach((v, i) => {
    if (v === null || v === undefined) {
      pen = false;
      return;
    }
    const px = (i / (props.values.length - 1)) * (W - 2) + 1;
    const py = H - 2 - (Math.min(v, max) / max) * (H - 4);
    d += `${pen ? 'L' : 'M'}${px.toFixed(1)},${py.toFixed(1)} `;
    pen = true;
  });
  return (
    <svg className="lv-spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
      <path d={d.trim()} fill="none" stroke={props.color || '#38bdf8'} strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}

// Heat map: one row per GPU, one column per history sample. `idx` selects util(0) / vram(1) / temp(2) / power(3).
export function Heatmap(props: {
  rows: { id: string; label: string }[];
  points: { g: Record<string, number[]> }[];
  idx: number;
  max: number;
  maxCols?: number;
}) {
  const cols = Math.min(props.maxCols || 120, props.points.length);
  if (cols < 2 || props.rows.length === 0) {
    return <div className="lv-chart-empty">Collecting samples…</div>;
  }
  const pts = props.points.slice(-cols);
  const cw = 760 / cols;
  const rh = 18;
  const H = props.rows.length * rh + 4;
  const color = (v: number | undefined) => {
    if (v === undefined || v === null) return '#1b2436';
    const f = Math.max(0, Math.min(1, v / props.max));
    // blue → amber → red
    const hue = 210 - f * 210;
    return `hsl(${hue.toFixed(0)}, 70%, ${(22 + f * 28).toFixed(0)}%)`;
  };
  return (
    <div className="lv-heat-wrap">
      <div className="lv-heat-labels" style={{ height: `${H}px` }}>
        {props.rows.map((r) => (
          <div style={{ height: `${rh}px` }}>{r.label}</div>
        ))}
      </div>
      <svg className="lv-heat" viewBox={`0 0 760 ${H}`} preserveAspectRatio="none" style={{ height: `${H}px` }}>
        {props.rows.map((r, ri) =>
          pts.map((p, ci) => {
            const v = p.g[r.id] ? p.g[r.id][props.idx] : undefined;
            return <rect x={ci * cw} y={ri * rh + 2} width={cw + 0.4} height={rh - 2} fill={color(v)} />;
          })
        )}
      </svg>
    </div>
  );
}
