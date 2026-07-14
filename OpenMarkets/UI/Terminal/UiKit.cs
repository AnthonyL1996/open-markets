using ColossalFramework.UI;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// Shared UI palette + cell builder used by the terminal's tab bodies. Extracted from the former
    /// MarketPanel/BalancePanel, which duplicated these verbatim. MAIN THREAD only.
    /// </summary>
    internal static class UiKit
    {
        public const float RowH = 22f;

        public static readonly Color32 Up = new Color32(126, 211, 126, 255);   // above base / profit
        public static readonly Color32 Down = new Color32(224, 124, 124, 255); // below base / loss
        public static readonly Color32 Flat = new Color32(220, 220, 220, 255);
        public static readonly Color32 Head = new Color32(180, 200, 230, 255); // header / totals
        public static readonly Color32 Dim = new Color32(120, 120, 120, 255);  // untraded / empty
        public static readonly Color32 Accent = new Color32(120, 175, 245, 255);  // primary action / "your turn"
        public static readonly Color32 Banner = new Color32(150, 44, 44, 235);    // filled alert-banner background
        public static readonly Color32 BannerText = new Color32(255, 232, 232, 255);

        /// <summary>A fixed-width, vertically-centred label cell inside an auto-layout row.</summary>
        public static UILabel Cellate(UIPanel row, float width, string text, Color32 color, UIHorizontalAlignment align)
        {
            UILabel label = row.AddUIComponent<UILabel>();
            label.autoSize = false;
            label.size = new Vector2(width, RowH);
            label.padding = new RectOffset(2, 6, 3, 0);
            label.textScale = 0.75f;
            label.textColor = color;
            label.verticalAlignment = UIVerticalAlignment.Middle;
            label.textAlignment = align;
            label.text = text;
            return label;
        }

        // "EmptySprite" is CS1's built-in 1px tintable white sprite (widely used by mods for solid bars). If a
        // bar renders invisible in-game, this is the name to revisit. Bars use absolute layout inside their
        // own container, so the host's auto-layout doesn't fight them.
        private const string SolidSprite = "EmptySprite";

        /// <summary>A faint horizontal rule for separating sections. Absolute-positioned in <paramref name="host"/>
        /// (does not disturb auto-layout siblings). MAIN THREAD.</summary>
        public static UISprite Divider(UIComponent host, float x, float y, float width)
        {
            UISprite line = host.AddUIComponent<UISprite>();
            line.spriteName = SolidSprite;
            line.color = new Color32(255, 255, 255, 38); // faint rule over the menu panel
            line.size = new Vector2(width, 1f);
            line.relativePosition = new Vector3(x, y);
            return line;
        }

        /// <summary>Emphasise a button as the PRIMARY call-to-action: accent text across all states, so it reads
        /// as the main action next to plain-white secondary buttons. Keeps the menu sprite (sizing unchanged).</summary>
        public static void Primary(UIButton b)
        {
            if (b == null) return;
            b.textColor = Accent;
            b.hoveredTextColor = new Color32(190, 215, 250, 255);
            b.pressedTextColor = Accent;
            b.focusedTextColor = Accent;
        }

        /// <summary>Fill a row as an alert banner (tinted background + bright text) and return the text label.
        /// For the austerity strip, so it reads as a banner rather than a line of red text. MAIN THREAD.</summary>
        public static UILabel BannerCell(UIPanel row, float width, string text)
        {
            row.backgroundSprite = SolidSprite;
            row.color = Banner;
            return Cellate(row, width, text, BannerText, UIHorizontalAlignment.Left);
        }

        /// <summary>A vertical bar/column chart: one bar per value, height ∝ (value − min)/(max − min) of the
        /// series' own window, laid out left→right inside a new container of the given size. Bars are baseline-
        /// aligned (grow upward). Returns the container. MAIN THREAD. &lt;1 value → empty container.</summary>
        public static UIPanel BarChart(UIComponent host, float width, float height, int[] values, Color32 color)
        {
            UIPanel chart = host.AddUIComponent<UIPanel>();
            chart.autoLayout = false;
            chart.size = new Vector2(width, height);
            if (values == null || values.Length == 0) return chart;
            // Stride-sample a long series down before drawing — one UISprite bar per value, so an unbounded input
            // (e.g. a long server-side history ring) would otherwise spawn a sprite per point. Mirrors LineChart.
            if (values.Length > MaxSamples)
            {
                int[] s = new int[MaxSamples];
                for (int k = 0; k < MaxSamples; k++) s[k] = values[(int)((long)k * (values.Length - 1) / (MaxSamples - 1))];
                values = s;
            }

            int min = values[0], max = values[0];
            for (int i = 0; i < values.Length; i++)
            {
                if (values[i] < min) min = values[i];
                if (values[i] > max) max = values[i];
            }
            int range = max - min;
            const float gap = 2f;
            float bw = (width - gap * (values.Length - 1)) / values.Length;
            if (bw < 1f) bw = 1f;
            for (int i = 0; i < values.Length; i++)
            {
                float frac = range == 0 ? 1f : (float)(values[i] - min) / range;
                float bh = 2f + frac * (height - 2f);
                UISprite bar = chart.AddUIComponent<UISprite>();
                bar.spriteName = SolidSprite;
                bar.color = color;
                bar.size = new Vector2(bw, bh);
                bar.relativePosition = new Vector3(i * (bw + gap), height - bh); // baseline-aligned
            }
            return chart;
        }

        /// <summary>A horizontal segmented progress bar: `total` equal segments, the first `filled` tinted
        /// <paramref name="fill"/>, the rest <paramref name="empty"/>. For contract installments paid/remaining.
        /// Absolute layout inside a new container. MAIN THREAD.</summary>
        // Cap on rendered segments: a contract's installment count is only loosely bounded, and one UISprite
        // per segment on the main thread would hang on a pathological value. Above the cap we render exactly
        // MaxSegments and scale `filled` proportionally so the bar still reads as a progress ratio.
        private const int MaxSegments = 60;

        /// <summary>A multi-series DOTTED line chart: one entry in <paramref name="series"/> is one line (a value
        /// per sample), drawn in <paramref name="colors"/>[i]. <paramref name="minY"/>/<paramref name="maxY"/> are
        /// the SHARED y-range across all series (so lines are comparable); a non-positive range is treated as 1.
        /// Each sample maps to x = i/(n-1)·width, y = height − (v−min)/(max−min)·height (clamped to [0,height]); a
        /// ~2px dot is placed at each sample and a few interpolated dots between consecutive samples so it reads as
        /// a continuous line (no CF rotation — unreliable; dot interpolation only). A series longer than MaxSamples
        /// is stride-sampled down first so a long history can't spawn thousands of sprites. Absolute layout inside a
        /// new container of the given size; returns the container. MAIN THREAD. Null/empty series are skipped.</summary>
        private const float DotSize = 2f;
        private const int InterpDots = 3;     // intermediate dots drawn between two consecutive samples (sprite-count vs smoothness)
        private const int MaxSamples = 120;   // stride-sample a longer series down to this before drawing

        public static UIPanel LineChart(UIComponent host, float width, float height, long[][] series, Color32[] colors, long minY, long maxY)
        {
            UIPanel chart = host.AddUIComponent<UIPanel>();
            chart.autoLayout = false;
            chart.size = new Vector2(width, height);
            if (series == null || series.Length == 0) return chart;

            long range = maxY - minY;
            if (range <= 0) range = 1;

            for (int s = 0; s < series.Length; s++)
            {
                long[] raw = series[s];
                if (raw == null || raw.Length == 0) continue;
                Color32 col = (colors != null && s < colors.Length) ? colors[s] : Flat;

                // Stride-sample a long series down to ~MaxSamples points (keep first + last) before plotting.
                long[] vals = raw;
                if (raw.Length > MaxSamples)
                {
                    vals = new long[MaxSamples];
                    for (int k = 0; k < MaxSamples; k++)
                        vals[k] = raw[(int)((long)k * (raw.Length - 1) / (MaxSamples - 1))];
                }

                int n = vals.Length;
                if (n == 1)
                {
                    Dot(chart, col, Px(0, 1, width), Py(vals[0], minY, range, height)); // single sample → one dot
                    continue;
                }

                float prevX = 0f, prevY = 0f;
                for (int i = 0; i < n; i++)
                {
                    float x = Px(i, n, width);
                    float y = Py(vals[i], minY, range, height);
                    if (i > 0)
                    {
                        // Interpolate intermediate dots between the previous sample and this one (continuous read).
                        for (int j = 1; j <= InterpDots; j++)
                        {
                            float t = (float)j / (InterpDots + 1);
                            Dot(chart, col, prevX + (x - prevX) * t, prevY + (y - prevY) * t);
                        }
                    }
                    Dot(chart, col, x, y);
                    prevX = x; prevY = y;
                }
            }
            return chart;
        }

        private static float Px(int i, int n, float width) { return n <= 1 ? 0f : (float)i / (n - 1) * width; }

        private static float Py(long v, long minY, long range, float height)
        {
            float frac = (float)(v - minY) / range;
            float y = height - frac * height;
            if (y < 0f) y = 0f; else if (y > height) y = height;
            return y;
        }

        private static void Dot(UIPanel chart, Color32 color, float cx, float cy)
        {
            UISprite dot = chart.AddUIComponent<UISprite>();
            dot.spriteName = SolidSprite;
            dot.color = color;
            dot.size = new Vector2(DotSize, DotSize);
            dot.relativePosition = new Vector3(cx - DotSize * 0.5f, cy - DotSize * 0.5f); // centre on the point
        }

        /// <summary>Overlay minimal axis labels onto a chart container produced by <see cref="LineChart"/> or
        /// <see cref="BarChart"/>: the y high/low VALUE labels (top-left / bottom-left) and the x oldest→newest
        /// caption under the plot. Small, dim, absolute-positioned inside the chart container so they ride along if
        /// it moves. Pass empty strings to omit a label. MAIN THREAD.</summary>
        public static void AxisLabels(UIPanel chart, float width, float height, string high, string low, string oldest, string newest)
        {
            if (chart == null) return;
            if (!string.IsNullOrEmpty(high)) AxisLabel(chart, 1f, 0f, high, UIHorizontalAlignment.Left);
            if (!string.IsNullOrEmpty(low)) AxisLabel(chart, 1f, height - 12f, low, UIHorizontalAlignment.Left);
            if (!string.IsNullOrEmpty(oldest)) AxisLabel(chart, 1f, height + 1f, oldest, UIHorizontalAlignment.Left);
            if (!string.IsNullOrEmpty(newest)) AxisLabel(chart, width - 80f, height + 1f, newest, UIHorizontalAlignment.Right);
        }

        private static void AxisLabel(UIPanel chart, float x, float y, string text, UIHorizontalAlignment align)
        {
            UILabel l = chart.AddUIComponent<UILabel>();
            l.autoSize = false;
            l.size = new Vector2(80f, 12f);
            l.textScale = 0.6f;
            l.textColor = Dim;
            l.textAlignment = align;
            l.relativePosition = new Vector3(x, y);
            l.text = text;
        }

        public static UIPanel SegmentBar(UIComponent host, float width, float height, int filled, int total, Color32 fill, Color32 empty)
        {
            UIPanel bar = host.AddUIComponent<UIPanel>();
            bar.autoLayout = false;
            bar.size = new Vector2(width, height);
            if (total <= 0) return bar;

            int segs = total, filledSegs = filled;
            if (segs > MaxSegments)
            {
                filledSegs = (int)((long)filled * MaxSegments / total); // preserve the filled ratio
                segs = MaxSegments;
            }
            if (filledSegs > segs) filledSegs = segs;

            const float gap = 2f;
            float sw = (width - gap * (segs - 1)) / segs;
            if (sw < 1f) sw = 1f;
            for (int i = 0; i < segs; i++)
            {
                UISprite seg = bar.AddUIComponent<UISprite>();
                seg.spriteName = SolidSprite;
                seg.color = i < filledSegs ? fill : empty;
                seg.size = new Vector2(sw, height);
                seg.relativePosition = new Vector3(i * (sw + gap), 0f);
            }
            return bar;
        }
    }
}
