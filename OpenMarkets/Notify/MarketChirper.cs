using System;

namespace OpenMarkets.Notify
{
    /// <summary>
    /// Posts market messages (price-event alerts, co-op notices, …) to the in-game Chirper feed. Callers gate on
    /// <see cref="Settings.IsChirperAlerts"/>. All posting is sim-thread only — <c>MessageManager.QueueMessage</c>
    /// is safe from the sim thread. Posting is wrapped in try/catch and never throws out to the caller.
    /// </summary>
    public static class MarketChirper
    {
        /// <summary>The Chirper sender name shown on the chirp.</summary>
        private const string SenderName = "Open Markets";

        /// <summary>Queue a free-form market message to Chirper. Sim thread; never throws.</summary>
        public static void Post(string text)
        {
            if (string.IsNullOrEmpty(text)) return;
            try
            {
                MessageManager mm = MessageManager.instance;
                if (mm == null) return; // only valid once in-game
                mm.QueueMessage(new MarketMessage(text));
            }
            catch (Exception e)
            {
                Log.Error("MarketChirper post failed: " + e);
            }
        }

        /// <summary>
        /// True if this is one of OUR messages. Used by <see cref="OpenMarkets.Patches.MessageSerializePatch"/>
        /// to strip our messages before the game serializes its queue/ring into the VANILLA save — our
        /// custom <c>MessageBase</c> type would fail to deserialize once the mod is removed (NFR-3).
        /// </summary>
        internal static bool IsOurs(MessageBase message)
        {
            return message is MarketMessage;
        }

        /// <summary>
        /// A custom Chirper message. Subclasses <c>MessageBase</c> from the game assembly; only the text
        /// and sender name matter (no associated citizen, so <c>GetSenderID</c> returns 0).
        /// </summary>
        private sealed class MarketMessage : MessageBase
        {
            private readonly string _text;

            public MarketMessage(string text) { _text = text; }

            public override string GetText() { return _text; }
            public override string GetSenderName() { return SenderName; }
            public override uint GetSenderID() { return 0u; }
        }
    }
}
