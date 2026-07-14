using ColossalFramework.UI;
using UnityEngine;

namespace OpenMarkets.UI.Terminal
{
    /// <summary>
    /// A content panel hosted inside <see cref="MarketTerminal"/>. The shell owns ALL window chrome
    /// (corner button, drag, close, Refresh, tab bar, title); a tab body only builds + refreshes its own
    /// content into the host it is given. MAIN THREAD only.
    /// </summary>
    internal interface ITabBody
    {
        /// <summary>Short label shown on the tab button, e.g. "Market".</summary>
        string TabLabel { get; }

        /// <summary>Shell title-bar text while this tab is active. Computed live (may reflect current data).</summary>
        string Title { get; }

        /// <summary>Create this body's content under <paramref name="host"/> (called once at shell build).</summary>
        void Build(UIComponent host, Vector2 size);

        /// <summary>Rebuild content from current data (on show, on Refresh click, on tab switch).</summary>
        void Refresh();

        /// <summary>Show/hide this body's root.</summary>
        void SetVisible(bool on);
    }
}
