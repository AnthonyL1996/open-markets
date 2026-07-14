using System;
using System.Collections;
using System.Collections.Generic;
using System.Text;
using UnityEngine;
using UnityEngine.Networking;

namespace OpenMarkets.Net
{
    /// <summary>
    /// MAIN-THREAD one-shot HTTP helper for the online API (account/league/report/contracts). Mirrors the
    /// transport rules of the online layer: net35/Mono can't
    /// negotiate TLS 1.2 over HttpWebRequest, so we use <see cref="UnityWebRequest"/> driven from a Unity
    /// coroutine on the main thread. Unlike the price feed (a perpetual back-off poll), these are
    /// event-driven one-shots fired from UI clicks or a day-rollover hand-off.
    ///
    /// Unity 5.6 API (do NOT modernise): construct <c>new UnityWebRequest(url, verb)</c> + UploadHandlerRaw
    /// for a raw JSON body; <c>.Send()</c> (NOT SendWebRequest); <c>.isError</c>; timeout via
    /// <c>Time.realtimeSinceStartup</c>. The result callback runs on the MAIN thread (so UI handlers can
    /// touch components directly). No value-tuples (net35 has no System.ValueTuple) — results are passed as
    /// three callback args.
    /// </summary>
    public sealed class OmHttp : MonoBehaviour
    {
        public const float RequestTimeoutSec = 8f;

        private static OmHttp _instance;

        // Main-thread dispatch queue: the mod's sim→main marshaling primitive. The SIM thread (e.g. the
        // day-rollover report path) enqueues via OnMainThread; this MonoBehaviour drains it in Update on the
        // MAIN thread. Guarded by a plain lock (net35 has no ConcurrentQueue). The pump must already exist —
        // create the Instance on the main thread at level load so Update runs during gameplay.
        private static readonly Queue<Action> _mainQueue = new Queue<Action>();

        /// <summary>Optional MAIN-THREAD heartbeat invoked once per <see cref="Update"/> with
        /// <c>Time.realtimeSinceStartup</c>. Lets a poller (e.g. <see cref="OpenMarkets.OnlineSync"/>) ride
        /// this MonoBehaviour's frame loop instead of owning its own GameObject. Set to null to disable;
        /// cleared automatically when the pump is torn down (so nothing fires while offline).</summary>
        public static Action<float> Heartbeat;

        /// <summary>Create the pump GameObject if needed. MAIN THREAD ONLY. Call when going online so the
        /// sim→main dispatch (Update) is running before any sim-thread <see cref="OnMainThread"/> enqueue.</summary>
        public static void EnsureRunning()
        {
            OmHttp keep = Instance; // touching Instance creates the GameObject on the main thread
            if (keep == null) Log.Warn("api: pump failed to start");
        }

        /// <summary>Tear down the pump: clear the queue and destroy the GameObject. MAIN THREAD ONLY. Called
        /// when going offline / on level-unload so no DontDestroyOnLoad MonoBehaviour or queued POST lingers
        /// (preserves the dormant-when-off guarantee). Recreated by <see cref="EnsureRunning"/> on re-enable.</summary>
        public static void Stop()
        {
            Heartbeat = null;   // no frame-loop callbacks once the pump is gone (dormant-when-off)
            lock (_mainQueue) { _mainQueue.Clear(); }
            OmHttp inst = _instance;
            _instance = null;
            if (inst != null && inst.gameObject != null)
                UnityEngine.Object.Destroy(inst.gameObject);
        }

        /// <summary>Run <paramref name="action"/> on the MAIN thread on the next Update. SAFE TO CALL FROM
        /// THE SIM THREAD. No-op-safe if the pump isn't running yet (the action just waits in the queue).</summary>
        public static void OnMainThread(Action action)
        {
            if (action == null) return;
            lock (_mainQueue) { _mainQueue.Enqueue(action); }
        }

        private void Update()
        {
            while (true)
            {
                Action a = null;
                lock (_mainQueue) { if (_mainQueue.Count > 0) a = _mainQueue.Dequeue(); }
                if (a == null) break;
                try { a(); } catch (Exception e) { Log.Error("api: main-thread action failed: " + e); }
            }

            // Drive the optional poll heartbeat AFTER draining the queue (so an enqueued action this frame is
            // already applied). Snapshot the delegate: it can be nulled from another path between the check
            // and the call. Never let a poll exception kill the pump.
            Action<float> hb = Heartbeat;
            if (hb != null)
            {
                try { hb(Time.realtimeSinceStartup); }
                catch (Exception e) { Log.Error("api: heartbeat failed: " + e); }
            }
        }

        /// <summary>Lazily create the persistent runner. MAIN THREAD ONLY (creates a GameObject).</summary>
        public static OmHttp Instance
        {
            get
            {
                if (_instance == null)
                {
                    GameObject go = new GameObject("OpenMarkets-Http");
                    UnityEngine.Object.DontDestroyOnLoad(go);
                    _instance = go.AddComponent<OmHttp>();
                }
                return _instance;
            }
        }

        /// <summary>Fire a request. <paramref name="bearer"/> may be null/empty (no auth); <paramref name="json"/>
        /// null/empty → no body. Callback (ok, httpStatus, body) runs on the MAIN thread; ok means a 2xx
        /// response (network/TLS failures and timeouts report ok=false, status 0).</summary>
        public void Request(string method, string url, string json, string bearer, Action<bool, long, string> onResult)
        {
            StartCoroutine(Run(method, url, json, bearer, onResult ?? delegate { }));
        }

        private IEnumerator Run(string method, string url, string json, string bearer, Action<bool, long, string> onResult)
        {
            UnityWebRequest req = Build(method, url, json, bearer);
            if (req == null) { onResult(false, 0, null); yield break; }

            float start = Time.realtimeSinceStartup;
            req.Send(); // Unity 5.6: Send(), not SendWebRequest()
            while (!req.isDone)
            {
                if (Time.realtimeSinceStartup - start > RequestTimeoutSec)
                {
                    Log.Warn("api: " + method + " " + url + " timed out");
                    Abort(req);
                    onResult(false, 0, null);
                    yield break;
                }
                yield return null;
            }
            Deliver(req, method, url, onResult);
        }

        // Build is a non-iterator so the try/catch is legal (can't try/catch across a yield).
        private static UnityWebRequest Build(string method, string url, string json, string bearer)
        {
            UnityWebRequest req = null;
            try
            {
                req = new UnityWebRequest(url, method);
                req.downloadHandler = new DownloadHandlerBuffer();
                if (!string.IsNullOrEmpty(json))
                {
                    byte[] body = Encoding.UTF8.GetBytes(json);
                    req.uploadHandler = new UploadHandlerRaw(body);
                    req.SetRequestHeader("Content-Type", "application/json");
                }
                if (!string.IsNullOrEmpty(bearer))
                    req.SetRequestHeader("Authorization", "Bearer " + bearer);
                return req;
            }
            catch (Exception e)
            {
                Log.Warn("api: could not build request: " + e.Message);
                if (req != null) { try { req.Dispose(); } catch { } }
                return null;
            }
        }

        // Read status/body, dispose, and invoke the callback. Non-iterator so the try/catch + Dispose are legal.
        private static void Deliver(UnityWebRequest req, string method, string url, Action<bool, long, string> onResult)
        {
            bool ok = false;
            long status = 0;
            string body = null;
            try
            {
                status = req.responseCode;
                if (req.isError)
                {
                    Log.Warn("api: " + method + " " + url + " failed: " + req.error);
                }
                else
                {
                    body = req.downloadHandler != null ? req.downloadHandler.text : null;
                    ok = status >= 200 && status < 300;
                }
            }
            catch (Exception e)
            {
                Log.Warn("api: read failed: " + e.Message);
            }
            finally
            {
                req.Dispose();
            }
            if (Settings.IsDebugLogging)
                Log.Info("api: " + method + " " + url + " -> " + status + (ok ? " ok" : " fail"));
            onResult(ok, status, body);
        }

        private static void Abort(UnityWebRequest req)
        {
            try { req.Abort(); req.Dispose(); }
            catch { /* best effort */ }
        }
    }
}
