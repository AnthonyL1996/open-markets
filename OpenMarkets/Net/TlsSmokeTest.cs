using System;
using System.Collections;
using UnityEngine;
using UnityEngine.Networking;

namespace OpenMarkets.Net
{
    /// <summary>
    /// M4 SPIKE — go/no-go gate for the whole online layer.
    ///
    /// Goal: prove that an HTTPS GET (TLS 1.2) actually succeeds from inside the shipped game build.
    /// The net35/Mono BCL's managed TLS can't negotiate TLS 1.2, so <c>HttpWebRequest</c>/<c>WebClient</c>/
    /// <c>System.Net.Http</c> over HTTPS fail. <see cref="UnityWebRequest"/> uses the OS TLS stack, so it is
    /// the only reliable escape — but it MUST be driven from the Unity MAIN thread (coroutine), never a
    /// background thread. The online price feed is already built on this same
    /// path; this spike is the GATE that confirms the path actually works before M4 Phase A relies on it.
    /// Results land in output_log.txt under [OpenMarkets].
    ///
    /// Trigger: the "Run TLS smoke test" button in the mod's Settings UI (main thread).
    ///
    /// Unity 5.6 API notes (do NOT "modernize" — these break the build against the game's UnityEngine.dll):
    ///   - <c>UnityWebRequest.Get</c> + <c>.Send()</c>  (NOT <c>SendWebRequest()</c> — that is 2017.2+)
    ///   - <c>.isError</c>                              (NOT <c>isNetworkError</c>/<c>isHttpError</c> — 2017.1+)
    ///   - timeout is self-managed via <c>Time.realtimeSinceStartup</c> rather than the <c>.timeout</c>
    ///     property, whose presence in 5.6 is unconfirmed.
    /// </summary>
    public sealed class TlsSmokeTest : MonoBehaviour
    {
        // Several independent HTTPS hosts: success on ANY one proves TLS 1.2 works. Testing more than one
        // means a single host being down doesn't get misread as a TLS failure.
        private static readonly string[] Endpoints =
        {
            "https://raw.githubusercontent.com/octocat/Hello-World/master/README",
            "https://httpbin.org/get",
            "https://example.com/",
        };

        private const float TimeoutSec = 10f;

        // Guard so two quick button presses don't spawn overlapping runs.
        private static bool _running;

        /// <summary>Create a throwaway GameObject + component and run the test. MUST be called on the main thread.</summary>
        public static void Run()
        {
            if (_running)
            {
                Log.Info("TLS smoke test: already running — ignoring repeat trigger.");
                return;
            }
            try
            {
                _running = true;
                GameObject go = new GameObject("OpenMarkets-TlsSmokeTest");
                DontDestroyOnLoad(go);
                TlsSmokeTest comp = go.AddComponent<TlsSmokeTest>();
                comp.StartCoroutine(comp.RunAll(go));
            }
            catch (Exception e)
            {
                _running = false;
                Log.Error("TLS smoke test: failed to start — " + e.Message);
            }
        }

        private IEnumerator RunAll(GameObject host)
        {
            Log.Info("TLS smoke test: starting (UnityWebRequest, main thread, OS TLS stack).");
            int ok = 0;
            for (int i = 0; i < Endpoints.Length; i++)
            {
                bool success = false;
                yield return RunOne(Endpoints[i], r => success = r);
                if (success) ok++;
            }

            if (ok > 0)
                Log.Info("TLS smoke test: DONE — " + ok + "/" + Endpoints.Length
                         + " HTTPS endpoints reachable. TLS 1.2 WORKS → M4 online transport is viable.");
            else
                Log.Error("TLS smoke test: DONE — 0/" + Endpoints.Length
                          + " reachable. TLS 1.2 FAILED from this build → resolve before building M4 Phase A.");

            _running = false;
            UnityEngine.Object.Destroy(host);
        }

        private IEnumerator RunOne(string url, Action<bool> onResult)
        {
            // build + Send in a non-iterator helper — C# forbids yield inside a try/catch, so the
            // request-construction error path must live outside the iterator.
            UnityWebRequest req = BeginRequest(url);
            if (req == null) { onResult(false); yield break; }   // yield break here is OUTSIDE any try/catch → legal

            float start = Time.realtimeSinceStartup;
            while (!req.isDone)
            {
                if (Time.realtimeSinceStartup - start > TimeoutSec)
                {
                    Log.Warn("TLS smoke test: FAIL " + url + " — timed out after " + TimeoutSec + "s.");
                    onResult(false);
                    Abort(req);
                    yield break;
                }
                yield return null;
            }

            onResult(HandleResponse(url, req));   // non-iterator: try/catch + Dispose
        }

        // Build + send. Non-iterator so the try/catch is legal. Disposes if Send() throws after Get().
        private static UnityWebRequest BeginRequest(string url)
        {
            UnityWebRequest req = null;
            try
            {
                req = UnityWebRequest.Get(url);
                req.Send();   // Unity 5.6: Send(), not SendWebRequest()
                return req;
            }
            catch (Exception e)
            {
                Log.Warn("TLS smoke test: FAIL " + url + " — could not start request: " + e.Message);
                if (req != null) { try { req.Dispose(); } catch { } }
                return null;
            }
        }

        // Inspect the completed request, log the verdict, dispose. Non-iterator. Never throws.
        private static bool HandleResponse(string url, UnityWebRequest req)
        {
            try
            {
                if (req.isError)
                {
                    // A TLS failure (vs. a plain network error) shows up as an SSL/handshake message here.
                    Log.Warn("TLS smoke test: FAIL " + url + " — " + req.error);
                    return false;
                }
                string body = req.downloadHandler != null ? req.downloadHandler.text : null;
                int len = body != null ? body.Length : 0;
                Log.Info("TLS smoke test: OK   " + url + " — HTTP " + req.responseCode + ", " + len + " bytes.");
                return true;
            }
            finally
            {
                req.Dispose();
            }
        }

        private static void Abort(UnityWebRequest req)
        {
            try { req.Abort(); req.Dispose(); }
            catch { /* best effort */ }
        }
    }
}
