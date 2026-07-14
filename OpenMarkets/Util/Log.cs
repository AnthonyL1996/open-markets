using UnityEngine;

namespace OpenMarkets
{
    /// <summary>Thin wrapper over Unity's logger. Output shows in the ModTools console (F7)
    /// and the game's player log. Every line is prefixed so it's easy to filter.</summary>
    internal static class Log
    {
        private const string Prefix = "[OpenMarkets] ";

        public static void Info(string message) => Debug.Log(Prefix + message);
        public static void Warn(string message) => Debug.LogWarning(Prefix + message);
        public static void Error(string message) => Debug.LogError(Prefix + message);
    }
}
