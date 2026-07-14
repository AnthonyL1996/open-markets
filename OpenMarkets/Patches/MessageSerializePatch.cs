using System;
using System.Collections.Generic;
using System.Reflection;
using ColossalFramework.IO;
using HarmonyLib;
using OpenMarkets.Notify;

namespace OpenMarkets.Patches
{
    /// <summary>
    /// Save-safety guard for the Chirper-alerts feature. <c>MessageManager.Serialize</c> writes the live
    /// message queue AND the 16-slot recent-message ring into the VANILLA save as a typed object array.
    /// Our custom <c>MessageBase</c> subclass (see <see cref="MarketChirper"/>) would therefore be baked
    /// into the base save and fail to deserialize once the mod is removed — the exact NFR-3 violation
    /// ("mod removal must leave the save loadable").
    ///
    /// This prefix strips our messages from the queue and ring just before the game writes them, so no save
    /// ever contains our type. Runs on the simulation thread during save; dropping our transient chirps from
    /// the in-game ring is harmless (they're cosmetic and re-posted by future events).
    ///
    /// NOTE: the serializer is the NESTED <c>MessageManager.Data.Serialize(DataSerializer)</c> (not a method
    /// on MessageManager itself), and it reaches the live collections via <c>Singleton&lt;MessageManager&gt;</c>
    /// — so we target the nested type and read the singleton directly rather than via <c>__instance</c>.
    /// </summary>
    [HarmonyPatch]
    public static class MessageSerializePatch
    {
        public static MethodBase TargetMethod()
        {
            Type dataType = typeof(MessageManager).GetNestedType(
                "Data", BindingFlags.Public | BindingFlags.NonPublic);
            MethodBase method = dataType != null
                ? AccessTools.Method(dataType, "Serialize", new[] { typeof(DataSerializer) })
                : null;

            if (method == null)
                Log.Warn("Could not resolve MessageManager.Data.Serialize — Chirper save-strip disabled.");

            return method;
        }

        public static void Prefix()
        {
            try
            {
                MessageManager mm = MessageManager.instance;
                if (mm == null) return;

                Queue<MessageBase> queue =
                    Traverse.Create(mm).Field("m_messageQueue").GetValue<Queue<MessageBase>>();
                if (queue != null && queue.Count > 0)
                {
                    MessageBase[] kept = queue.ToArray();
                    queue.Clear();
                    for (int i = 0; i < kept.Length; i++)
                        if (!MarketChirper.IsOurs(kept[i]))
                            queue.Enqueue(kept[i]);
                }

                MessageBase[] ring =
                    Traverse.Create(mm).Field("m_recentMessages").GetValue<MessageBase[]>();
                if (ring != null)
                    for (int i = 0; i < ring.Length; i++)
                        if (ring[i] != null && MarketChirper.IsOurs(ring[i]))
                            ring[i] = null;
            }
            catch (Exception e)
            {
                Log.Error("MessageSerializePatch strip failed: " + e);
            }
        }
    }
}
